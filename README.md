# 公众号文章采集到Readwise Reader

## Overview
该项目实现了企业微信回调服务：完成 URL 验证、消息验签解密，并按照协议进行被动回复。当收到的文本消息以 `http://` 或 `https://` 开头时，服务会在后台触发“抓取 → 提取 → 保存”的流水线，从外部 MCP streamable HTTP 服务获取网页内容，调用 OpenAI 兼容模型提取元信息，最终保存到 Readwise Reader。

其它网页的内容，也可以采集到Readwise Reader。

## Features
- 使用企业微信官方 `wxbizmsgcrypt` 代码处理 URL 验证、消息验签和 AES-256-CBC 解密。
- `POST /` 会根据消息类型自动回包：事件消息返回明文 `success`，文本消息加密回复 `OK`。
- 文本消息携带 URL 时异步执行流水线，不阻塞企业微信回调，避免重复重试。
- 通过 MCP HTTP 工具（默认 `scrape_wechat_article`）统一抓取网页，支持 Bearer 鉴权。
- 采集到的 HTML 交由 LLM（OpenAI 兼容接口）提取结构化 JSON，再写入 Readwise Reader。
- 提供文件系统或 Redis 缓存，加速重复抓取；可选追踪目录记录流水线每一步的上下文。
- `POST /url` 表单接口用于本地测试同样的流水线，无需企业微信环境。

## Quick Start
```sh
# 必需：企业微信回调配置
export WECOM_TOKEN=your_token
export WECOM_ENCODING_AES_KEY=your_43_char_key
export WECOM_RECEIVE_ID=your_corp_id_or_suite_id

# 流水线依赖：MCP + LLM + Readwise（按需启用）
export MCP_HTTP_URL=https://your-mcp-endpoint.example/mcp
export MCP_TOOL_NAME=scrape_wechat_article   # 可选，默认已设
export LLM_BASE_URL=https://api.openai.com/v1
export LLM_API_KEY=sk-your_key
export LLM_MODEL=gpt-4o-mini
export READWISE_API_TOKEN=rw_your_token

# 在受限环境建议自定义 Go 缓存目录
GOCACHE=$(pwd)/.gocache go run ./cmd/wecom-robot
```
服务默认监听 `:8080`，可通过设置 `PORT` 环境变量调整。

## Configuration
### 必需（WeCom 回调）
- `WECOM_TOKEN` – 企业微信后台配置的 Token。
- `WECOM_ENCODING_AES_KEY` – 43 位 EncodingAESKey。
- `WECOM_RECEIVE_ID` – 企业 ID（CorpID，`ww` 开头）或第三方套件 SuiteID。
- `PORT` – 可选，默认 `8080`。

### 流水线依赖
- `MCP_HTTP_URL` – MCP streamable HTTP 端点；流水线必须配置。
- `MCP_TOOL_NAME` – MCP 工具名称，默认 `scrape_wechat_article`。
- `MCP_AUTH_TOKEN` – 可选 Bearer Token。
- `LLM_BASE_URL`、`LLM_API_KEY`、`LLM_MODEL` – OpenAI 兼容接口参数，可使用 `EXAMPLE_*` 变量兜底。
- `LLM_TEMPERATURE` – 可选，解析为 `float64` 后传给模型；留空则使用默认温度。
- `READWISE_API_TOKEN` – Readwise Reader API Key。

### 缓存与调试
- `READER_CACHE_DIR` – 本地 HTML 缓存目录，默认 `.reader-cache`（Redis 启用时自动禁用）。
- `READER_LOG_DIR` – 每次任务的追踪目录，默认 `.reader-logs`。
- `REDIS_ADDR` – 启用 Redis 缓存（示例：`127.0.0.1:6379` 或 `redis://user:pass@host:6379/0`）。
- `REDIS_PREFIX` – Redis 键前缀，默认 `wecom-robot`。
- `REDIS_TTL_SECONDS` – Redis 缓存过期时间，默认 86400 秒。

## HTTP Endpoints
- `GET /`（无参数）– 健康检查，返回 `ok`。
- `GET /`（带 `msg_signature`、`timestamp`、`nonce`、`echostr`）– 企业微信 URL 验证，解密后原样返回。
- `POST /`（带签名参数）– 企业微信加密消息入口。
  - 文本消息并且内容以 `http/https` 开头：异步启动流水线，主线程加密回复 `OK`。
  - 其余消息：直接返回明文 `success`。
- `POST /url` – 表单字段 `url`（必须以 `http/https` 开头），用于本地测试，立即返回 `queued`。

## Link Processing Pipeline
1. **Fetch** – 通过 MCP HTTP 工具获取指定 URL 的 HTML 与元数据，可选文件/Redis 缓存。
2. **Extract** – 将 HTML 输入 OpenAI 兼容模型，按 `docs/网页内容提取提示语.md` 的提示语生成结构化 JSON。
3. **Save** – 将 JSON + HTML 组合成 Readwise Reader 文档并调用 `/api/v3/save/` 写入。

每个步骤都会以 `job=<hash> step=<stage>` 形式输出日志；若配置 `READER_LOG_DIR`，会在对应目录中记录请求、提示语、响应与错误详情。

## Development
```sh
go fmt ./...
go vet ./...
go test ./... -cover
go build ./...
```
Go 1.23+ 环境下开发测试即可。发布前请确保清理本地 `.env`、`.reader-cache/`、`.reader-logs/` 等包含敏感信息的目录。
