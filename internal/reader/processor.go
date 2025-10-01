package reader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wecom-robot/internal/cache"
	"wecom-robot/internal/config"
	"wecom-robot/internal/mcpclient"
	"wecom-robot/internal/params"

	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

// Processor coordinates: fetch via MCP streamable HTTP -> LLM extract -> Readwise save
type Processor struct {
	cfg *config.Config
	hc  *http.Client
	rc  *cache.Redis
}

func NewProcessor(cfg *config.Config) *Processor {
	p := &Processor{
		cfg: cfg,
		hc:  &http.Client{Timeout: params.HTTPClientTimeout},
	}
	// Optional Redis cache
	if strings.TrimSpace(cfg.RedisAddr) != "" {
		ttl := time.Duration(cfg.RedisTTLSeconds) * time.Second
		p.rc = cache.NewRedis(cfg.RedisAddr, cfg.RedisPrefix, ttl)
	}
	return p
}

// ProcessURL runs the full pipeline asynchronously-safe. Logs errors; no panics.
func (p *Processor) ProcessURL(ctx context.Context, url string) {
    start := time.Now()
    job := hashURL(url)
    jobShort := job
    if len(jobShort) > 8 {
        jobShort = jobShort[:8]
    }
    traceDir := p.makeTraceDir(url)
    if traceDir != "" {
        log.Printf("[reader] job=%s trace_dir=%s", jobShort, traceDir)
    }
    p.traceWrite(traceDir, "url.txt", []byte(strings.TrimSpace(url)))
    p.traceWrite(traceDir, "job.txt", []byte(jobShort))

	log.Printf("[reader] job=%s step=process event=start url=%s", jobShort, url)

	// Step: fetch HTML (<= 5m)
	stepFetchStart := time.Now()
	log.Printf("[reader] job=%s step=fetch_html event=start", jobShort)
	ctxFetch, cancelFetch := context.WithTimeout(ctx, params.StepTimeout)
	html, fromCache, err := p.fetchHTML(ctxFetch, url)
	cancelFetch()
	if err != nil {
		log.Printf("[reader] job=%s step=fetch_html event=error err=%v", jobShort, err)
		p.traceWrite(traceDir, "error.txt", []byte("fetch: "+err.Error()))
		return
	}
	p.traceWrite(traceDir, "fetch_source.txt", []byte(map[bool]string{true: "cache", false: "mcp"}[fromCache]))
	p.traceWrite(traceDir, "fetch.html", []byte(html))
	log.Printf("[reader] job=%s step=fetch_html event=end from_cache=%t bytes=%d dur=%s", jobShort, fromCache, len(html), time.Since(stepFetchStart))

	// Step: extract metadata (<= 5m) and receive Readwise-ready body
	stepExtractStart := time.Now()
	log.Printf("[reader] job=%s step=extract_meta event=start", jobShort)
	ctxExtract, cancelExtract := context.WithTimeout(ctx, params.StepTimeout)
	body, metaFromCache, err := p.extractMetadata(ctxExtract, html, url, traceDir)
	cancelExtract()
	if err != nil {
		log.Printf("[reader] job=%s step=extract_meta event=error err=%v", jobShort, err)
		p.traceWrite(traceDir, "error.txt", []byte("extract: "+err.Error()))
		return
	}
	// quick body size for visibility
	var bodyBytes int
	if b, _ := json.Marshal(body); len(b) > 0 {
		bodyBytes = len(b)
	}
	log.Printf("[reader] job=%s step=extract_meta event=end from_cache=%t keys=%d bytes=%d dur=%s", jobShort, metaFromCache, len(body), bodyBytes, time.Since(stepExtractStart))

	// Step: save to Readwise (<= 5m)
	stepSaveStart := time.Now()
	log.Printf("[reader] job=%s step=save_readwise event=start", jobShort)
	ctxSave, cancelSave := context.WithTimeout(ctx, params.StepTimeout)
	defer cancelSave()
	link, err := p.saveToReadwise(ctxSave, body, html, traceDir)
	if err != nil {
		log.Printf("[reader] job=%s step=save_readwise event=error err=%v", jobShort, err)
		p.traceWrite(traceDir, "error.txt", []byte("readwise: "+err.Error()))
		return
	}
	if strings.TrimSpace(link) != "" {
		log.Printf("[reader] job=%s step=save_readwise event=end dur=%s url=%s", jobShort, time.Since(stepSaveStart), link)
	} else {
		log.Printf("[reader] job=%s step=save_readwise event=end dur=%s", jobShort, time.Since(stepSaveStart))
	}

	log.Printf("[reader] job=%s step=process event=end url=%s dur=%s", jobShort, url, time.Since(start))
}

