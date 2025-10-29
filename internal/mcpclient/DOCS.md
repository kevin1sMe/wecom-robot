返回概览

  - 工具 scrape_wechat_article 的 MCP 返回为：content[{ type: 'text', text: <JSON字符串> }]
  - text 内部是本文档所述的“文章抓取结果 JSON”
  - 成功时不带 isError；异常时返回 isError: true 且 text 为错误信息
  - 同时将相同的 JSON 写入文件 wechat_article_<timestamp>.json（工作目录）

  成功响应（MCP 外层）

  - content: [ { type: 'text', text: <抓取结果JSON字符串> } ]

  示例（外层结构，省略 JSON 字符串内容）:

  - {"content":[{"type":"text","text":"<见下方内部JSON>"}]}

  成功响应（内部 JSON 载荷）示例

  - 以下为 text 字段内的 JSON 内容：
  - {\n  "status": "success",\n  "url": "https://mp.weixin.qq.com/s/xxxxx",\n  "timestamp": "2025-10-29T08:12:34.567Z",\n  "metadata": {\n    "title": "文章标题",\n    "author": "作者名",\n
    "published_date": "2025-09-30T04:10:00.000Z",\n    "saved_using": "wechat-scraper-mcp"\n  },\n  "markdown": "# 标题\\n\\n正文 Markdown...",\n  "html": "<div>正文 HTML...</div>"\n}

  字段说明（内部 JSON 载荷）

  - status string
      - 固定为 success
  - url string
      - 抓取目标 URL
  - timestamp string
      - ISO 8601 时间（抓取完成时间）
  - metadata object
      - title string：文章标题（可能为空字符串）
      - author string：作者名（可能为空字符串）
      - published_date string：ISO 8601 发布时间（解析失败时为空字符串）
      - saved_using string：固定为 wechat-scraper-mcp
  - markdown string 可选
      - 当 formats 包含 markdown 时返回
  - html string 可选
      - 当 formats 包含 html 时返回

  错误响应（MCP 外层）

  - 形态：{ content: [{ type: 'text', text: '错误信息' }], isError: true }
  - 示例：{"content":[{"type":"text","text":"抓取异常: <错误详情>"}],"isError":true}

  以下为真实场景的返回示例：
  ```json
  {
  "status": "success",
  "url": "https://mp.weixin.qq.com/s/l75In6mmhGZEqaRvUYVYgA",
  "timestamp": "2025-10-29T08:06:31.316Z",
  "metadata": {
    "title": "海外刷屏了，竟然是个中国的大模型，有点DeepSeek那意思了",
    "author": "路人甲TM",
    "published_date": "2025-10-28T16:30:00.000Z",
    "saved_using": "wechat-scraper-mcp"
  },
  "html": "<p> test </p>"
}
```