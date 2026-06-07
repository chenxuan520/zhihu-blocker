package main

import (
	"flag"
	"fmt"
	"os"
)

const usage = `zhblock (Go) — 按观点拉黑知乎答主

用法:
  zhblock serve [--port 8000]                          启动 Web 界面 (推荐)
  zhblock crawl [--question URL|ID]                    抓取问题全部回答
  zhblock stance --opinion "你的观点" [--engine llm|mock] [--limit N] [--min-voteup N]
  zhblock review                                       生成拉黑候选 blocklist.csv
  zhblock comments [--answer URL|--mine] [--criterion "..."] [--engine llm|all|mock] [--replies]
                                                       按评论拉黑: 爬评论->判定->名单
  zhblock block [--execute]                            对 confirmed=Y 的人拉黑
  zhblock unblock [--execute]                          按 unblock_list.json 恢复
`

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func main() {
	if len(os.Args) < 2 {
		Serve(8000)
		return
	}
	cfg := func() *Config { return LoadConfig("config.json") }
	switch os.Args[1] {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		port := fs.Int("port", 8000, "端口")
		fs.Parse(os.Args[2:])
		Serve(*port)
	case "crawl":
		fs := flag.NewFlagSet("crawl", flag.ExitOnError)
		q := fs.String("question", "", "问题 URL 或 ID")
		fs.Parse(os.Args[2:])
		must(Crawl(cfg(), *q, 0))
	case "stance":
		fs := flag.NewFlagSet("stance", flag.ExitOnError)
		opinion := fs.String("opinion", "", "你的观点")
		engine := fs.String("engine", "llm", "llm 或 mock")
		limit := fs.Int("limit", 0, "只判赞数最高的前 N 条 (0=全部)")
		minv := fs.Int("min-voteup", 0, "最低赞数")
		fs.Parse(os.Args[2:])
		if *opinion == "" {
			fmt.Fprintln(os.Stderr, "stance 需要 --opinion")
			os.Exit(1)
		}
		must(RunStance(cfg(), *opinion, *engine, *limit, *minv))
	case "review":
		must(BuildBlocklist(cfg()))
	case "comments":
		fs := flag.NewFlagSet("comments", flag.ExitOnError)
		answer := fs.String("answer", "", "答案 URL 或 ID")
		mine := fs.Bool("mine", false, "扫描我自己最近的回答 (忽略 --answer)")
		criterion := fs.String("criterion", "", "屏蔽标准 (评论符合则拉黑)")
		engine := fs.String("engine", "llm", "llm / all / mock")
		limit := fs.Int("limit", 0, "最多判定多少条评论 (0=全部)")
		replies := fs.Bool("replies", false, "抓取楼中楼回复 (更全但更慢)")
		fs.Parse(os.Args[2:])
		src := *answer
		if *mine {
			src = "my"
		}
		must(RunCommentPipeline(cfg(), src, *criterion, *engine, *limit, *replies))
	case "block":
		fs := flag.NewFlagSet("block", flag.ExitOnError)
		ex := fs.Bool("execute", false, "真正执行拉黑")
		fs.Parse(os.Args[2:])
		must(BlockUsers(cfg(), *ex))
	case "unblock":
		fs := flag.NewFlagSet("unblock", flag.ExitOnError)
		ex := fs.Bool("execute", false, "真正执行取消拉黑")
		fs.Parse(os.Args[2:])
		must(Unblock(cfg(), *ex))
	default:
		fmt.Print(usage)
	}
}