// fetchHTML fetches content strictly via MCP HTTP server (streamable-http).
func (p *Processor) fetchHTML(ctx context.Context, url string) (string, bool, error) {
	url = strings.TrimSpace(url)
	if p.cfg.MCPHTTPURL == "" {
		return "", false, errors.New("MCP_HTTP_URL 未配置；本服务仅通过 MCP streamable http 抓取，不支持直接 HTTP")
	}
	// Try Redis/file cache first
	if cached, ok := p.tryReadCache(ctx, url); ok {
		log.Printf("[reader] cache hit for url=%s", url)
		return cached, true, nil
	}
	log.Printf("[reader] cache miss for url=%s", url)

	html, err := p.fetchViaMCPHTTP(ctx, url)
	if err != nil {
		return "", false, fmt.Errorf("mcp fetch: %w", err)
	}
	if html == "" {
		return "", false, errors.New("mcp fetch 返回空内容")
	}
	// Write cache best-effort (Redis then file)
	if err := p.writeCache(ctx, url, html); err != nil {
		log.Printf("[reader] cache write failed: %v", err)
	}
	return html, false, nil
}

// NOTE: direct HTTP fetching is intentionally disabled per requirements.

// fetchViaMCPHTTP calls a remote MCP HTTP server tool (default tool "http") with {url, method}
func (p *Processor) fetchViaMCPHTTP(ctx context.Context, url string) (string, error) {
	cli := mcpclient.New(p.cfg.MCPHTTPURL, p.cfg.MCPToolName)
	return cli.FetchURL(ctx, url)
}

// extractMetadata asks the LLM to return a strict JSON object as metadata
// extractMetadata returns a Readwise-ready body map. It encapsulates LLM extraction
// and local caching (like fetchHTML), and writes trace logs.
func (p *Processor) extractMetadata(ctx context.Context, html, url, traceDir string) (map[string]any, bool, error) {
	if p.cfg.LLMBaseURL == "" || p.cfg.LLMAPIKey == "" || p.cfg.LLMModel == "" {
		return nil, false, errors.New("LLM config missing (LLM_BASE_URL, LLM_API_KEY, LLM_MODEL)")
	}
	// Truncate excessively long HTML to reduce token usage
	const maxChars = 200_000
	if len(html) > maxChars {
		// html = html[:maxChars]
		log.Printf("html toooooo long, %d", len(html))
	}

	systemPrompt := "你是一名严谨的网页内容解析器。只输出一个合法 JSON 对象，不要输出任何多余字符，也不要使用 Markdown 代码块。严禁在 JSON 中包含原始 HTML 或 Markdown 内容（不要返回 html 字段）。JSON 必须严格符合 Readwise Reader 保存接口所需的字段与类型。"
	userPrompt := buildExtractionPrompt(url, html)
	p.traceWrite(traceDir, "extract_prompt_system.txt", []byte(systemPrompt))
	p.traceWrite(traceDir, "extract_prompt_user.txt", []byte(userPrompt))

	// Try body cache first
	if cached, ok := p.tryReadMetaCache(ctx, url); ok {
		if b, _ := json.MarshalIndent(cached, "", "  "); len(b) > 0 {
			p.traceWrite(traceDir, "extracted_cached.json", b)
		}
		return cached, true, nil
	}

	// OpenAI official Go SDK
	opts := []option.RequestOption{option.WithAPIKey(p.cfg.LLMAPIKey)}
	if p.cfg.LLMBaseURL != "" {
		opts = append(opts, option.WithBaseURL(strings.TrimRight(p.cfg.LLMBaseURL, "/")))
	}
	client := openai.NewClient(opts...)

	params := openai.ChatCompletionNewParams{
		Model: shared.ChatModel(p.cfg.LLMModel),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(userPrompt),
		},
	}
	// Some providers/models (e.g. certain Azure model groups) only support default temperature.
	// Only set Temperature if configured via env to avoid 400s like
	// "Unsupported value: 'temperature' does not support 0.2 with this model".
	if p.cfg.LLMTemperature != nil {
		params.Temperature = openai.Float(*p.cfg.LLMTemperature)
	}

	resp, err := client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, false, fmt.Errorf("llm request: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, false, errors.New("llm no choices")
	}
	txt := strings.TrimSpace(resp.Choices[0].Message.Content)
	// strip code fences if present
	txt = strings.TrimPrefix(txt, "```json")
	txt = strings.TrimPrefix(txt, "```")
	txt = strings.TrimSuffix(txt, "```")
	txt = strings.TrimSpace(txt)
	p.traceWrite(traceDir, "llm_raw_response.txt", []byte(txt))

	var meta map[string]any
	if err := json.Unmarshal([]byte(txt), &meta); err != nil {
		return nil, false, fmt.Errorf("llm json parse: %w", err)
	}
	if b, _ := json.MarshalIndent(meta, "", "  "); len(b) > 0 {
		p.traceWrite(traceDir, "extracted.json", b)
	}
	// Build body and cache it
	body := buildReadwiseBody(meta)
	if b, _ := json.MarshalIndent(body, "", "  "); len(b) > 0 {
		p.traceWrite(traceDir, "extracted_body.json", b)
	}
	_ = p.writeMetaCache(ctx, url, body)
	return body, false, nil
}

