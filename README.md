# WeCom（企业微信）回调服务（Go）

最小可用的 HTTP 服务：接收企业微信加密回调、验签、解密、打印原始 XML，并按标准回包：
- 非事件消息：被动加密回复文本“OK”（Encrypt/MsgSignature/TimeStamp/Nonce）。
- 事件消息：返回明文 `success`（不回消息）。

## 目录结构

```
.
├── cmd/
│   └── wecom-robot/
│       └── main.go          # 程序入口
├── internal/
│   ├── config/
│   │   └── config.go        # 环境变量加载
│   ├── server/
│   │   └── handlers.go      # HTTP 路由与处理
│   └── wecom/
│       └── wxbizmsgcrypt.go # 官方加解密实现（已内置）
├── go.mod
└── README.md
```

## 配置

设置以下环境变量：

- `WECOM_TOKEN`：在企业微信后台配置的 Token。
- `WECOM_ENCODING_AES_KEY`：43 位 EncodingAESKey。
- `WECOM_RECEIVE_ID`：接收方标识。企业自建应用为企业 ID（CorpID，ww 开头）；第三方套件为 SuiteID。
- `PORT`（可选）：默认 `8080`。

## 运行

```sh
export WECOM_TOKEN=your_token
export WECOM_ENCODING_AES_KEY=your_43_char_key
export WECOM_RECEIVE_ID=your_corp_id_or_suite_id

# 使用本地缓存目录（适配沙箱写权限）
GOCACHE=$(pwd)/.gocache go run ./cmd/wecom-robot
```

服务默认监听 `:8080`（或 `:$PORT`）。

## Docker

本仓库提供 Docker 镜像构建：

- 本地构建（生成 `wecom-robot:local`）：

  ```sh
  ./build/local-build.sh
  docker run --rm -p 8080:8080 \
    -e WECOM_TOKEN=your_token \
    -e WECOM_ENCODING_AES_KEY=your_43_char_key \
    -e WECOM_RECEIVE_ID=your_corp_id_or_suite_id \
    wecom-robot:local
  ```

- GitHub Actions 会在 push/pr 触发，构建并推送镜像到腾讯云 TCR（需在环境 secrets 配置 `TCR_REGISTRY`、`TCR_NAMESPACE`、`TCR_REPOSITORY`、`TCR_USERNAME`、`TCR_PASSWORD`）。

## 回调 URL 配置与接口

企业微信后台“回调 URL”推荐配置为根路径：
- 根路径：`https://<your-domain>/`（本项目仅支持根路径）

同一个回调 URL 同时承担两种行为：
- `GET` 验证 URL：企业微信会携带 `msg_signature`、`timestamp`、`nonce`、`echostr` 参数发起请求，服务端需要验签并解密 `echostr` 原样返回。
- `POST` 接收消息：企业微信会携带 `msg_signature`、`timestamp`、`nonce` 参数，并在 Body 的 XML 里包含 `<Encrypt>` 字段。

- `GET /`：URL 校验。参数 `msg_signature`、`timestamp`、`nonce`、`echostr`。
  - 验签并解密 `echostr`，原样返回解密后的 echo 字符串。
- `POST /`：消息接收。参数 `msg_signature`、`timestamp`、`nonce`；Body 为包含 `<Encrypt>` 的 XML。
  - 验签并解密消息；日志打印解密后的原始 XML。
  - 若为非事件消息：被动加密回复文本“OK”（标准回包 XML）。
  - 若为事件消息：返回明文 `success`。

示例 POST 结构：

```xml
<xml>
  <ToUserName><![CDATA[wxCorpId]]></ToUserName>
  <AgentID><![CDATA[1000002]]></AgentID>
  <Encrypt><![CDATA[base64_ciphertext]]></Encrypt>
</xml>
```

注意：`msg_signature`、`timestamp`、`nonce`、`echostr` 作为查询参数传递；Body 只需 `<Encrypt>` 字段即可。

健康检查：`GET /` 无参数时返回 `ok`。

被动回复说明：
- 需要正确设置 `WECOM_RECEIVE_ID`（企业ID或SuiteID），否则无法进行加密回包（服务会兜底返回明文 `success`）。
- 回包超时会导致企业微信重试，请确保处理耗时 < 5 秒。

## 说明

- 使用企业微信官方 Go 实现（`wxbizmsgcrypt.go`）进行验签、解密与回包加密。
- 元数据提取使用官方 OpenAI Go SDK（`github.com/openai/openai-go`），支持自定义 `LLM_BASE_URL`。
- 加密：AES-256-CBC + PKCS#7，IV 为 AESKey 的前 16 字节（`EncodingAESKey` 解出 32 字节）。
- 签名：对 `[token, timestamp, nonce, encrypted]` 字典序排序后拼接，做 SHA1。
- 明文结构：`16B 随机 | 4B 大端长度 | XML 消息 | receiveid`。服务会在提供 `WECOM_RECEIVE_ID` 时进行校验。

