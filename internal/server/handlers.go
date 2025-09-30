package server

import (
    "encoding/xml"
    "fmt"
    "io"
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

func NewMux(wc *wecom.WXBizMsgCrypt) *http.ServeMux {
    mux := http.NewServeMux()
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

func handleMessage(wc *wecom.WXBizMsgCrypt, w http.ResponseWriter, r *http.Request) {
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