func buildExtractionPrompt(url, html string) string {
	// Use China Standard Time (+08:00) for temporal reasoning
	cst := time.FixedZone("CST", 8*3600)
	fetchTime := time.Now().In(cst).Format(time.RFC3339)
	currentYear := time.Now().In(cst).Year()

	var sb strings.Builder
	sb.WriteString("你是一名严谨的网页内容解析器。请从给定 HTML 中提取文章字段，并输出唯一一个 JSON 对象。除 JSON 外不要返回任何多余文字。\n\n")
	sb.WriteString("【任务目标】\n")
	sb.WriteString("- 从HTML提取适合Readwise Reader的字段，生成干净的article HTML与结构化元信息\n")
	sb.WriteString("- 不臆造：未知则置为null\n")
	sb.WriteString("- 保留内容语义与层级\n\n")

	sb.WriteString("【重要时间信息】\n")
	sb.WriteString(fmt.Sprintf("- 抓取时间：%s\n", fetchTime))
	sb.WriteString(fmt.Sprintf("- 当前年份：%d年（时区：CST +08:00）\n\n", currentYear))

	sb.WriteString("【输入】\n")
	sb.WriteString("- 页面URL：")
	sb.WriteString(url)
	sb.WriteString("\n- HTML内容：\n")
	sb.WriteString(html)
	sb.WriteString("\n\n")

	sb.WriteString("【输出JSON字段定义】\n")
	sb.WriteString("{\n")
	sb.WriteString("  \"url\": string,\n")
	sb.WriteString("  \"should_clean_html\": boolean,\n")
	sb.WriteString("  \"title\": string|null,\n")
	sb.WriteString("  \"author\": string|null,\n")
	sb.WriteString("  \"published_date\": string|null,\n")
	sb.WriteString("  \"image_url\": string|null,\n")
	sb.WriteString("  \"summary\": string|null,\n")
	sb.WriteString("  \"category\": \"article\"|\"email\"|\"rss\"|\"highlight\"|\"note\"|\"pdf\"|\"epub\"|\"tweet\"|\"video\"|null,\n")
	sb.WriteString("  \"tags\": string[],\n")
	sb.WriteString("  \"notes\": string|null,\n")
	sb.WriteString("  \"location\": \"new\"|\"later\"|\"archive\"|\"feed\"|null,\n")
	sb.WriteString("  \"saved_using\": string|null\n")
	sb.WriteString("}\n\n")

	sb.WriteString("【提取规则】\n")
	sb.WriteString("1. url：使用输入的URL\n")
	sb.WriteString("2. should_clean_html：设为true，让API自动清理HTML\n")
	sb.WriteString("3. title：优先级：og:title > title标签 > h1标签 > 从内容推断\n")
	sb.WriteString("4. author：优先级：og:article:author > meta[name=\"author\"] > byline文本 > 作者署名\n\n")

	sb.WriteString("6. published_date（重要）：\n")
	sb.WriteString("   a) 优先查找meta时间：article:published_time、publishdate、date 等\n")
	sb.WriteString("   b) 在正文中匹配日期：‘YYYY年MM月DD日’、‘YYYY-MM-DD’、‘MM月DD日HH:MM’ 等；‘今天/昨天/前天’等需换算\n")
	sb.WriteString("   c) 年份缺失时，使用当前年份：")
	sb.WriteString(fmt.Sprintf("%d年\n", currentYear))
	sb.WriteString("   d) 输出格式：严格ISO8601，且使用东八区（+08:00），如：YYYY-MM-DDTHH:MM:SS+08:00\n")
	sb.WriteString("   e) 若完全无时间信息，则置为null\n\n")

	sb.WriteString("7. image_url：优先级：og:image > twitter:image > 第一张内容图片（必须绝对URL）\n")
	sb.WriteString("8. summary：基于文章内容生成客观概述，2-4句话\n")
	sb.WriteString("9. category：按内容类型判断，通常为‘article’\n")
	sb.WriteString("10. tags：从标题和内容中提取3-8个标签（字符串数组）\n")
	sb.WriteString("11. location：固定设为‘new’\n")
	sb.WriteString("12. saved_using：固定设为‘web_extractor’\n\n")

	sb.WriteString("【重要提醒】\n")
	sb.WriteString("- published_date：当出现‘X月Y日Z:Z’时，年份必须使用当前年份；最终必须为+08:00的ISO8601\n")
	sb.WriteString("- 所有URL转换为绝对URL\n")
	sb.WriteString("- 不要臆造，不确定设为null\n")
	sb.WriteString("- 严禁在输出JSON中包含原始HTML或Markdown内容（不要输出 html 字段）\n\n")

	sb.WriteString("【输出要求】\n")
	sb.WriteString("- 仅返回合法JSON对象，字段与类型严格符合Readwise Reader API\n")
	sb.WriteString("- published_date为完整ISO8601(+08:00)或null\n")
	sb.WriteString("- 不要包含 html 字段；should_clean_html=true；location=\"new\"；saved_using=\"web_extractor\"\n")
	return sb.String()
}

