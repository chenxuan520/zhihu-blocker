# AGENTS.md

给 AI 编码助手 / 贡献者的工程说明。改代码前请先读完本文件。

## 项目是什么
知乎批量拉黑工具：**单一 Go 二进制 + 内嵌 Web 界面**。两条流水线：
- 回答观点流水线：`crawler.go → stance.go → review.go`
- 评论流水线：`comments.go`（爬评论 + 判定 + 出名单）
- 公共拉黑层：`blocker.go`（按 `url_token` 拉黑/恢复，两条流水线复用）

## 硬性约束（不要破坏）
1. **纯标准库，零第三方依赖**。`go.mod` 必须保持没有 `require`。需要 HTTP/JSON/并发都用标准库。
2. **密钥与登录态绝不入库**：`secret_cookies.txt`、`config.json`、`data/`、二进制 `zhb` 已在 `.gitignore`。LLM key 只经环境变量 `LLM_API_KEY` 或网页表单传入，**禁止硬编码**。提交前务必确认没有泄漏。
3. **拉黑安全**：默认 dry-run；`block --execute` 才真拉。限速、单批上限、连续失败自停（`blocker.go` 的 `_run_block`/`runBlock` 逻辑）**不可移除**。
4. 知乎请求：**读接口**只需 Cookie（无需签名）；**写接口（拉黑）**需 `x-xsrftoken`（取自 `_xsrf` Cookie）+ Cookie，同样无需 `x-zse-96`。不要去引入签名逆向。

## 文件地图
| 文件 | 职责 |
|---|---|
| `main.go` | CLI 入口与子命令分发（serve/crawl/stance/review/comments/block/unblock） |
| `config.go` | 配置加载（默认值 + `config.json` 覆盖）、Cookie/xsrf/apikey/路径辅助 |
| `httpx.go` | HTTP 层：知乎 GET、写操作 POST/DELETE、LLM POST（含代理、gzip 由标准库自动处理） |
| `crawler.go` | 抓某问题全部回答；`htmlToText` 清洗；`parseQID` |
| `stance.go` | 回答立场判定（LLM/mock，worker 池 + 缓存） |
| `review.go` | 生成 `blocklist.csv`；`ReadCandidates`/`LoadConfirmed` |
| `comments.go` | 评论爬取（根评论+楼中楼）、判定、出名单、`MyAnswers`、`MeToken` |
| `llm.go` | OpenAI 兼容客户端：`classify`（立场）、`judgeComment`（评论） |
| `blocker.go` | 拉黑/恢复，限速、可恢复（`unblock_list.json`） |
| `session.go` | 登录态自检（`/api/v4/me`） |
| `web.go` | HTTP 服务、curl 解析、后台任务（单任务模型）、favicon、`go:embed` |
| `web/index.html` | 前端单页（被 `go:embed` 编进二进制） |
| `web/favicon.png` | 站点图标（被 `go:embed`） |

## 构建 / 运行 / 自测
```bash
go build -o zhb .            # 编译（先确保能过）
./zhb serve                  # 起 Web, http://127.0.0.1:8000
./zhb comments --mine --engine all   # 无 LLM 成本地验证评论流水线
```
没有单测框架；改动后至少 `go build` 通过，并用 `mock`/`all` 引擎跑一遍对应流水线确认端到端不报错（这两种不花 LLM 额度）。

## 数据契约
- `answers.jsonl` / `comments.jsonl` / `answers_stance.jsonl`：每行一个 JSON 对象。
- `blocklist.csv` 列固定：`confirmed,url_token,name,stance,confidence,voteup,reason,answer_url,excerpt`。两条流水线都写这同一格式，Web 表格与拉黑层据此复用。**新增字段请保持向后兼容**。

## 常见改动怎么做
- **加一个判定维度**：在 `llm.go` 加一个 `judgeXxx`，在对应 pipeline 调用，最终仍写进 `blocklist.csv`。
- **加 Web 接口**：在 `web.go` 的 `mux` 注册；长任务走 `startBG`（单任务 + 日志重定向到 `data/web_job.log`，前端轮询 `/api/status`）。
- **加前端能力**：编辑 `web/index.html`（注意它是 `go:embed`，改完要重新 `go build`）。

## cf/（Cloudflare Workers 版）
与根目录 Go 版**功能一致的平行实现**，用于零服务器分享部署。栈：Worker + 一个 Durable Object（`ZhihuBlocker`，强一致存登录态/任务进度/候选名单，`waitUntil` 跑后台任务）+ Workers 静态资源（前端 = `cf/public/index.html`，是 `web/index.html` 的副本）。
- `cf/src/zhihu.ts`（爬取/拉黑/会话/curl 解析）、`cf/src/llm.ts`（判定）、`cf/src/do.ts`（DO 编排）、`cf/src/index.ts`（路由 `/api/*` → DO RPC）。
- API 契约与 Go 版完全一致，故 `index.html` 可直接复用；**改前端要两边同步**（`web/index.html` 与 `cf/public/index.html`）。
- 约束：纯 Workers 运行时（不用 Node 内建）；LLM key 用 `wrangler secret put`，登录 Cookie 存 DO，均不入库；单次判定有上限 `JUDGE_CAP`（贴合 Workers 限制）。
- 验证：`cd cf && npm install && npx wrangler types && npx tsc --noEmit && npx wrangler deploy --dry-run`；本地 `npm run dev`。
- `cf/` 自带 `.gitignore`（`node_modules/`、`.wrangler/`、`.dev.vars`、`worker-configuration.d.ts` 不提交；clone 后需先 `wrangler types`）。

## Git
- 提交信息用中文、说明「为什么」。
- 不要提交 `zhb` 二进制、`data/`、`secret_cookies.txt`、`config.json`、`cf/.dev.vars`、`cf/node_modules/`。
- 不要改动 git 全局配置。