## 链接到 Readwise（Go 内置集成）

当接收到文本消息且内容包含 `http://` 或 `https://` 链接时，服务会在后台执行“抓取 → 提取 → 保存”流水线：

- 抓取：通过 MCP `streamablehttp`（或兼容）服务器获取原始 HTML（不进行直接 HTTP 抓取）
- 提取：调用 OpenAI 兼容 Chat Completions 模型，输出严格 JSON 的元数据
- 保存：调用 Readwise Reader API `/api/v3/save/`

主回包不受影响（事件：明文 `success`；文本：加密回复 `OK`），流水线异步执行，避免企业微信重试。

必需/可选环境变量：
- WeCom：`WECOM_TOKEN`、`WECOM_ENCODING_AES_KEY`、`WECOM_RECEIVE_ID`、`PORT`（可选）
- LLM：`LLM_BASE_URL`（例如官方为 `https://api.openai.com/v1`）、`LLM_API_KEY`、`LLM_MODEL`（也兼容 `EXAMPLE_BASE_URL`、`EXAMPLE_API_KEY`、`EXAMPLE_MODEL_NAME`）
- LLM 可选参数：`LLM_TEMPERATURE`（不设置则不传该参数，使用模型默认值；如某些 Azure 模型组仅支持默认温度，建议留空或设为 `1`）
- Readwise：`READWISE_API_TOKEN`
- MCP（必需）：
  - `MCP_HTTP_URL`（你的 MCP HTTP 端点，例如 `http://localhost:8080/mcp`）
  - `MCP_TOOL_NAME`（默认 `http`）
- 缓存（可选）：
  - `READER_CACHE_DIR`（抓取结果本地缓存目录，默认 `.reader-cache`）
 - 追踪日志（可选）：
   - `READER_LOG_DIR`（每次处理的上下文落盘目录，默认 `.reader-logs`）

快速运行示例：

```sh
export WECOM_TOKEN=your_token
export WECOM_ENCODING_AES_KEY=your_43_char_key
export WECOM_RECEIVE_ID=your_corp_id_or_suite_id

export LLM_BASE_URL=https://api.openai.com/v1  # 或你的 OpenAI 兼容端点
export LLM_API_KEY=sk-xxx
export LLM_MODEL=gpt-4o-mini
# 可选：如使用 Azure 某些模型组（如 azure/gpt-5-mini）且其仅支持默认温度，建议不设置该变量；
# 若确需设置请用 1
# export LLM_TEMPERATURE=1
export READWISE_API_TOKEN=rw_XXX

# MCP streamable HTTP（必需）
export MCP_HTTP_URL=http://localhost:8080/mcp

GOCACHE=$(pwd)/.gocache go run ./cmd/wecom-robot

### 本地测试入口（非 WeCom）

- POST `./url` 表单：`url=https://example.com`
- 行为：与企业微信文本消息中的链接处理一致，后台执行“抓取 → 提取 → 保存”，接口立即返回 `queued`
- 缓存：若设置 `READER_CACHE_DIR`（默认 `.reader-cache`），会按 URL 的 SHA-256 计算文件名并缓存 HTML，例如：`.reader-cache/<hash>.html`。再次请求相同 URL 将命中缓存并跳过抓取。
- 追踪日志：若设置 `READER_LOG_DIR`（默认 `.reader-logs`），每次请求会在该目录下创建 `<hash>-<timestamp>/`，包含：
  - `url.txt`、`fetch_source.txt`（cache/mcp）、`fetch.html`
  - `extract_prompt_system.txt`、`extract_prompt_user.txt`、`llm_raw_response.txt`、`extracted.json`
  - `readwise_request.json`、`readwise_response_<status>.txt`、遇错时 `error.txt`
  - 调试日志（step-based）：控制台输出包含 `job=<id> step=<name> event=<start|end|error>` 字段，便于串联异步流程；可按 `job=<id>` 过滤（URL 的 SHA-256 前 8 位）。
    - 示例：
      - `[reader] job=ab12cd34 step=process event=start url=https://...`
      - `[reader] job=ab12cd34 step=fetch_html event=start`
      - `[reader] job=ab12cd34 step=fetch_html event=end from_cache=false bytes=123456 dur=1.23s`
      - `[reader] job=ab12cd34 step=extract_meta event=end from_cache=false keys=10 bytes=2048 dur=2.01s`
      - `[reader] job=ab12cd34 step=save_readwise event=end dur=350ms`
      - `[reader] job=ab12cd34 step=process event=end url=https://... dur=3.85s`
- 示例：

```
curl -X POST http://localhost:8080/url \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  -d 'url=https://mp.weixin.qq.com/s/R5t8xJW1CnjJjZoeOgX_Rg'
```
```
