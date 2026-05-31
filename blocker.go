package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"time"
)

const (
	memberAPI = "https://www.zhihu.com/api/v4/members/%s?include=is_blocking"
	blockAPI  = "https://www.zhihu.com/api/v4/members/%s/actions/block"
)

func blog(c *Config, line string) {
	if f, err := os.OpenFile(c.DataPath("block_result.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintln(f, line)
		f.Close()
	}
	jprintf("%s\n", line)
}

func unblockPath(c *Config) string { return c.DataPath("unblock_list.json") }

func loadUnblock(c *Config) []map[string]string {
	var out []map[string]string
	if b, err := os.ReadFile(unblockPath(c)); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func appendUnblock(c *Config, token, name string) {
	data := loadUnblock(c)
	for _, e := range data {
		if e["url_token"] == token {
			return
		}
	}
	data = append(data, map[string]string{"url_token": token, "name": name})
	b, _ := json.MarshalIndent(data, "", "  ")
	_ = os.WriteFile(unblockPath(c), b, 0o644)
}

func isBlocking(c *Config, cookie, token string) bool {
	status, body, err := zGet(c, fmt.Sprintf(memberAPI, token), cookie, "https://www.zhihu.com/people/"+token)
	if err != nil || status != 200 {
		return false
	}
	var m struct {
		IsBlocking bool `json:"is_blocking"`
	}
	_ = json.Unmarshal(body, &m)
	return m.IsBlocking
}

func runBlock(c *Config, rows []map[string]string, execute bool) error {
	cookie, err := c.CookieStr()
	if err != nil {
		return err
	}
	xsrf := XsrfFromCookie(cookie)
	rl := c.RateLimit
	mode := "DRY-RUN(仅预览)"
	if execute {
		mode = "EXECUTE(真拉黑)"
	}
	jprintf("[block] 模式=%s | 目标 %d 人 | 单次上限 %d | 间隔 %.1f~%.1fs\n\n", mode, len(rows), rl.BlockBatchLimit, rl.BlockMinDelay, rl.BlockMaxDelay)

	blocked, skipped, failed, would, consec := 0, 0, 0, 0, 0
	for _, row := range rows {
		if blocked >= rl.BlockBatchLimit {
			blog(c, fmt.Sprintf("[block] 已达单次上限 %d, 停止。", rl.BlockBatchLimit))
			break
		}
		token := row["url_token"]
		name := row["name"]
		tag := fmt.Sprintf("%s(@%s) [%s %s]", name, token, row["stance"], row["confidence"])

		if !execute {
			ex := row["excerpt"]
			if len([]rune(ex)) > 40 {
				ex = string([]rune(ex)[:40])
			}
			blog(c, "[would] "+tag+"  "+ex)
			would++
			continue
		}
		if isBlocking(c, cookie, token) {
			blog(c, "[skip] 已拉黑: "+tag)
			skipped++
			continue
		}
		status, body, err := zWrite(c, "POST", fmt.Sprintf(blockAPI, token), cookie, xsrf, "https://www.zhihu.com/people/"+token, []byte("{}"))
		if err != nil {
			blog(c, fmt.Sprintf("[fail] %s -> %v", tag, err))
			failed++
			consec++
		} else if status == 200 || status == 201 || status == 204 {
			blog(c, "[ok] 已拉黑: "+tag)
			appendUnblock(c, token, name)
			blocked++
			consec = 0
		} else {
			blog(c, fmt.Sprintf("[fail] %s -> HTTP %d: %s", tag, status, truncate(string(body), 120)))
			failed++
			consec++
		}
		if consec >= 3 {
			blog(c, "[block] 连续失败 3 次 (疑似风控), 自动停止。")
			break
		}
		d := rl.BlockMinDelay + rand.Float64()*(rl.BlockMaxDelay-rl.BlockMinDelay)
		time.Sleep(time.Duration(d * float64(time.Second)))
	}
	jprintf("\n[block] 结束: blocked=%d skipped=%d failed=%d would=%d\n", blocked, skipped, failed, would)
	if !execute {
		jprintf("[block] 这是预览。确认无误后再执行才会真正拉黑。\n")
	}
	return nil
}

func BlockSelected(c *Config, tokens []string, execute bool) error {
	all := map[string]map[string]string{}
	for _, r := range ReadCandidates(c) {
		all[r["url_token"]] = r
	}
	var rows []map[string]string
	for _, t := range tokens {
		if t == "" {
			continue
		}
		if r, ok := all[t]; ok {
			rows = append(rows, r)
		} else {
			rows = append(rows, map[string]string{"url_token": t, "name": ""})
		}
	}
	if len(rows) == 0 {
		jprintf("[block] 未选择任何人。\n")
		return nil
	}
	return runBlock(c, rows, execute)
}

func BlockUsers(c *Config, execute bool) error {
	rows := LoadConfirmed(c)
	if len(rows) == 0 {
		jprintf("[block] blocklist.csv 中没有 confirmed=Y 的行, 无事可做。\n")
		return nil
	}
	return runBlock(c, rows, execute)
}

func Unblock(c *Config, execute bool) error {
	data := loadUnblock(c)
	if len(data) == 0 {
		jprintf("[unblock] unblock_list.json 为空, 无可恢复。\n")
		return nil
	}
	cookie, err := c.CookieStr()
	if err != nil {
		return err
	}
	xsrf := XsrfFromCookie(cookie)
	rl := c.RateLimit
	ok, fail, would := 0, 0, 0
	for _, e := range data {
		token := e["url_token"]
		if !execute {
			jprintf("[would-unblock] %s(@%s)\n", e["name"], token)
			would++
			continue
		}
		status, _, err := zWrite(c, "DELETE", fmt.Sprintf(blockAPI, token), cookie, xsrf, "https://www.zhihu.com/people/"+token, nil)
		if err == nil && (status == 200 || status == 204) {
			blog(c, fmt.Sprintf("[unblock-ok] %s(@%s)", e["name"], token))
			ok++
		} else {
			blog(c, fmt.Sprintf("[unblock-fail] @%s -> HTTP %d %v", token, status, err))
			fail++
		}
		d := rl.BlockMinDelay + rand.Float64()*(rl.BlockMaxDelay-rl.BlockMinDelay)
		time.Sleep(time.Duration(d * float64(time.Second)))
	}
	jprintf("[unblock] 结束: unblocked=%d failed=%d would=%d\n", ok, fail, would)
	return nil
}
