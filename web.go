package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
)

//go:embed web/index.html
var indexHTML []byte

//go:embed web/favicon.png
var faviconPNG []byte

// ---- 任务进度输出 (单任务模型, 重定向到日志文件) ----
var jobOut io.Writer = os.Stdout

func jprintf(format string, a ...any) { fmt.Fprintf(jobOut, format, a...) }

type bgState struct {
	mu      sync.Mutex
	state   string
	kind    string
	errMsg  string
	logfile string
}

var BG = &bgState{state: "idle", logfile: "data/web_job.log"}

func (b *bgState) snapshot() (string, string, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state, b.kind, b.errMsg
}

func startBG(kind string, fn func() error) bool {
	BG.mu.Lock()
	if BG.state == "running" {
		BG.mu.Unlock()
		return false
	}
	BG.state, BG.kind, BG.errMsg = "running", kind, ""
	BG.mu.Unlock()

	go func() {
		_ = os.MkdirAll("data", 0o755)
		f, ferr := os.Create(BG.logfile)
		if ferr == nil {
			jobOut = f
		}
		defer func() {
			if r := recover(); r != nil {
				BG.mu.Lock()
				BG.state, BG.errMsg = "error", fmt.Sprint(r)
				BG.mu.Unlock()
			}
			jobOut = os.Stdout
			if f != nil {
				f.Close()
			}
		}()
		err := fn()
		BG.mu.Lock()
		if err != nil {
			BG.state, BG.errMsg = "error", err.Error()
		} else {
			BG.state = "done"
		}
		BG.mu.Unlock()
	}()
	return true
}

// ---- 运行时状态 ----
var pastedUA string

func currentCfg(o map[string]any) *Config {
	c := LoadConfig("config.json")
	if pastedUA != "" {
		c.UserAgent = pastedUA
	}
	if v, ok := o["model"].(string); ok && v != "" {
		c.LLM.Model = v
	}
	if v, ok := o["api_key"].(string); ok && v != "" {
		c.LLM.APIKey = v
	}
	if v, ok := o["concurrency"]; ok {
		c.LLM.Concurrency = toInt(v, c.LLM.Concurrency)
	}
	if v, ok := o["threshold"]; ok {
		c.Stance.Threshold = toFloat(v, c.Stance.Threshold)
	}
	return c
}

func toInt(v any, def int) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
			return n
		}
	}
	return def
}

func toFloat(v any, def float64) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return f
		}
	}
	return def
}

func toStrSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ---- 任务 ----
func analyzeJob(body map[string]any) error {
	c := currentCfg(body)
	engine, _ := body["engine"].(string)
	if engine == "" {
		engine = "llm"
	}
	// 评论模式: 爬评论 -> 判定 -> 名单
	if mode, _ := body["mode"].(string); mode == "comment" {
		source, _ := body["answer"].(string)
		criterion, _ := body["criterion"].(string)
		replies, _ := body["replies"].(bool)
		return RunCommentPipeline(c, source, criterion, engine, toInt(body["limit"], 0), replies)
	}
	// 回答观点模式
	question, _ := body["question"].(string)
	opinion, _ := body["opinion"].(string)
	limit := toInt(body["limit"], 0)
	qid := parseQID(question)
	if qid == "" {
		qid = parseQID(c.Question)
	}
	if cached := cachedAnswerCount(c, qid); limit > 0 && cached >= limit {
		jprintf("[crawl] 复用本地回答: 问题 %s 已有 %d 条, 本次需要 %d 条。\n", qid, cached, limit)
	} else {
		if err := Crawl(c, question, limit); err != nil {
			return err
		}
	}
	if err := RunStance(c, opinion, engine, limit, toInt(body["min_voteup"], 0)); err != nil {
		return err
	}
	return BuildBlocklist(c)
}

// ---- curl 解析 ----
func shellSplit(s string) []string {
	var out []string
	var buf strings.Builder
	inS, inD, has := false, false, false
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		ch := r[i]
		switch {
		case inS:
			if ch == '\'' {
				inS = false
			} else {
				buf.WriteRune(ch)
			}
		case inD:
			if ch == '"' {
				inD = false
			} else if ch == '\\' && i+1 < len(r) {
				i++
				buf.WriteRune(r[i])
			} else {
				buf.WriteRune(ch)
			}
		default:
			switch ch {
			case '\'':
				inS, has = true, true
			case '"':
				inD, has = true, true
			case '\\':
				if i+1 < len(r) {
					i++
					if r[i] != '\n' {
						buf.WriteRune(r[i])
						has = true
					}
				}
			case ' ', '\t', '\n', '\r':
				if has {
					out = append(out, buf.String())
					buf.Reset()
					has = false
				}
			default:
				buf.WriteRune(ch)
				has = true
			}
		}
	}
	if has {
		out = append(out, buf.String())
	}
	return out
}

