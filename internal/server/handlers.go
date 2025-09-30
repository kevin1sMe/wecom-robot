package server

import (
	"encoding/xml"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"time"

	"wecom-robot/internal/wecom"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

type encryptedXML struct {
	XMLName    xml.Name `xml:"xml"`
	ToUserName string   `xml:"ToUserName"`
	AgentID    string   `xml:"AgentID"`
	Encrypt    string   `xml:"Encrypt"`
}

type receivedMessage struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	AgentID      string   `xml:"AgentID"`
	MsgType      string   `xml:"MsgType"`
	Content      string   `xml:"Content"`
	Event        string   `xml:"Event"`
}

func NewMux(wc *wecom.Crypto) *http.ServeMux {
    mux := http.NewServeMux()
    mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
        switch r.Method {
        case http.MethodGet:
            handleVerify(wc, w, r)
        case http.MethodPost:
            handleMessage(wc, w, r)
        default:
            w.WriteHeader(http.StatusMethodNotAllowed)
        }
    })

    // 支持根路径作为回调 URL（便于直接将域名配置为回调）
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
                handleMessage(wc, w, r)
                return
            }
            http.Error(w, "missing query params", http.StatusBadRequest)
        default:
            w.WriteHeader(http.StatusMethodNotAllowed)
        }
    })
    return mux
}

func handleVerify(wc *wecom.Crypto, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	signature := q.Get("msg_signature")
	timestamp := q.Get("timestamp")
	nonce := q.Get("nonce")
	echostr := q.Get("echostr")

	if signature == "" || timestamp == "" || nonce == "" || echostr == "" {
		http.Error(w, "missing query params", http.StatusBadRequest)
		return
	}
	if !wc.VerifySignature(signature, timestamp, nonce, echostr) {
		http.Error(w, "invalid signature", http.StatusForbidden)
		return
	}
	msg, err := wc.Decrypt(echostr)
	if err != nil {
		http.Error(w, "decrypt echostr failed", http.StatusForbidden)
		log.Printf("verify decrypt error: %v", err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(msg)
}

func handleMessage(wc *wecom.Crypto, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	signature := q.Get("msg_signature")
	timestamp := q.Get("timestamp")
	nonce := q.Get("nonce")

	if signature == "" || timestamp == "" || nonce == "" {
		http.Error(w, "missing query params", http.StatusBadRequest)
		return
	}

	var req encryptedXML
	decoder := xml.NewDecoder(r.Body)
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, "invalid xml", http.StatusBadRequest)
		return
	}
	if req.Encrypt == "" {
		http.Error(w, "missing Encrypt field", http.StatusBadRequest)
		return
	}
	if !wc.VerifySignature(signature, timestamp, nonce, req.Encrypt) {
		http.Error(w, "invalid signature", http.StatusForbidden)
		return
	}
	msg, err := wc.Decrypt(req.Encrypt)
	if err != nil {
		http.Error(w, "decrypt failed", http.StatusForbidden)
		log.Printf("decrypt error: %v", err)
		return
	}

	log.Printf("收到解密后的消息: %s", string(msg))

	// 尝试解析消息类型；事件类型按标准返回明文 success；非事件回复加密文本 "OK"
	var rm receivedMessage
	if err := xml.Unmarshal(msg, &rm); err != nil {
		// 如果解析失败，作为兜底：返回明文 success（避免回包失败重试）
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("success"))
		return
	}

	if rm.MsgType == "event" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("success"))
		return
	}

	// 构造被动回复文本消息（OK），并加密回包
	replyPlain := buildTextReplyXML(rm.FromUserName, rm.ToUserName, "OK")
	encrypt, err := wc.Encrypt([]byte(replyPlain))
	if err != nil {
		log.Printf("encrypt reply failed: %v", err)
		// 兜底：按标准返回 success
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("success"))
		return
	}

	ts := strconv.FormatInt(time.Now().Unix(), 10)
	replyNonce := randString(16)
	sig := wc.Sign(ts, replyNonce, encrypt)

	// 标准加密回包 XML
	resp := fmt.Sprintf("<xml>\n<Encrypt><![CDATA[%s]]></Encrypt>\n<MsgSignature><![CDATA[%s]]></MsgSignature>\n<TimeStamp>%s</TimeStamp>\n<Nonce><![CDATA[%s]]></Nonce>\n</xml>", encrypt, sig, ts, replyNonce)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write([]byte(resp))
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
