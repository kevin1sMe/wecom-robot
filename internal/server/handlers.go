package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"wecom-robot/internal/config"
	"wecom-robot/internal/params"
	"wecom-robot/internal/reader"
	"wecom-robot/internal/wecom"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

// encryptedXML no longer needed; we pass raw body to official decryptor

type receivedMessage struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	AgentID      string   `xml:"AgentID"`
	MsgType      string   `xml:"MsgType"`
	Content      string   `xml:"Content"`
	Event        string   `xml:"Event"`
}

func NewMux(cfg *config.Config, wc *wecom.WXBizMsgCrypt) *http.ServeMux {
	mux := http.NewServeMux()
	proc := reader.NewProcessor(cfg)
	// POST /url — RESTful endpoint to trigger the same pipeline as WeCom messages.
	// Accepts: application/json {"url":"..."} or application/x-www-form-urlencoded url=...
	// Auth: if API_TOKEN is set, requires "Authorization: Bearer <token>" header.
	// Response: {"status":"queued","job":"<sha256[:8]>"}
	mux.HandleFunc("/url", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		// Bearer token auth (enforced only when API_TOKEN is configured)
		if cfg.APIToken != "" {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != cfg.APIToken {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		// Parse URL from JSON body or form
		var rawURL string
		ct := r.Header.Get("Content-Type")
		if strings.HasPrefix(ct, "application/json") {
			var payload struct {
				URL string `json:"url"`
			}
			body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			if err := json.Unmarshal(body, &payload); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			rawURL = strings.TrimSpace(payload.URL)
		} else {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "invalid form", http.StatusBadRequest)
				return
			}
			rawURL = strings.TrimSpace(r.FormValue("url"))
		}

		if rawURL == "" || (!strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://")) {
			http.Error(w, "missing or invalid url", http.StatusBadRequest)
			return
		}

		job := urlJobID(rawURL)
		log.Printf("[/url] received url=%s job=%s", rawURL, job)
		ctx, cancel := context.WithTimeout(context.Background(), params.PipelineTimeout)
		go func() { defer cancel(); proc.ProcessURL(ctx, rawURL, "") }()

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "queued", "job": job})
	})
	// 仅使用根路径作为回调 URL
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 仅处理精确的根路径，其它子路径交给默认 404
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			// 若包含验签参数则执行 URL 验证，否则返回健康检查 OK
			q := r.URL.Query()
			if q.Get("msg_signature") != "" && q.Get("timestamp") != "" && q.Get("nonce") != "" && q.Get("echostr") != "" {
				handleVerify(wc, w, r)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("ok"))
		case http.MethodPost:
			// 若包含消息签名参数则处理消息，否则提示缺少参数
			q := r.URL.Query()
			if q.Get("msg_signature") != "" && q.Get("timestamp") != "" && q.Get("nonce") != "" {
				handleMessage(cfg, proc, wc, w, r)
				return
			}
			http.Error(w, "missing query params", http.StatusBadRequest)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	return mux
}