func parseCurl(text string) (cookie, ua string) {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "curl")
	parts := shellSplit(text)
	for i := 0; i < len(parts); i++ {
		p := parts[i]
		if (p == "-b" || p == "--cookie") && i+1 < len(parts) {
			cookie = parts[i+1]
			i++
		} else if (p == "-H" || p == "--header") && i+1 < len(parts) {
			h := parts[i+1]
			i++
			low := strings.ToLower(h)
			if strings.HasPrefix(low, "cookie:") {
				cookie = strings.TrimSpace(h[len("cookie:"):])
			} else if strings.HasPrefix(low, "user-agent:") {
				ua = strings.TrimSpace(h[len("user-agent:"):])
			}
		} else if (p == "-A" || p == "--user-agent") && i+1 < len(parts) {
			ua = parts[i+1]
			i++
		}
	}
	return
}

// ---- HTTP 辅助 ----
func writeJSON(w http.ResponseWriter, code int, obj any) {
	b, _ := json.Marshal(obj)
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	w.Write(b)
}

func readBody(r *http.Request) map[string]any {
	m := map[string]any{}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&m)
	}
	return m
}

func tailFile(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return string(b)
}

func Serve(port int) {
	_ = os.MkdirAll("data", 0o755)
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})

	favicon := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "image/png")
		w.Header().Set("cache-control", "public, max-age=86400")
		w.Write(faviconPNG)
	}
	mux.HandleFunc("/favicon.ico", favicon)
	mux.HandleFunc("/favicon.png", favicon)

	mux.HandleFunc("/api/session", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, CheckSession(currentCfg(nil)))
	})

	mux.HandleFunc("/api/candidates", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"candidates": ReadCandidates(LoadConfig("config.json"))})
	})

	mux.HandleFunc("/api/clear", func(w http.ResponseWriter, r *http.Request) {
		_ = os.Remove(LoadConfig("config.json").DataPath("blocklist.csv"))
		writeJSON(w, 200, map[string]any{"ok": true})
	})

	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		s, k, e := BG.snapshot()
		writeJSON(w, 200, map[string]any{"state": s, "kind": k, "error": e, "log": tailFile(BG.logfile, 6000)})
	})

	mux.HandleFunc("/api/curl", func(w http.ResponseWriter, r *http.Request) {
		body := readBody(r)
		curl, _ := body["curl"].(string)
		cookie, ua := parseCurl(curl)
		if cookie == "" {
			writeJSON(w, 400, map[string]any{"ok": false, "msg": "没从 curl 里解析到 cookie"})
			return
		}
		c := LoadConfig("config.json")
		_ = os.WriteFile(c.CookieFile, []byte(strings.TrimSpace(cookie)+"\n"), 0o644)
		if ua != "" {
			pastedUA = ua
		}
		has := map[string]bool{}
		for _, k := range []string{"z_c0", "d_c0", "_xsrf", "__zse_ck"} {
			has[k] = strings.Contains(cookie, k+"=")
		}
		writeJSON(w, 200, map[string]any{"ok": true, "cookie_len": len(cookie), "ua": ua, "has": has})
	})

	mux.HandleFunc("/api/my_answers", func(w http.ResponseWriter, r *http.Request) {
		mas, err := MyAnswers(currentCfg(nil), 30)
		if err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "msg": err.Error(), "answers": []any{}})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "answers": mas})
	})

	mux.HandleFunc("/api/run", func(w http.ResponseWriter, r *http.Request) {
		body := readBody(r)
		mode, _ := body["mode"].(string)
		if mode != "comment" {
			if op, _ := body["opinion"].(string); strings.TrimSpace(op) == "" {
				writeJSON(w, 400, map[string]any{"ok": false, "msg": "请填写你的观点"})
				return
			}
		}
		if !startBG("analyze", func() error { return analyzeJob(body) }) {
			writeJSON(w, 409, map[string]any{"ok": false, "msg": "已有任务在运行, 请稍候"})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})

	mux.HandleFunc("/api/block", func(w http.ResponseWriter, r *http.Request) {
		body := readBody(r)
		tokens := toStrSlice(body["tokens"])
		if len(tokens) == 0 {
			writeJSON(w, 400, map[string]any{"ok": false, "msg": "未勾选任何人"})
			return
		}
		execute, _ := body["execute"].(bool)
		if !startBG("block", func() error { return BlockSelected(currentCfg(nil), tokens, execute) }) {
			writeJSON(w, 409, map[string]any{"ok": false, "msg": "已有任务在运行, 请稍候"})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})

	mux.HandleFunc("/api/unblock", func(w http.ResponseWriter, r *http.Request) {
		body := readBody(r)
		execute, _ := body["execute"].(bool)
		if !startBG("unblock", func() error { return Unblock(currentCfg(nil), execute) }) {
			writeJSON(w, 409, map[string]any{"ok": false, "msg": "已有任务在运行, 请稍候"})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	fmt.Printf("\n  zhblock(Go) Web 已启动 ->  http://%s\n  (Ctrl+C 退出)\n\n", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintln(os.Stderr, "服务启动失败:", err)
	}
}
