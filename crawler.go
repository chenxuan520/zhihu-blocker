package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html"
	"math/rand"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const answersInclude = "data[*].is_normal,is_collapsed,collapse_reason,comment_count,content," +
	"voteup_count,created_time,updated_time,question,excerpt,is_author;" +
	"data[*].author.follower_count,vip_info,badge[*].topics"

type AnswerAuthor struct {
	Name        string `json:"name"`
	URLToken    string `json:"url_token"`
	ID          string `json:"id"`
	IsAnonymous bool   `json:"is_anonymous"`
}

type Answer struct {
	AnswerID         string       `json:"answer_id"`
	AnswerURL        string       `json:"answer_url"`
	Author           AnswerAuthor `json:"author"`
	VoteupCount      int          `json:"voteup_count"`
	CommentCount     int          `json:"comment_count"`
	CreatedTime      int64        `json:"created_time"`
	ContentText      string       `json:"content_text"`
	ContentChars     int          `json:"content_chars"`
	Stance           string       `json:"stance,omitempty"`
	StanceConfidence float64      `json:"stance_confidence,omitempty"`
	StanceReason     string       `json:"stance_reason,omitempty"`
}

type apiResp struct {
	Paging struct {
		IsEnd  bool   `json:"is_end"`
		Next   string `json:"next"`
		Totals int    `json:"totals"`
	} `json:"paging"`
	Data []struct {
		ID           json.Number `json:"id"`
		Content      string      `json:"content"`
		VoteupCount  int         `json:"voteup_count"`
		CommentCount int         `json:"comment_count"`
		CreatedTime  int64       `json:"created_time"`
		Author       struct {
			Name     string `json:"name"`
			URLToken string `json:"url_token"`
			ID       string `json:"id"`
		} `json:"author"`
	} `json:"data"`
}

var (
	reScript = regexp.MustCompile(`(?is)<(script|style).*?>.*?</(script|style)>`)
	reBr     = regexp.MustCompile(`(?i)<br\s*/?>`)
	reBlock  = regexp.MustCompile(`(?i)</(p|div|li|h[1-6])>`)
	reTag    = regexp.MustCompile(`<[^>]+>`)
	reWS     = regexp.MustCompile(`\n{3,}`)
	reQID    = regexp.MustCompile(`/question/(\d+)`)
	reDigits = regexp.MustCompile(`(\d{6,})`)
)

func htmlToText(s string) string {
	if s == "" {
		return ""
	}
	s = reScript.ReplaceAllString(s, "")
	s = reBr.ReplaceAllString(s, "\n")
	s = reBlock.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = reWS.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func parseQID(q string) string {
	if m := reQID.FindStringSubmatch(q); m != nil {
		return m[1]
	}
	if m := reDigits.FindStringSubmatch(q); m != nil {
		return m[1]
	}
	return strings.TrimSpace(q)
}

func Crawl(c *Config, question string, maxAnswers int) error {
	if question == "" {
		question = c.Question
	}
	qid := parseQID(question)
	cookie, err := c.CookieStr()
	if err != nil {
		return fmt.Errorf("读取 cookie 失败: %w", err)
	}
	referer := "https://www.zhihu.com/question/" + qid
	next := fmt.Sprintf("https://www.zhihu.com/api/v4/questions/%s/answers?include=%s&limit=20&offset=0&platform=desktop&sort_by=default",
		qid, url.QueryEscape(answersInclude))

	f, err := os.Create(c.DataPath("answers.jsonl"))
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()

	seen := map[string]bool{}
	page, anon, totals := 0, 0, 0
	for next != "" {
		page++
		status, body, err := zGet(c, next, cookie, referer)
		if err != nil {
			return fmt.Errorf("采集失败: %w", err)
		}
		if status != 200 {
			return fmt.Errorf("采集失败 status=%d: %s", status, truncate(string(body), 200))
		}
		var resp apiResp
		if err := json.Unmarshal(body, &resp); err != nil {
			return fmt.Errorf("解析 JSON 失败: %w", err)
		}
		totals = resp.Paging.Totals
		added := 0
		for _, a := range resp.Data {
			if maxAnswers > 0 && len(seen) >= maxAnswers {
				break
			}
			id := a.ID.String()
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			token := a.Author.URLToken
			isAnon := token == "" || a.Author.Name == "匿名用户"
			if isAnon {
				anon++
			}
			text := htmlToText(a.Content)
			ans := Answer{
				AnswerID:  id,
				AnswerURL: fmt.Sprintf("https://www.zhihu.com/question/%s/answer/%s", qid, id),
				Author: AnswerAuthor{
					Name: a.Author.Name, URLToken: token, ID: a.Author.ID, IsAnonymous: isAnon,
				},
				VoteupCount:  a.VoteupCount,
				CommentCount: a.CommentCount,
				CreatedTime:  a.CreatedTime,
				ContentText:  text,
				ContentChars: utf8.RuneCountInString(text),
			}
			line, _ := json.Marshal(ans)
			w.Write(line)
			w.WriteByte('\n')
			added++
		}
		jprintf("page %3d | +%2d | 累计 %4d/%d | is_end=%v\n", page, added, len(seen), totals, resp.Paging.IsEnd)
		if maxAnswers > 0 && len(seen) >= maxAnswers {
			jprintf("[crawl] 已达抓取上限 %d, 停止。\n", maxAnswers)
			break
		}
		if resp.Paging.IsEnd || len(resp.Data) == 0 {
			break
		}
		next = resp.Paging.Next
		d := c.CrawlDelay[0] + rand.Float64()*(c.CrawlDelay[1]-c.CrawlDelay[0])
		time.Sleep(time.Duration(d * float64(time.Second)))
	}
	jprintf("\n[crawl] 完成: %d 个回答 (匿名 %d) -> %s\n", len(seen), anon, c.DataPath("answers.jsonl"))
	return nil
}

func cachedAnswerCount(c *Config, qid string) int {
	rows, err := loadAnswers(c)
	if err != nil {
		return 0
	}
	count := 0
	for _, a := range rows {
		if a.AnswerID == "" {
			continue
		}
		if parseQID(a.AnswerURL) != qid {
			return 0
		}
		count++
	}
	return count
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
