package main

import (
	"bufio"
	"crypto/md5"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

func loadAnswers(c *Config) ([]*Answer, error) {
	f, err := os.Open(c.DataPath("answers.jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []*Answer
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var a Answer
		if json.Unmarshal([]byte(line), &a) == nil {
			out = append(out, &a)
		}
	}
	return out, sc.Err()
}

func cacheKey(opinion, id string) string {
	h := sha1.Sum([]byte(opinion))
	return fmt.Sprintf("%x:%s", h[:4], id)
}

func loadStanceCache(c *Config) map[string]stanceResult {
	m := map[string]stanceResult{}
	if b, err := os.ReadFile(c.DataPath("stance_cache.json")); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func saveStanceCache(c *Config, m map[string]stanceResult) {
	b, _ := json.Marshal(m)
	_ = os.WriteFile(c.DataPath("stance_cache.json"), b, 0o644)
}

func mockClassify(a *Answer) stanceResult {
	if a.ContentChars == 0 {
		return stanceResult{"irrelevant", 1.0, "空回答"}
	}
	h := md5.Sum([]byte(a.AnswerID))
	switch n := int(h[0]) % 10; {
	case n < 4:
		return stanceResult{"oppose", 0.85, "[mock]"}
	case n < 7:
		return stanceResult{"support", 0.85, "[mock]"}
	case n < 9:
		return stanceResult{"neutral", 0.7, "[mock]"}
	default:
		return stanceResult{"irrelevant", 0.7, "[mock]"}
	}
}

func RunStance(c *Config, opinion, engine string, limit, minVoteup int) error {
	rows, err := loadAnswers(c)
	if err != nil {
		return fmt.Errorf("读取 answers.jsonl 失败: %w", err)
	}
	filtered := rows[:0]
	for _, r := range rows {
		if r.VoteupCount >= minVoteup {
			filtered = append(filtered, r)
		}
	}
	rows = filtered
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].VoteupCount > rows[j].VoteupCount })
	if limit > 0 && limit < len(rows) {
		rows = rows[:limit]
	}

	apiKey := ""
	if engine == "llm" {
		apiKey = c.LLMAPIKey()
		if apiKey == "" {
			return fmt.Errorf("未找到 LLM API key, 请在页面填写或设置环境变量 %s", c.LLM.APIKeyEnv)
		}
	}

	cache := loadStanceCache(c)
	total := len(rows)
	var mu sync.Mutex
	done, fail := 0, 0

	workers := 1
	if engine != "mock" && c.LLM.Concurrency > 1 {
		workers = c.LLM.Concurrency
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup

	for _, a := range rows {
		wg.Add(1)
		sem <- struct{}{}
		go func(a *Answer) {
			defer wg.Done()
			defer func() { <-sem }()
			key := cacheKey(opinion, a.AnswerID)
			mu.Lock()
			res, ok := cache[key]
			mu.Unlock()
			if !ok {
				switch {
				case engine == "mock":
					res = mockClassify(a)
				case a.ContentChars == 0:
					res = stanceResult{"irrelevant", 1.0, "空回答"}
				default:
					r, err := classify(c, apiKey, opinion, a.ContentText)
					if err != nil {
						mu.Lock()
						fail++
						mu.Unlock()
						res = stanceResult{"unclear", 0, "err"}
					} else {
						res = r
					}
				}
				mu.Lock()
				cache[key] = res
				mu.Unlock()
			}
			a.Stance = res.Stance
			a.StanceConfidence = res.Confidence
			a.StanceReason = res.Reason
			mu.Lock()
			done++
			if done%10 == 0 || done == total {
				jprintf("[stance] %d/%d (fail=%d)\n", done, total, fail)
			}
			mu.Unlock()
		}(a)
	}
	wg.Wait()

	saveStanceCache(c, cache)

	out, err := os.Create(c.DataPath("answers_stance.jsonl"))
	if err != nil {
		return err
	}
	defer out.Close()
	w := bufio.NewWriter(out)
	defer w.Flush()
	meta, _ := json.Marshal(map[string]any{"_meta": map[string]string{"opinion": opinion, "engine": engine}})
	w.Write(meta)
	w.WriteByte('\n')
	dist := map[string]int{}
	for _, a := range rows {
		dist[a.Stance]++
		line, _ := json.Marshal(a)
		w.Write(line)
		w.WriteByte('\n')
	}
	jprintf("\n[stance] 完成 %d 条, 分布=%v -> %s\n", total, dist, c.DataPath("answers_stance.jsonl"))
	return nil
}
