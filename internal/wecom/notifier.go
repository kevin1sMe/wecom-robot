package wecom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Notifier sends active messages via the WeCom messaging API.
// Access tokens are cached in-memory and refreshed proactively before expiry.
type Notifier struct {
	corpID     string
	corpSecret string
	agentID    int

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time

	hc *http.Client
}

func NewNotifier(corpID, corpSecret string, agentID int) *Notifier {
	return &Notifier{
		corpID:     corpID,
		corpSecret: corpSecret,
		agentID:    agentID,
		hc:         &http.Client{Timeout: 10 * time.Second},
	}
}

func (n *Notifier) Enabled() bool {
	return n.corpID != "" && n.corpSecret != ""
}

func (n *Notifier) token(ctx context.Context) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.accessToken != "" && time.Now().Before(n.tokenExpiry) {
		return n.accessToken, nil
	}
	url := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=%s&corpsecret=%s",
		n.corpID, n.corpSecret)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := n.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("gettoken: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var result struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return "", fmt.Errorf("gettoken parse: %w", err)
	}
	if result.ErrCode != 0 {
		return "", fmt.Errorf("gettoken errcode=%d: %s", result.ErrCode, result.ErrMsg)
	}
	n.accessToken = result.AccessToken
	// cache for 90% of expires_in to avoid using a token right as it expires
	n.tokenExpiry = time.Now().Add(time.Duration(float64(result.ExpiresIn)*0.9) * time.Second)
	return n.accessToken, nil
}

// SendTextCard sends a WeCom textcard message to toUser.
// description is plain text, max 512 chars (truncated automatically).
func (n *Notifier) SendTextCard(ctx context.Context, toUser, title, description, readwiseURL string) error {
	if !n.Enabled() {
		return fmt.Errorf("notifier not configured")
	}
	tok, err := n.token(ctx)
	if err != nil {
		return err
	}

	const maxDesc = 512
	if len([]rune(description)) > maxDesc {
		runes := []rune(description)
		description = string(runes[:maxDesc-1]) + "…"
	}

	payload := map[string]any{
		"touser":  toUser,
		"msgtype": "textcard",
		"agentid": n.agentID,
		"textcard": map[string]string{
			"title":       title,
			"description": description,
			"url":         readwiseURL,
			"btntxt":      "在 Readwise 阅读",
		},
	}
	body, _ := json.Marshal(payload)

	apiURL := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/message/send?access_token=%s", tok)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.hc.Do(req)
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(rb, &result); err != nil {
		return fmt.Errorf("send message parse: %w", err)
	}
	if result.ErrCode != 0 {
		return fmt.Errorf("send message errcode=%d: %s", result.ErrCode, result.ErrMsg)
	}
	return nil
}
