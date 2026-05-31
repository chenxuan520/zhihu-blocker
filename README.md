<div align="center">
  <img src="web/favicon.png" width="112" alt="zhihu-blocker logo">

# zhihu-blocker

**人生不是辩论赛，与其相互说服，不如相互删除。**

知乎批量拉黑工具 · 单一 Go 二进制 · 零外部依赖 · 自带 Web 界面

</div>

---

爬取一个知乎**问题下的全部回答**（或一条回答下的**全部评论**），用 LLM 按**你的观点**或**屏蔽标准**判定立场，把"持不同观点的人"或"评论区杠精"列成清单，勾选后**一键拉黑**（随时可一键恢复）。

> 全程在你**本地**运行：出网请求用你自己的 IP，避开机房 / Serverless 共享 IP 被风控的问题。

## ✨ 功能

### 两种模式
- **按回答观点拉黑**：给一个问题链接 + 你的观点 → 抓全部回答 → LLM 判每条是「支持 / 反对 / 中立 / 无关」→ 反对者进候选。
- **按评论拉黑（清评论区杠精）**：选你的某条回答（或填答案链接，或留空扫你全部有评论的回答）+ 屏蔽标准 → 抓评论（含楼中楼回复）→ LLM 挑出辱骂 / 抬杠 / 阴阳怪气 → 进候选。也可选「全部列出」不调 AI、纯人工勾。

### 特性
- 🔐 **登录态自检**：打开页面自动用已存 Cookie 验证是否有效，失效才让你重新粘 curl。
- 📋 **粘 curl 即用**：浏览器 F12 → Copy as cURL → 粘进去，自动解析 Cookie 与 UA。
- ✅ **勾选式拉黑**：默认 dry-run 预览，确认后才执行；**一键恢复**全部拉黑。
- 🛡️ **风控保护**：随机间隔限速 + 单批上限 + 连续失败自动停。
- 🤖 **OpenAI 兼容**：OpenAI / DeepSeek / 通义 / 豆包 / 小米 MiMo 等，改 `base_url` + `model` 即可。
- 💾 **判定缓存**：同一观点同一条不重复调用，省 token。
- 📦 **纯标准库**：无第三方依赖，`go build` 出一个文件，前端与图标 `go:embed` 内嵌。

## 🧭 工作原理

```
问题/回答链接 ──▶ 抓取(回答|评论) ──▶ LLM 判定(立场|是否杠精) ──▶ 候选名单 ──▶ 勾选 ──▶ 拉黑
                                                                              └──▶ 一键恢复
```

技术要点：知乎**读接口**带登录 Cookie 即可（无需逆向签名）；**拉黑写接口**带 `x-xsrftoken` 也无需签名。所以整套不碰 `x-zse-96` 逆向，纯 HTTP 即可跑。

## 🚀 快速开始

### 依赖
- Go 1.25+

### 构建
```bash
go build -o zhb .
```

### 配置
```bash
cp config.example.json config.json     # 按需改 base_url / model
export LLM_API_KEY=sk-xxxxxx            # 你的 LLM key (也可在网页表单里填)
```

### 提供登录态（二选一）
- **推荐**：启动后在网页里粘 curl（自动提取 Cookie + UA）。
- 或手动：把浏览器请求的整段 Cookie 写到 `secret_cookies.txt`（一行，含 `z_c0` / `d_c0` / `_xsrf` / `__zse_ck`）。

### 启动 Web 界面
```bash
./zhb serve            # 默认 http://127.0.0.1:8000
./zhb serve --port 9000
```
打开页面 → ① 确认登录态 → ② 选模式填观点/标准 → ③ 开始分析 → ④ 勾选 → 拉黑。

### 命令行（无界面也能用）
```bash
./zhb crawl   --question https://www.zhihu.com/question/xxx
./zhb stance  --opinion "你的观点" --limit 30
./zhb review
./zhb comments --mine --engine all          # 列出我所有回答评论区的人, 人工勾
./zhb comments --answer <url> --criterion "辱骂、抬杠、阴阳怪气"
./zhb block                                  # 预览 (dry-run)
./zhb block --execute                        # 真拉黑 (对 blocklist.csv 中 confirmed=Y 的人)
./zhb unblock --execute                      # 一键恢复
```

## ⚙️ 配置项（config.json）

| 字段 | 说明 |
|---|---|
| `cookie_file` | 登录 Cookie 文件路径（默认 `secret_cookies.txt`） |
| `llm.base_url` / `llm.model` | LLM 接口与模型（OpenAI 兼容） |
| `llm.api_key_env` | 读哪个环境变量当 key（默认 `LLM_API_KEY`） |
| `llm.concurrency` | LLM 并发数 |
| `stance.threshold` | 判定置信度阈值，低于此不进候选（默认 0.8） |
| `stance.block_stances` | 哪些立场进候选（默认 `["oppose"]`） |
| `rate_limit.*` | 拉黑间隔 / 单批上限 |
| `proxy` | 可选出网代理 |

## 📂 数据文件（`data/`，已 gitignore）

| 文件 | 内容 |
|---|---|
| `blocklist.csv` | 候选名单（网页表格读它；每次分析覆盖重写） |
| `answers.jsonl` / `comments.jsonl` | 抓到的回答 / 评论 |
| `answers_stance.jsonl` | 立场判定结果 |
| `stance_cache.json` | LLM 判定缓存 |
| `unblock_list.json` | 已拉黑记录（一键恢复靠它） |

## ☁️ 部署到服务器

```bash
GOOS=linux GOARCH=amd64 go build -o zhb .   # 交叉编译
# scp zhb 到服务器, 配好 config.json / secret_cookies.txt, ./zhb serve
```
单文件、零运行时依赖。⚠️ 注意：在机房 IP 上批量请求更易被风控，建议保守限速或挂住宅代理。

## ⚠️ 风险与免责

- 自动化抓取与批量操作属知乎 ToS 灰色地带，**仅供个人小范围、低频使用**，请勿商用或公开服务。
- 短时间大量拉黑可能触发风控，请保持限速；本工具已内置间隔 / 上限 / 失败自停。
- LLM 判定可能出错，**强烈建议用 dry-run 预览 + 人工勾选**，避免误伤。
- 拉黑可逆：所有操作记入 `unblock_list.json`，`unblock --execute` 可一键恢复。
- 按观点过滤会形成信息茧房——这是你的自由，仅作提醒。
- 本项目仅供学习交流，使用造成的一切后果由使用者自负。

## 📄 License

MIT