func handleVerify(wc *wecom.WXBizMsgCrypt, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	signature := q.Get("msg_signature")
	timestamp := q.Get("timestamp")
	nonce := q.Get("nonce")
	echostr := q.Get("echostr")

	if signature == "" || timestamp == "" || nonce == "" || echostr == "" {
		http.Error(w, "missing query params", http.StatusBadRequest)
		return
	}
	// 使用官方实现进行验签 + 解密
	msg, cerr := wc.VerifyURL(signature, timestamp, nonce, echostr)
	if cerr != nil {
		http.Error(w, "decrypt echostr failed", http.StatusForbidden)
		log.Printf("verify decrypt error: %s (%d)", cerr.ErrMsg, cerr.ErrCode)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(msg)
}

func handleMessage(cfg *config.Config, proc *reader.Processor, wc *wecom.WXBizMsgCrypt, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	signature := q.Get("msg_signature")
	timestamp := q.Get("timestamp")
	nonce := q.Get("nonce")

	if signature == "" || timestamp == "" || nonce == "" {
		http.Error(w, "missing query params", http.StatusBadRequest)
		return
	}

	// 读取原始请求体，交给官方实现处理（内含验签、解密、receive_id 校验）
	body, _ := io.ReadAll(r.Body)
	msg, cerr := wc.DecryptMsg(signature, timestamp, nonce, body)
	if cerr != nil {
		http.Error(w, "decrypt failed", http.StatusForbidden)
		log.Printf("decrypt error: %s (%d)", cerr.ErrMsg, cerr.ErrCode)
		return
	}

	log.Printf("收到解密后的消息: %s", string(msg))

	// 尝试解析消息类型；事件类型按标准返回明文 success；非事件回复加密文本 "OK"
	var rm receivedMessage
	if err := xml.Unmarshal(msg, &rm); err != nil {
		log.Printf("[DEBUG] 解析消息失败: %v", err)
		// 如果解析失败，作为兜底：返回明文 success（避免回包失败重试）
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("success"))
		return
	}

	log.Printf("[DEBUG] 解析成功 - MsgType: %s, Content: %s", rm.MsgType, rm.Content)

	if rm.MsgType == "event" {
		log.Printf("[DEBUG] 收到事件消息，直接返回success")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("success"))
		return
	}

	// 如果是文本消息并且包含 http/https 链接，则异步触发 Go 处理流水线
	if rm.MsgType == "text" {
		log.Printf("[DEBUG] 收到文本消息，检查是否包含URL")
		trimmed := strings.TrimSpace(rm.Content)
		preview := trimmed
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		log.Printf("[DEBUG] URL 触发策略: 仅当 Content 以 http/https 开头 时才触发; Content(trim)='%s'", preview)
		if url := firstHTTPURL(rm.Content); url != "" {
			log.Printf("[DEBUG] 发现URL: %s，开始异步处理", url)
			ctx, cancel := context.WithTimeout(context.Background(), params.PipelineTimeout)
			sender := rm.FromUserName
			go func() { defer cancel(); proc.ProcessURL(ctx, url, sender) }()
		} else {
			log.Printf("[DEBUG] 文本消息中未触发URL处理（未以 http/https 开头或为空）")
		}
	} else {
		log.Printf("[DEBUG] 非文本消息，跳过URL处理")
	}

	// 构造被动回复文本消息（OK），使用官方实现加密并生成标准回包 XML
	replyPlain := buildTextReplyXML(rm.FromUserName, rm.ToUserName, "OK")
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	replyNonce := randString(16)
	respXML, cerr := wc.EncryptMsg(replyPlain, ts, replyNonce)
	if cerr != nil {
		log.Printf("encrypt reply failed: %s (%d)", cerr.ErrMsg, cerr.ErrCode)
		// 兜底：按标准返回 success
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("success"))
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write(respXML)
}

func buildTextReplyXML(toUser, fromUser, content string) string {
	now := time.Now().Unix()
	return fmt.Sprintf("<xml>\n<ToUserName><![CDATA[%s]]></ToUserName>\n<FromUserName><![CDATA[%s]]></FromUserName>\n<CreateTime>%d</CreateTime>\n<MsgType><![CDATA[text]]></MsgType>\n<Content><![CDATA[%s]]></Content>\n</xml>", toUser, fromUser, now, content)
}

func randString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

// urlJobID returns the first 8 hex chars of the URL's SHA-256, matching Processor's internal jobShort.
func urlJobID(url string) string {
	sum := sha256.Sum256([]byte(url))
	return fmt.Sprintf("%x", sum[:4])
}

// firstHTTPURL 提取文本中的第一个 http/https 链接
func firstHTTPURL(text string) string {
	s := strings.TrimSpace(text)
	// 严格策略：仅当整个内容以 http/https 开头时返回
	hasPrefix := strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
	if hasPrefix {
		return s
	}
	// 不再匹配正文中的其他链接位置
	return ""
}

// (no-op placeholder removed)
