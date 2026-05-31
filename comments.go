package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var reAnsID = regexp.MustCompile(`/answer/(\d+)`)

func parseAnswerID(s string) string {
	if m := reAnsID.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	if m := reDigits.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return strings.TrimSpace(s)
}

type Comment struct {
	CommentID   string       `json:"comment_id"`
	AnswerID    string       `json:"answer_id"`
	AnswerURL   string       `json:"answer_url"`
	Author      AnswerAuthor `json:"author"`
	Content     string       `json:"content_text"`
	LikeCount   int          `json:"like_count"`
	IsReply     bool         `json:"is_reply"`
	ReplyToName string       `json:"reply_to_name"`
	Flag        bool         `json:"flag,omitempty"`
	Confidence  float64      `json:"confidence,omitempty"`
	Reason      string       `json:"reason,omitempty"`
}

type apiComment struct {
	ID                json.Number `json:"id"`
	Content           string      `json:"content"`
	LikeCount         int         `json:"like_count"`
	IsAuthor          bool        `json:"is_author"`
	ChildCommentCount int         `json:"child_comment_count"`
	URL               string      `json:"url"`
	Author            struct {
		Name        string `json:"name"`
		URLToken    string `json:"url_token"`
		IsAnonymous bool   `json:"is_anonymous"`
	} `json:"author"`
	ReplyToAuthor *struct {
		Member struct {
			Name string `json:"name"`
		} `json:"member"`
	} `json:"reply_to_author"`
	ChildComments []apiComment `json:"child_comments"`
}

type apiCommentResp struct {
	Data   []apiComment `json:"data"`
	Paging struct {
		IsEnd  bool   `json:"is_end"`
		Next   string `json:"next"`
		Totals int    `json:"totals"`
	} `json:"paging"`
}

var cachedMeToken string

func MeToken(c *Config) (string, error) {
	if cachedMeToken != "" {
		return cachedMeToken, nil
	}
	cookie, err := c.CookieStr()
	if err != nil {
		return "", err
	}
	status, body, err := zGet(c, "https://www.zhihu.com/api/v4/me", cookie, "https://www.zhihu.com/")
	if err != nil || status != 200 {
		return "", fmt.Errorf("获取当前用户失败 (HTTP %d)", status)
	}
	var me struct {
		URLToken string `json:"url_token"`
	}
	_ = json.Unmarshal(body, &me)
	cachedMeToken = me.URLToken
	return cachedMeToken, nil
}

type MyAnswer struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	CommentCount int    `json:"comment_count"`
	URL          string `json:"url"`
}

