#!/usr/bin/env python3
"""
Readwise Reader Agent - 一键保存网页到 Readwise Reader
整合网页抓取、内容提取和保存功能的统一脚本
"""

import asyncio
import json
import os
import shutil
import sys
import time
from datetime import datetime, timezone
from typing import Optional, Dict, Any

import requests
from agents import (
    Agent,
    Runner,
    OpenAIChatCompletionsModel,
    trace,
    set_tracing_disabled,
)
from agents.model_settings import ModelSettings
from agents.mcp import MCPServer, MCPServerStdio
from openai import AsyncOpenAI

# 禁用 tracing
set_tracing_disabled(disabled=True)


class ReadwiseAgent:
    """Readwise Reader 智能代理"""

    def __init__(self):
        self.readwise_token = None
        self.firecrawl_key = None
        self.log_dir = None  # 当前会话的日志目录
        self.setup_environment()

    def setup_environment(self):
        """设置环境变量"""
        # 从环境变量获取配置
        self.readwise_token = os.getenv('READWISE_API_TOKEN')
        self.firecrawl_key = os.getenv('FIRECRAWL_API_KEY')

        # LLM 配置
        self.base_url = os.getenv("EXAMPLE_BASE_URL") or ""
        self.api_key = os.getenv("EXAMPLE_API_KEY") or ""
        self.model_name = os.getenv("EXAMPLE_MODEL_NAME") or ""

        if not self.base_url or not self.api_key or not self.model_name:
            raise ValueError("请设置 EXAMPLE_BASE_URL, EXAMPLE_API_KEY, EXAMPLE_MODEL_NAME 环境变量")

    async def create_http_mcp_server(self) -> MCPServerStdio:
        """创建可配置的 HTTP 抓取 MCP 服务器实例（默认 streamablehttp）"""
        # 允许通过环境变量自定义命令与参数
        # 例如：
        #   MCP_SERVER_COMMAND=mcp
        #   MCP_SERVER_ARGS=streamablehttp
        # 或者：
        #   MCP_SERVER_COMMAND=npx
        #   MCP_SERVER_ARGS=-y @modelcontextprotocol/servers/streamable-http
        mcp_cmd = os.getenv("MCP_SERVER_COMMAND") or "streamablehttp"
        mcp_args = os.getenv("MCP_SERVER_ARGS") or ""

        # 可选：传入 Firecrawl key 或其它 server 需要的 env
        env = {}
        if self.firecrawl_key:
            env["FIRECRAWL_API_KEY"] = self.firecrawl_key

        args_list = [a for a in mcp_args.split(" ") if a]

        return MCPServerStdio(
            cache_tools_list=True,
            params={
                "command": mcp_cmd,
                "args": args_list,
                "env": env,
            },
            client_session_timeout_seconds=60,
        )

    def create_log_directory(self):
        """为当前会话创建日志目录"""
        timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
        self.log_dir = f"logs_{timestamp}"
        try:
            os.makedirs(self.log_dir, exist_ok=True)
            print(f"📁 创建日志目录: {self.log_dir}")
        except Exception as e:
            print(f"⚠️ 创建日志目录失败: {e}")
            self.log_dir = "."  # 回退到当前目录

    def save_to_file(self, filename: str, content: str, description: str = ""):
        """保存内容到文件"""
        if self.log_dir:
            filepath = os.path.join(self.log_dir, filename)
        else:
            filepath = filename

        try:
            with open(filepath, 'w', encoding='utf-8') as f:
                f.write(content)
            print(f"  💾 {description}已保存到: {filename}")
        except Exception as e:
            print(f"  ⚠️ 保存文件失败 {filename}: {e}")

    async def fetch_content(self, url: str) -> str:
        """抓取网页原始 HTML 内容（通过 MCP streamablehttp 或兼容服务）"""
        start_time = time.time()
        print(f"🔍 正在抓取网页: {url}")

        # 检查必要的依赖
        if not shutil.which("npx"):
            raise RuntimeError("npx 未安装。请安装 Node.js 和 npm。")

        # 创建 OpenAI 兼容的客户端
        client = AsyncOpenAI(
            base_url=self.base_url,
            api_key=self.api_key,
            timeout=60.0,
            max_retries=3
        )

        async with await self.create_http_mcp_server() as server:
            agent = Agent(
                name="WebContentFetcher",
                model=OpenAIChatCompletionsModel(model=self.model_name, openai_client=client),
                instructions=(
                    "你是一个网页抓取专家。必须使用提供的 MCP HTTP 工具来获取页面原始内容。"
                    "返回结果必须是原始 HTML 文本（不使用 markdown 代码块，不添加额外解释）。"
                ),
                mcp_servers=[server],
                model_settings=ModelSettings(tool_choice="auto"),
            )

            message = f"请用 MCP HTTP 工具抓取该页面并返回原始 HTML：{url}"

            # 增加重试机制
            max_retries = 3
            for attempt in range(max_retries):
                try:
                    print(f"  尝试第 {attempt + 1} 次...")
                    result = await Runner.run(starting_agent=agent, input=message)
                    content = result.final_output

                    elapsed_time = time.time() - start_time
                    print(f"  ✅ 内容抓取成功 ({len(content)} 字符) - 用时: {elapsed_time:.2f}s")

                    # 保存抓取的原始内容
                    self.save_to_file("01_fetched_content.html", content, "抓取的原始HTML内容")

                    return content
                except Exception as e:
                    print(f"  ❌ 第 {attempt + 1} 次尝试失败: {e}")
                    if attempt == max_retries - 1:
                        raise
                    print(f"  ⏳ 等待 {(attempt + 1) * 2} 秒后重试...")
                    await asyncio.sleep((attempt + 1) * 2)

    async def extract_metadata(self, html_content: str, url: str) -> Dict[str, Any]:
        """从HTML内容中提取结构化元数据"""
        start_time = time.time()
        print("📋 正在提取结构化信息...")

        fetch_time = datetime.now(timezone.utc).isoformat()

        # 创建 OpenAI 兼容的客户端
        client = AsyncOpenAI(base_url=self.base_url, api_key=self.api_key)

        # 提取抓取时间的年份
        fetch_datetime = datetime.fromisoformat(fetch_time.replace('Z', '+00:00'))
        current_year = fetch_datetime.year

        # 构建agent指令
        instructions = f"""
你是一名严谨的网页内容解析器。请从给定 HTML 中提取文章字段，并输出**唯一一个** JSON 对象。除 JSON 外不要返回任何多余文字。

【任务目标】
- 从HTML提取适合Readwise Reader的字段，生成干净的article HTML与结构化元信息
- 不臆造：未知则置为null或空数组
- 保留内容语义与层级

【重要时间信息】
- 抓取时间：{fetch_time}
- 当前年份：{current_year}年

【输入】
- 页面URL：{url}
- HTML内容：
{html_content}

【输出JSON字段定义】
{{
  "url": string,
  "html": string|null,
  "should_clean_html": boolean,
  "title": string|null,
  "author": string|null,
  "published_date": string|null,
  "image_url": string|null,
  "summary": string|null,
  "category": "article"|"email"|"rss"|"highlight"|"note"|"pdf"|"epub"|"tweet"|"video"|null,
  "tags": string[],
  "notes": string|null,
  "location": "new"|"later"|"archive"|"feed"|null,
  "saved_using": string|null
}}

【提取规则】
1. url：使用输入的URL
2. html：提取完整的文章HTML内容，保持语义结构
3. should_clean_html：设为true，让API自动清理HTML
4. title：优先级：og:title > title标签 > h1标签 > 从内容推断
5. author：优先级：og:article:author > meta[name="author"] > byline文本 > 作者署名

6. published_date：**重要**时间提取规则：
   a) 优先查找meta标签中的时间：
      - meta[property="article:published_time"]
      - meta[name="publishdate"]
      - meta[name="date"]
   b) 在页面文本中查找日期格式：
      - "YYYY年MM月DD日" 或 "YYYY-MM-DD"
      - "MM月DD日HH:MM" 或 "M月D日H:M"
      - "昨天"、"今天"、"前天"等相对时间
   c) **年份处理规则**：
      - 如果找到完整日期（包含年份），直接使用
      - 如果只有月日没有年份，**必须**使用当前年份：{current_year}年
      - 如果是相对时间，根据抓取时间计算具体日期
   d) 时间格式输出：严格按照ISO8601格式（YYYY-MM-DDTHH:MM:SS+00:00）
   e) 如果完全没有时间信息，设为null

7. image_url：优先级：og:image > twitter:image > 第一张内容图片（必须是绝对URL）
8. summary：基于文章内容生成客观概述，2-4句话，突出核心信息
9. category：根据内容类型智能判断，通常为"article"
10. tags：从标题和内容中提取3-8个相关主题标签，用数组格式
11. location：固定设为"new"
12. saved_using：固定设为"web_extractor"

【重要提醒】
- published_date字段：当遇到"X月Y日Z:Z"格式时，年份必须使用{current_year}年
- 所有URL必须转换为绝对URL格式
- 时间格式必须严格遵循ISO8601标准
- 不要臆造不存在的信息，不确定时设为null

【输出要求】
- 返回合法JSON对象，符合Readwise Reader API规范
- published_date必须是完整的ISO8601格式字符串或null
- should_clean_html设为true
- location设为"new"
- saved_using设为"web_extractor"
"""

        agent = Agent(
            name="MetadataExtractor",
            model=OpenAIChatCompletionsModel(model=self.model_name, openai_client=client),
            instructions=instructions
        )

        message = "请根据上述HTML内容提取出我需要的结构化信息"
        result = await Runner.run(starting_agent=agent, input=message)

        # 保存LLM的原始响应
        self.save_to_file("02_llm_response.txt", result.final_output, "LLM原始响应")

        # 清理并解析JSON响应
        try:
            # 清理markdown代码块标记
            json_content = result.final_output.strip()
            if json_content.startswith('```json'):
                # 移除开头的```json
                json_content = json_content[7:]
            if json_content.startswith('```'):
                # 移除开头的```
                json_content = json_content[3:]
            if json_content.endswith('```'):
                # 移除结尾的```
                json_content = json_content[:-3]

            # 去除首尾空白字符
            json_content = json_content.strip()

            metadata = json.loads(json_content)
            elapsed_time = time.time() - start_time
            print(f"  ✅ 信息提取成功: {metadata.get('title', 'Unknown Title')} - 用时: {elapsed_time:.2f}s")

            # 保存解析后的结构化数据
            formatted_json = json.dumps(metadata, ensure_ascii=False, indent=2)
            self.save_to_file("03_extracted_metadata.json", formatted_json, "提取的结构化元数据")

            return metadata
        except json.JSONDecodeError as e:
            elapsed_time = time.time() - start_time
            print(f"  ❌ JSON解析失败: {e} - 用时: {elapsed_time:.2f}s")
            print(f"  原始响应: {result.final_output}")
            print(f"  清理后内容: {json_content if 'json_content' in locals() else 'N/A'}")
            raise

    def validate_readwise_token(self) -> bool:
        """验证Readwise API令牌是否有效"""
        if not self.readwise_token:
            return False

        try:
            response = requests.get(
                "https://readwise.io/api/v2/auth/",
                headers={"Authorization": f"Token {self.readwise_token}"}
            )
            return response.status_code == 204
        except Exception:
            return False

    def upload_to_readwise(self, metadata: Dict[str, Any]) -> Optional[Dict[str, Any]]:
        """上传文档到Readwise Reader"""
        start_time = time.time()
        print("📤 正在上传到 Readwise Reader...")

        if not self.readwise_token:
            self.readwise_token = input("请输入 Readwise API Token: ")

        # 验证令牌
        if not self.validate_readwise_token():
            elapsed_time = time.time() - start_time
            print(f"❌ Readwise API令牌无效 - 用时: {elapsed_time:.2f}s")
            return None

        try:
            # 构建API请求数据，移除不需要的字段
            api_data = {k: v for k, v in metadata.items() if v is not None and k != "notes"}

            # 添加notes字段（如果存在且不为空）
            if metadata.get("notes"):
                api_data["notes"] = metadata["notes"]

            headers = {
                "Authorization": f"Token {self.readwise_token}",
                "Content-Type": "application/json"
            }

            response = requests.post(
                "https://readwise.io/api/v3/save/",
                headers=headers,
                json=api_data,
                timeout=30
            )

            # 保存发送到Readwise的数据
            formatted_request = json.dumps(api_data, ensure_ascii=False, indent=2)
            self.save_to_file("04_readwise_request.json", formatted_request, "发送到Readwise的请求数据")

            elapsed_time = time.time() - start_time

            if response.status_code in [200, 201]:
                result = response.json()
                print(f"  ✅ 上传成功! - 用时: {elapsed_time:.2f}s")
                print(f"  📄 文档ID: {result.get('id')}")
                print(f"  🔗 阅读链接: {result.get('url')}")

                # 保存Readwise的响应
                formatted_response = json.dumps(result, ensure_ascii=False, indent=2)
                self.save_to_file("05_readwise_response.json", formatted_response, "Readwise响应结果")

                return result
            else:
                print(f"  ❌ 上传失败! 状态码: {response.status_code} - 用时: {elapsed_time:.2f}s")
                print(f"  错误信息: {response.text}")

                # 保存错误响应
                error_content = f"Status Code: {response.status_code}\nResponse: {response.text}"
                self.save_to_file("05_readwise_error.txt", error_content, "Readwise错误响应")

                return None

        except Exception as e:
            elapsed_time = time.time() - start_time
            print(f"  ❌ 上传过程中发生错误: {e} - 用时: {elapsed_time:.2f}s")
            return None

    async def process_url(self, url: str) -> bool:
        """处理单个URL：抓取->提取->上传"""
        total_start_time = time.time()
        try:
            print(f"\n🚀 开始处理 URL: {url}")
            print("=" * 60)

            # 创建日志目录
            self.create_log_directory()

            # 步骤1: 抓取网页内容
            html_content = await self.fetch_content(url)

            # 步骤2: 提取元数据
            metadata = await self.extract_metadata(html_content, url)

            # 步骤3: 上传到Readwise
            result = self.upload_to_readwise(metadata)

            total_elapsed_time = time.time() - total_start_time

            if result:
                print(f"\n🎉 处理完成! 文档已保存到 Readwise Reader")
                print(f"⏱️  总用时: {total_elapsed_time:.2f}s")
                print(f"\n📁 日志文件保存在: {self.log_dir}/")
                print(f"   01_fetched_content.md - 抓取的原始网页内容")
                print(f"   02_llm_response.txt - LLM的原始JSON响应")
                print(f"   03_extracted_metadata.json - 提取的结构化元数据")
                print(f"   04_readwise_request.json - 发送到Readwise的请求数据")
                print(f"   05_readwise_response.json - Readwise的响应结果")
                return True
            else:
                print(f"\n💔 处理失败")
                print(f"⏱️  总用时: {total_elapsed_time:.2f}s")
                print(f"\n📁 可查看日志文件分析问题: {self.log_dir}/")
                print(f"   01_fetched_content.md - 抓取的原始网页内容")
                print(f"   02_llm_response.txt - LLM的原始JSON响应")
                print(f"   03_extracted_metadata.json - 提取的结构化元数据")
                print(f"   04_readwise_request.json - 发送到Readwise的请求数据")
                print(f"   05_readwise_error.txt - Readwise的错误响应")
                return False

        except Exception as e:
            total_elapsed_time = time.time() - total_start_time
            print(f"\n❌ 处理过程中发生错误: {e}")
            print(f"⏱️  总用时: {total_elapsed_time:.2f}s")
            if self.log_dir:
                print(f"\n📁 可查看部分日志文件: {self.log_dir}/")
            return False


async def main():
    """主函数"""
    print("🔖 Readwise Reader Agent")
    print("一键保存网页到 Readwise Reader")
    print("=" * 50)

    # 检查命令行参数
    if len(sys.argv) > 1:
        url = sys.argv[1]
    else:
        url = input("请输入要保存的网页URL: ").strip()

    if not url:
        print("❌ 未提供URL")
        return

    if not url.startswith(('http://', 'https://')):
        url = 'https://' + url

    try:
        agent = ReadwiseAgent()
        success = await agent.process_url(url)

        if success:
            print(f"\n✨ 任务完成!")
        else:
            print(f"\n😞 任务失败")

    except Exception as e:
        print(f"\n💥 发生致命错误: {e}")


if __name__ == "__main__":
    asyncio.run(main())