func (p *Processor) saveToReadwise(ctx context.Context, meta map[string]any, originalHTML string, traceDir string) (string, error) {
	token := p.cfg.ReadwiseToken
	if token == "" {
		return "", errors.New("missing READWISE_API_TOKEN")
	}
	// strictly type and sanitize per Reader API
	body := make(map[string]any)
	if s, ok := toString(meta["url"]); ok {
		body["url"] = s
	}
    // Include original fetched HTML only if it looks like HTML (avoid forwarding JSON envelopes)
    if hs := strings.TrimSpace(originalHTML); hs != "" && looksLikeHTML(hs) {
        body["html"] = hs
    }
	body["should_clean_html"] = toBool(meta["should_clean_html"], true)
	if s, ok := toString(meta["title"]); ok {
		body["title"] = s
	}
	if s, ok := toString(meta["author"]); ok {
		body["author"] = s
	}
	if s, ok := toString(meta["published_date"]); ok {
		if iso, ok2 := normalizePublishedDateChina(s); ok2 {
			body["published_date"] = iso
		}
	}
	if s, ok := toString(meta["image_url"]); ok {
		body["image_url"] = s
	}
	if s, ok := toString(meta["summary"]); ok {
		body["summary"] = s
	}
	if s, ok := toString(meta["category"]); ok {
		body["category"] = s
	}
	if tags := toStringSlice(meta["tags"]); len(tags) > 0 {
		body["tags"] = tags
	}
	if s, ok := toNotesString(meta["notes"]); ok {
		body["notes"] = s
	}
	if s, ok := toString(meta["location"]); ok {
		switch strings.ToLower(s) {
		case "new", "later", "archive", "feed":
			body["location"] = strings.ToLower(s)
		}
	}
	if s, ok := toString(meta["saved_using"]); ok {
		body["saved_using"] = s
	}
	// defaults
	if _, ok := body["location"]; !ok {
		body["location"] = "new"
	}
	if _, ok := body["category"]; !ok {
		body["category"] = "article"
	}
	if _, ok := body["saved_using"]; !ok {
		body["saved_using"] = "web_extractor"
	}

	// Ensure/derive image_url
	baseURL, _ := toString(meta["url"])
	if v, ok := body["image_url"]; ok {
		s := toStringMust(v)
		if s != "" && !isLikelyHTTPURL(s) {
			if ru := resolveURL(baseURL, s); ru != "" {
				body["image_url"] = ru
			}
		}
	}
	// Try meta og:image/twitter:image from original HTML
	if _, ok := body["image_url"]; !ok || toStringMust(body["image_url"]) == "" {
		if u := firstMetaImageURL(originalHTML); u != "" {
			if ru := resolveURL(baseURL, u); ru != "" {
				body["image_url"] = ru
			} else {
				body["image_url"] = u
			}
		}
	}
	// Fallback: if still missing, pick first image from original HTML
	if _, ok := body["image_url"]; !ok || toStringMust(body["image_url"]) == "" {
		if u := firstImageURL(originalHTML); u != "" {
			if ru := resolveURL(baseURL, u); ru != "" {
				body["image_url"] = ru
			} else {
				body["image_url"] = u
			}
		}
	}

	b, _ := json.Marshal(body)
	p.traceWrite(traceDir, "readwise_request.json", b)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://readwise.io/api/v3/save/", bytes.NewReader(b))
	req.Header.Set("Authorization", "Token "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("readwise request: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	p.traceWrite(traceDir, fmt.Sprintf("readwise_response_%d.txt", resp.StatusCode), rb)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("readwise status %d: %s", resp.StatusCode, string(rb))
	}
	// Try to parse response to extract created document link
	var link, id string
	var respJSON map[string]any
	if err := json.Unmarshal(rb, &respJSON); err == nil {
		if s, ok := toString(respJSON["url"]); ok {
			link = s
		}
		if s, ok := toString(respJSON["id"]); ok {
			id = s
		}
	}
	// Fallback: construct from id if url missing
	if link == "" && id != "" {
		link = "https://read.readwise.io/read/" + id
	}
	if link != "" {
		if id != "" {
			log.Printf("[reader] readwise saved OK id=%s url=%s", id, link)
		} else {
			log.Printf("[reader] readwise saved OK url=%s", link)
		}
	} else {
		log.Printf("[reader] readwise saved OK (no url in response)")
	}
	return link, nil
}

// makeTraceDir returns a unique trace dir for this URL processing run.
func (p *Processor) makeTraceDir(url string) string {
	root := strings.TrimSpace(p.cfg.ReaderLogDir)
	if root == "" {
		return ""
	}
	ts := time.Now().Format("20060102_150405")
	h := hashURL(url)
	dir := filepath.Join(root, fmt.Sprintf("%s-%s", h[:12], ts))
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// traceWrite writes a file into trace dir (best-effort, ignores errors).
func (p *Processor) traceWrite(dir, name string, data []byte) {
	if dir == "" || name == "" {
		return
	}
	path := filepath.Join(dir, name)
	_ = os.WriteFile(path, data, 0o644)
}

// Helpers for typing/sanitization
func toString(v any) (string, bool) {
	switch t := v.(type) {
	case nil:
		return "", false
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return "", false
		}
		return s, true
	case fmt.Stringer:
		s := strings.TrimSpace(t.String())
		if s == "" {
			return "", false
		}
		return s, true
	default:
		s := strings.TrimSpace(fmt.Sprintf("%v", v))
		if s == "" || s == "<nil>" {
			return "", false
		}
		return s, true
	}
}

// toStringMust converts to string using toString, returns empty on failure.
func toStringMust(v any) string {
	if s, ok := toString(v); ok {
		return s
	}
	return ""
}

func toBool(v any, def bool) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		if s == "true" || s == "1" || s == "yes" {
			return true
		}
		if s == "false" || s == "0" || s == "no" {
			return false
		}
	case float64:
		return t != 0
	case int, int64:
		return fmt.Sprintf("%v", v) != "0"
	}
	return def
}

func toStringSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return filterNotEmpty(t)
	case []any:
		out := make([]string, 0, len(t))
		for _, it := range t {
			if s, ok := toString(it); ok {
				out = append(out, s)
			}
		}
		return filterNotEmpty(out)
	case string:
		// allow comma-separated
		parts := strings.Split(t, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return filterNotEmpty(parts)
	default:
		return nil
	}
}

func toNotesString(v any) (string, bool) {
	if s, ok := toString(v); ok {
		return s, true
	}
	switch t := v.(type) {
	case []any:
		parts := make([]string, 0, len(t))
		for _, it := range t {
			if s, ok := toString(it); ok {
				parts = append(parts, s)
			}
		}
		s := strings.TrimSpace(strings.Join(parts, "\n"))
		if s == "" {
			return "", false
		}
		return s, true
	case map[string]any:
		b, _ := json.Marshal(t)
		s := strings.TrimSpace(string(b))
		if s == "" {
			return "", false
		}
		return s, true
	}
	return "", false
}

func filterNotEmpty(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// looksLikeHTML makes a light-weight guess whether the string is HTML markup.
func looksLikeHTML(s string) bool {
    if s == "" {
        return false
    }
    t := strings.ToLower(strings.TrimSpace(s))
    if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
        return false
    }
    if strings.HasPrefix(t, "<!doctype") || strings.HasPrefix(t, "<html") || strings.HasPrefix(t, "<head") || strings.HasPrefix(t, "<body") {
        return true
    }
    // Heuristic: contains common block-level tags
    if strings.Contains(t, "<p") || strings.Contains(t, "</p>") || strings.Contains(t, "<div") || strings.Contains(t, "</div>") || strings.Contains(t, "<article") {
        return true
    }
    return false
}