func MyAnswers(c *Config, limit int) ([]MyAnswer, error) {
	cookie, err := c.CookieStr()
	if err != nil {
		return nil, err
	}
	tok, err := MeToken(c)
	if err != nil {
		return nil, err
	}
	inc := url.QueryEscape("data[*].comment_count,question.title")
	u := fmt.Sprintf("https://www.zhihu.com/api/v4/members/%s/answers?limit=%d&offset=0&include=%s", tok, limit, inc)
	status, body, err := zGet(c, u, cookie, "https://www.zhihu.com/")
	if err != nil || status != 200 {
		return nil, fmt.Errorf("获取我的回答失败 (HTTP %d)", status)
	}
	var resp struct {
		Data []struct {
			ID           json.Number `json:"id"`
			CommentCount int         `json:"comment_count"`
			URL          string      `json:"url"`
			Question     struct {
				Title string `json:"title"`
			} `json:"question"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &resp)
	var out []MyAnswer
	for _, a := range resp.Data {
		out = append(out, MyAnswer{ID: a.ID.String(), Title: a.Question.Title, CommentCount: a.CommentCount, URL: a.URL})
	}
	return out, nil
}

func appendComment(out []*Comment, ac *apiComment, answerID, meTok string, isReply bool) []*Comment {
	token := ac.Author.URLToken
	if ac.IsAuthor || token == "" || ac.Author.IsAnonymous || token == meTok {
		return out // 跳过自己 / 匿名 / 无主
	}
	replyTo := ""
	if ac.ReplyToAuthor != nil {
		replyTo = ac.ReplyToAuthor.Member.Name
	}
	cm := &Comment{
		CommentID:   ac.ID.String(),
		AnswerID:    answerID,
		AnswerURL:   ac.URL,
		Author:      AnswerAuthor{Name: ac.Author.Name, URLToken: token},
		Content:     htmlToText(ac.Content),
		LikeCount:   ac.LikeCount,
		IsReply:     isReply,
		ReplyToName: replyTo,
	}
	return append(out, cm)
}

// CrawlComments 抓取某答案下的评论 (根评论 + 内联楼中楼; fetchReplies=true 时翻页抓全部楼中楼)。
func CrawlComments(c *Config, answerID string, fetchReplies bool) ([]*Comment, error) {
	cookie, err := c.CookieStr()
	if err != nil {
		return nil, err
	}
	meTok, _ := MeToken(c)
	const referer = "https://www.zhihu.com/"
	next := fmt.Sprintf("https://www.zhihu.com/api/v4/comment_v5/answers/%s/root_comment?order_by=score&limit=20", answerID)

	var out []*Comment
	childCount := 0
	for next != "" {
		status, body, err := zGet(c, next, cookie, referer)
		if err != nil {
			return out, err
		}
		if status != 200 {
			return out, fmt.Errorf("评论接口 HTTP %d: %s", status, truncate(string(body), 160))
		}
		var resp apiCommentResp
		if json.Unmarshal(body, &resp) != nil {
			return out, fmt.Errorf("解析评论 JSON 失败")
		}
		for i := range resp.Data {
			root := &resp.Data[i]
			out = appendComment(out, root, answerID, meTok, false)
			seen := map[string]bool{}
			for j := range root.ChildComments {
				out = appendComment(out, &root.ChildComments[j], answerID, meTok, true)
				seen[root.ChildComments[j].ID.String()] = true
			}
			if fetchReplies && root.ChildCommentCount > len(root.ChildComments) && childCount < 3000 {
				cnext := fmt.Sprintf("https://www.zhihu.com/api/v4/comment_v5/comment/%s/child_comment?order_by=ts&limit=20", root.ID.String())
				for cnext != "" && childCount < 3000 {
					st, b2, e2 := zGet(c, cnext, cookie, referer)
					if e2 != nil || st != 200 {
						break
					}
					var cr apiCommentResp
					if json.Unmarshal(b2, &cr) != nil {
						break
					}
					for k := range cr.Data {
						if seen[cr.Data[k].ID.String()] {
							continue
						}
						out = appendComment(out, &cr.Data[k], answerID, meTok, true)
						childCount++
					}
					if cr.Paging.IsEnd || len(cr.Data) == 0 {
						break
					}
					cnext = cr.Paging.Next
					time.Sleep(time.Duration((0.3 + rand.Float64()*0.4) * float64(time.Second)))
				}
			}
		}
		jprintf("[comments] 答案 %s 累计 %d 条 | is_end=%v\n", answerID, len(out), resp.Paging.IsEnd)
		if resp.Paging.IsEnd || len(resp.Data) == 0 {
			break
		}
		next = resp.Paging.Next
		time.Sleep(time.Duration((0.5 + rand.Float64()*0.5) * float64(time.Second)))
	}
	return out, nil
}

func writeCommentsFile(c *Config, comments []*Comment) {
	f, err := os.Create(c.DataPath("comments.jsonl"))
	if err != nil {
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()
	for _, cm := range comments {
		line, _ := json.Marshal(cm)
		w.Write(line)
		w.WriteByte('\n')
	}
}

func JudgeComments(c *Config, criterion, engine string, comments []*Comment) {
	apiKey := ""
	if engine == "llm" {
		apiKey = c.LLMAPIKey()
	}
	total := len(comments)
	var mu sync.Mutex
	done, fail := 0, 0
	workers := 1
	if engine == "llm" && c.LLM.Concurrency > 1 {
		workers = c.LLM.Concurrency
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, cm := range comments {
		wg.Add(1)
		sem <- struct{}{}
		go func(cm *Comment) {
			defer wg.Done()
			defer func() { <-sem }()
			switch engine {
			case "all":
				cm.Flag, cm.Confidence, cm.Reason = true, 1.0, "(人工勾选)"
			case "mock":
				h := 0
				for _, ch := range cm.CommentID {
					h += int(ch)
				}
				cm.Flag = h%3 == 0
				cm.Confidence, cm.Reason = 0.85, "[mock]"
			default:
				r, err := judgeComment(c, apiKey, criterion, cm.Content)
				if err != nil {
					mu.Lock()
					fail++
					mu.Unlock()
					cm.Flag, cm.Confidence, cm.Reason = false, 0, "err"
				} else {
					cm.Flag = r.Flag && r.Confidence >= c.Stance.Threshold
					cm.Confidence, cm.Reason = r.Confidence, r.Reason
				}
			}
			mu.Lock()
			done++
			if done%10 == 0 || done == total {
				jprintf("[judge] %d/%d (fail=%d)\n", done, total, fail)
			}
			mu.Unlock()
		}(cm)
	}
	wg.Wait()
}

func BuildCommentBlocklist(c *Config, comments []*Comment) error {
	byAuthor := map[string]*Comment{}
	for _, cm := range comments {
		if !cm.Flag || cm.Author.URLToken == "" {
			continue
		}
		t := cm.Author.URLToken
		if cur, ok := byAuthor[t]; !ok || cm.LikeCount > cur.LikeCount {
			byAuthor[t] = cm
		}
	}
	final := make([]*Comment, 0, len(byAuthor))
	for _, cm := range byAuthor {
		final = append(final, cm)
	}
	sort.SliceStable(final, func(i, j int) bool {
		if final[i].Confidence != final[j].Confidence {
			return final[i].Confidence > final[j].Confidence
		}
		return final[i].LikeCount > final[j].LikeCount
	})

	f, err := os.Create(c.DataPath("blocklist.csv"))
	if err != nil {
		return err
	}
	defer f.Close()
	cw := csv.NewWriter(f)
	defer cw.Flush()
	cw.Write(blocklistHeader)
	for _, cm := range final {
		ex := strings.ReplaceAll(cm.Content, "\n", " ")
		if utf8.RuneCountInString(ex) > 70 {
			ex = string([]rune(ex)[:70])
		}
		tag := "评论"
		if cm.IsReply {
			tag = "回复"
		}
		cw.Write([]string{
			"N", cm.Author.URLToken, cm.Author.Name, tag,
			fmt.Sprintf("%.2f", cm.Confidence), strconv.Itoa(cm.LikeCount),
			cm.Reason, cm.AnswerURL, ex,
		})
	}
	jprintf("[review] 评论候选拉黑 %d 人 -> %s\n", len(final), c.DataPath("blocklist.csv"))
	return nil
}

// RunCommentPipeline: 爬评论 -> 判定 -> 出名单(写入同一 blocklist.csv)。
func RunCommentPipeline(c *Config, source, criterion, engine string, limit int, fetchReplies bool) error {
	var ids []string
	src := strings.TrimSpace(source)
	if src == "" || src == "my" || src == "mine" {
		mas, err := MyAnswers(c, 20)
		if err != nil {
			return err
		}
		for _, a := range mas {
			if a.CommentCount > 0 {
				ids = append(ids, a.ID)
			}
		}
		jprintf("[comments] 我的回答中有评论的 %d 条\n", len(ids))
	} else {
		ids = []string{parseAnswerID(src)}
	}
	if engine == "llm" && c.LLMAPIKey() == "" {
		return fmt.Errorf("未找到 LLM API key, 请在页面填写或设置环境变量 %s", c.LLM.APIKeyEnv)
	}

	var all []*Comment
	for _, id := range ids {
		cs, err := CrawlComments(c, id, fetchReplies)
		if err != nil {
			jprintf("[comments] 答案 %s 抓取出错: %v\n", id, err)
			continue
		}
		all = append(all, cs...)
		if len(all) > 4000 {
			jprintf("[comments] 已达 4000 条上限, 停止抓取。\n")
			break
		}
	}
	jprintf("[comments] 共收集 %d 条非本人评论\n", len(all))

	sort.SliceStable(all, func(i, j int) bool { return all[i].LikeCount > all[j].LikeCount })
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}
	writeCommentsFile(c, all)
	JudgeComments(c, criterion, engine, all)
	return BuildCommentBlocklist(c, all)
}
