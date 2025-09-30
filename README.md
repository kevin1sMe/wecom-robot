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
│       └── crypto.go        # WeCom 加解密与验签
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

企业微信后台“回调 URL”可以配置为：
- 根路径：`https://<your-domain>/`（本项目已支持）
- 指定路径：`https://<your-domain>/callback`

同一个回调 URL 同时承担两种行为：
- `GET` 验证 URL：企业微信会携带 `msg_signature`、`timestamp`、`nonce`、`echostr` 参数发起请求，服务端需要验签并解密 `echostr` 原样返回。
- `POST` 接收消息：企业微信会携带 `msg_signature`、`timestamp`、`nonce` 参数，并在 Body 的 XML 里包含 `<Encrypt>` 字段。

- `GET /callback` 或 `GET /`：URL 校验。参数 `msg_signature`、`timestamp`、`nonce`、`echostr`。
  - 验签并解密 `echostr`，原样返回解密后的 echo 字符串。
- `POST /callback` 或 `POST /`：消息接收。参数 `msg_signature`、`timestamp`、`nonce`；Body 为包含 `<Encrypt>` 的 XML。
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

- 加密：AES-256-CBC + PKCS#7，IV 为 AESKey 的前 16 字节（`EncodingAESKey` 解出 32 字节）。
- 签名：对 `[token, timestamp, nonce, encrypted]` 字典序排序后拼接，做 SHA1。
- 明文结构：`16B 随机 | 4B 大端长度 | XML 消息 | receiveid`。服务会在提供 `WECOM_RECEIVE_ID` 时进行校验。