// isLikelyHTTPURL returns true if s starts with http:// or https://
func isLikelyHTTPURL(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// resolveURL resolves ref against base. Returns empty if cannot resolve.
func resolveURL(base, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if strings.HasPrefix(ref, "data:") || strings.HasPrefix(ref, "blob:") {
		return ""
	}
	// Protocol-relative
	if strings.HasPrefix(ref, "//") {
		return "https:" + ref
	}
	// Already absolute
	if isLikelyHTTPURL(ref) {
		return ref
	}
	bu, err := url.Parse(base)
	if err != nil || bu == nil {
		return ""
	}
	ru, err := url.Parse(ref)
	if err != nil || ru == nil {
		return ""
	}
	return bu.ResolveReference(ru).String()
}

// firstImageURL tries to find the first <img> src or srcset URL in the html.
// It returns the raw value (may be relative); caller should resolve to absolute.
func firstImageURL(html string) string {
	if strings.TrimSpace(html) == "" {
		return ""
	}
	// Make a lowercase copy for searching while slicing from original
	low := strings.ToLower(html)
	i := 0
	for {
		idx := strings.Index(low[i:], "<img")
		if idx < 0 {
			break
		}
		// absolute position
		pos := i + idx
		// find end of tag
		end := strings.IndexByte(low[pos:], '>')
		if end < 0 {
			break
		}
		endPos := pos + end
		tag := html[pos : endPos+1]
		tagLow := low[pos : endPos+1]
		// Try src= first
		if u := extractAttr(tag, tagLow, "src"); u != "" {
			if isAcceptableImageURL(u) {
				return u
			}
		}
		// Try data-src (lazy load)
		if u := extractAttr(tag, tagLow, "data-src"); u != "" {
			if isAcceptableImageURL(u) {
				return u
			}
		}
		// Try data-original
		if u := extractAttr(tag, tagLow, "data-original"); u != "" {
			if isAcceptableImageURL(u) {
				return u
			}
		}
		// Try srcset: choose the first URL before whitespace/comma
		if ss := extractAttr(tag, tagLow, "srcset"); ss != "" {
			// srcset entries are like: "url1 1x, url2 2x"
			comma := strings.Index(ss, ",")
			if comma >= 0 {
				ss = ss[:comma]
			}
			// take up to first whitespace
			for j := 0; j < len(ss); j++ {
				if ss[j] == ' ' || ss[j] == '\t' || ss[j] == '\n' {
					ss = ss[:j]
					break
				}
			}
			if isAcceptableImageURL(ss) {
				return ss
			}
		}
		// advance
		i = endPos + 1
	}
	return ""
}

// extractAttr gets attribute value from a single tag string; tagLow must be lowercase of tag.
func extractAttr(tag, tagLow, name string) string {
	// try with optional whitespace around '=' by scanning for name and then skipping spaces
	start := 0
	for {
		idx := strings.Index(tagLow[start:], name)
		if idx < 0 {
			return ""
		}
		idx += start
		// ensure boundary (prev not letter)
		if idx > 0 {
			prev := tagLow[idx-1]
			if (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9') || prev == '-' || prev == '_' {
				start = idx + len(name)
				continue
			}
		}
		k := idx + len(name)
		// skip spaces
		for k < len(tagLow) && (tagLow[k] == ' ' || tagLow[k] == '\t' || tagLow[k] == '\n') {
			k++
		}
		if k >= len(tagLow) || tagLow[k] != '=' {
			start = idx + len(name)
			continue
		}
		k++ // skip '='
		for k < len(tagLow) && (tagLow[k] == ' ' || tagLow[k] == '\t' || tagLow[k] == '\n') {
			k++
		}
		if k >= len(tag) {
			return ""
		}
		// quote or unquoted
		q := tag[k]
		if q == '\'' || q == '"' {
			k++
			vstart := k
			for k < len(tag) && tag[k] != q {
				k++
			}
			if k <= len(tag) {
				return html.UnescapeString(strings.TrimSpace(tag[vstart:k]))
			}
			return ""
		}
		// unquoted: read until space/>
		vstart := k
		for k < len(tag) && tag[k] != ' ' && tag[k] != '\t' && tag[k] != '\n' && tag[k] != '>' {
			k++
		}
		return html.UnescapeString(strings.TrimSpace(tag[vstart:k]))
	}
}

func isAcceptableImageURL(u string) bool {
	if u == "" {
		return false
	}
	ul := strings.ToLower(strings.TrimSpace(u))
	if strings.HasPrefix(ul, "data:") || strings.HasPrefix(ul, "blob:") {
		return false
	}
	return true
}

// firstMetaImageURL finds <meta property="og:image" content="..."> or
// <meta name="twitter:image" content="..."> in the HTML head/body.
func firstMetaImageURL(html string) string {
	if strings.TrimSpace(html) == "" {
		return ""
	}
	low := strings.ToLower(html)
	i := 0
	for {
		idx := strings.Index(low[i:], "<meta")
		if idx < 0 {
			break
		}
		pos := i + idx
		end := strings.IndexByte(low[pos:], '>')
		if end < 0 {
			break
		}
		endPos := pos + end
		tag := html[pos : endPos+1]
		tagLow := low[pos : endPos+1]
		prop := strings.ToLower(extractAttr(tag, tagLow, "property"))
		name := strings.ToLower(extractAttr(tag, tagLow, "name"))
		if prop == "og:image" || prop == "og:image:url" || name == "twitter:image" || prop == "twitter:image" {
			if c := extractAttr(tag, tagLow, "content"); c != "" {
				if isAcceptableImageURL(c) {
					return c
				}
			}
		}
		i = endPos + 1
	}
	return ""
}

// normalizePublishedDateChina parses a variety of date/time formats and
// returns an ISO-8601 string in UTC+08:00 (China Standard Time).
func normalizePublishedDateChina(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	// Try unix timestamps first
	if isAllDigits(s) {
		if ts, ok := parseUnixTimestamp(s); ok {
			cst := time.FixedZone("CST", 8*3600)
			return ts.In(cst).Format(time.RFC3339), true
		}
	}
	layouts := []string{
		time.RFC3339, time.RFC3339Nano,
		time.RFC1123, time.RFC1123Z,
		"2006-01-02T15:04:05-0700",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05",
		"2006/01/02 15:04:05",
		"Mon Jan 2 15:04:05 MST 2006",
		"02 Jan 2006 15:04:05 -0700",
		"02 Jan 2006 15:04:05",
		"2006-01-02",
		"2006/01/02",
	}
	cst := time.FixedZone("CST", 8*3600)
	// Try with exact layouts (with timezone if present)
	for _, layout := range layouts {
		// If layout includes zone offset or name, use Parse; otherwise use ParseInLocation (assume CST)
		var t time.Time
		var err error
		if strings.Contains(layout, "-0700") || strings.Contains(layout, "MST") || layout == time.RFC3339 || layout == time.RFC3339Nano || layout == time.RFC1123 || layout == time.RFC1123Z {
			t, err = time.Parse(layout, s)
		} else {
			t, err = time.ParseInLocation(layout, s, cst)
		}
		if err == nil {
			return t.In(cst).Format(time.RFC3339), true
		}
	}
	return "", false
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parseUnixTimestamp(s string) (time.Time, bool) {
	// seconds (10) or milliseconds (13) or micro/nano variants
	// We use length heuristic; fallback to parsing as seconds
	var (
		n  int64
		ok bool
	)
	if len(s) >= 10 && len(s) <= 19 {
		// avoid strconv import; parse manually
		var v int64
		for _, r := range s {
			v = v*10 + int64(r-'0')
		}
		n = v
		ok = true
	}
	if !ok {
		return time.Time{}, false
	}
	switch {
	case len(s) >= 19: // nanoseconds
		return time.Unix(0, n), true
	case len(s) >= 16: // microseconds
		return time.Unix(0, n*1_000), true
	case len(s) >= 13: // milliseconds
		return time.UnixMilli(n), true
	default: // seconds
		return time.Unix(n, 0), true
	}
}

// tryReadCache returns cached HTML if found.
func (p *Processor) tryReadCache(ctx context.Context, url string) (string, bool) {
	// Prefer Redis if configured
	if p.rc != nil {
		key := p.rc.Key("html", hashURL(url))
		if v, ok, err := p.rc.GetString(ctx, key); err == nil && ok {
			return v, true
		}
		// Redis enabled: do not fallback to local file cache
		return "", false
	}
	// Fallback to file cache (if enabled)
	path := p.cacheFilePath(url)
	if path == "" {
		return "", false
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return "", false
	}
	return string(b), true
}

// writeCache saves HTML to cache file (best-effort).
func (p *Processor) writeCache(ctx context.Context, url, html string) error {
	// When Redis is configured, use Redis only and do not write local files
	if p.rc != nil {
		key := p.rc.Key("html", hashURL(url))
		if err := p.rc.SetString(ctx, key, html); err != nil {
			log.Printf("[reader] redis set html error: %v", err)
		} else {
			log.Printf("[reader] redis set html ok key=%s bytes=%d ttl=%ds", key, len(html), p.cfg.RedisTTLSeconds)
		}
		return nil
	}
	// Local file cache (when Redis not in use)
	path := p.cacheFilePath(url)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(html), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (p *Processor) cacheFilePath(url string) string {
	dir := strings.TrimSpace(p.cfg.ReaderCacheDir)
	if dir == "" {
		return ""
	}
	h := hashURL(url)
	return filepath.Join(dir, h+".html")
}

func (p *Processor) cacheMetaFilePath(url string) string {
	dir := strings.TrimSpace(p.cfg.ReaderCacheDir)
	if dir == "" {
		return ""
	}
	h := hashURL(url)
	return filepath.Join(dir, h+".body.json")
}

func hashURL(s string) string {
	// Use SHA-256 hex of the URL for filename stability
	sum := sha256Sum([]byte(s))
	return sum
}

// sha256Sum returns hex-encoded SHA-256 of data.
func sha256Sum(b []byte) string {
	h := sha256.New()
	_, _ = h.Write(b)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// tryReadMetaCache loads a previously computed Readwise body JSON if present
func (p *Processor) tryReadMetaCache(ctx context.Context, url string) (map[string]any, bool) {
	// Prefer Redis if configured
	if p.rc != nil {
		key := p.rc.Key("body", hashURL(url))
		if s, ok, err := p.rc.GetString(ctx, key); err == nil && ok && s != "" {
			var v map[string]any
			if json.Unmarshal([]byte(s), &v) == nil {
				return v, true
			}
		}
		// Redis enabled: do not fallback to local file cache
		return nil, false
	}
	// Fallback to file
	path := p.cacheMetaFilePath(url)
	if path == "" {
		return nil, false
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return nil, false
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, false
	}
	return v, true
}

func (p *Processor) writeMetaCache(ctx context.Context, url string, body map[string]any) error {
	// Marshal once for Redis and/or file
	b, _ := json.MarshalIndent(body, "", "  ")
	if p.rc != nil {
		key := p.rc.Key("body", hashURL(url))
		if err := p.rc.SetString(ctx, key, string(b)); err != nil {
			log.Printf("[reader] redis set body error: %v", err)
		} else {
			log.Printf("[reader] redis set body ok key=%s bytes=%d ttl=%ds", key, len(b), p.cfg.RedisTTLSeconds)
		}
		// Do not write local files when Redis is enabled
		return nil
	}
	// File (when Redis not in use)
	path := p.cacheMetaFilePath(url)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// buildReadwiseBody converts extracted meta into a strictly-typed Reader API body
func buildReadwiseBody(meta map[string]any) map[string]any {
	body := make(map[string]any)
	if s, ok := toString(meta["url"]); ok {
		body["url"] = s
	}
	body["should_clean_html"] = toBool(meta["should_clean_html"], true)
	if s, ok := toString(meta["title"]); ok {
		body["title"] = s
	}
	if s, ok := toString(meta["author"]); ok {
		body["author"] = s
	}
	if s, ok := toString(meta["published_date"]); ok {
		if iso, ok2 := normalizePublishedDateChina(s); ok2 {
			body["published_date"] = iso
		}
	}
	if s, ok := toString(meta["image_url"]); ok {
		body["image_url"] = s
	}
	if s, ok := toString(meta["summary"]); ok {
		body["summary"] = s
	}
	if s, ok := toString(meta["category"]); ok {
		body["category"] = s
	}
	if tags := toStringSlice(meta["tags"]); len(tags) > 0 {
		body["tags"] = tags
	}
	if s, ok := toNotesString(meta["notes"]); ok {
		body["notes"] = s
	}
	if s, ok := toString(meta["location"]); ok {
		switch strings.ToLower(s) {
		case "new", "later", "archive", "feed":
			body["location"] = strings.ToLower(s)
		}
	}
	if s, ok := toString(meta["saved_using"]); ok {
		body["saved_using"] = s
	}
	if _, ok := body["location"]; !ok {
		body["location"] = "new"
	}
	if _, ok := body["saved_using"]; !ok {
		body["saved_using"] = "web_extractor"
	}
	if _, ok := body["category"]; !ok {
		body["category"] = "article"
	}
	return body
}
