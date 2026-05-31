package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const llmSystem = "你是一个严谨、中立的文本立场分析器。给定[用户观点]和[一条知乎回答], " +
	"判断该回答相对于该观点的立场。注意识别反讽、反问、阴阳怪气。" +
	"只输出一个 JSON 对象, 不要任何多余文字。"

func llmUserPrompt(opinion, answer string) string {
	return "[用户观点]\n" + opinion + "\n\n[一条知乎回答]\n" + answer + "\n\n" +
		"请输出 JSON: {\"stance\": \"support|oppose|neutral|irrelevant\", " +
		"\"confidence\": 0到1的小数, \"reason\": \"不超过30字的理由\"}\n" +
		"- support: 回答支持/认同该观点\n- oppose: 回答反对/反驳该观点\n" +
		"- neutral: 中立或两面都谈\n- irrelevant: 与该观点无关"
}

var reJSONObj = regexp.MustCompile(`(?s)\{.*\}`)
var reFence = regexp.MustCompile("(?s)^```(?:json)?|```$")

type stanceResult struct {
	Stance     string  `json:"stance"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

func extractStance(content string) (stanceResult, error) {
	t := strings.TrimSpace(content)
	t = strings.TrimSpace(reFence.ReplaceAllString(t, ""))
	var raw map[string]any
	if err := json.Unmarshal([]byte(t), &raw); err != nil {
		m := reJSONObj.FindString(t)
		if m == "" || json.Unmarshal([]byte(m), &raw) != nil {
			return stanceResult{}, fmt.Errorf("无法解析模型输出: %s", truncate(t, 120))
		}
	}
	st, _ := raw["stance"].(string)
	st = strings.ToLower(strings.TrimSpace(st))
	switch st {
	case "support", "oppose", "neutral", "irrelevant":
	default:
		st = "unclear"
	}
	var conf float64
	switch v := raw["confidence"].(type) {
	case float64:
		conf = v
	case string:
		conf, _ = strconv.ParseFloat(strings.TrimSpace(v), 64)
	}
	if conf < 0 {
		conf = 0
	} else if conf > 1 {
		conf = 1
	}
	reason, _ := raw["reason"].(string)
	if len([]rune(reason)) > 60 {
		reason = string([]rune(reason)[:60])
	}
	return stanceResult{Stance: st, Confidence: conf, Reason: reason}, nil
}

func classify(c *Config, apiKey, opinion, answerText string) (stanceResult, error) {
	ans := answerText
	if r := []rune(ans); len(r) > c.LLM.MaxChars {
		ans = string(r[:c.LLM.MaxChars])
	}
	reqBody := map[string]any{
		"model":       c.LLM.Model,
		"temperature": c.LLM.Temperature,
		"messages": []map[string]string{
			{"role": "system", "content": llmSystem},
			{"role": "user", "content": llmUserPrompt(opinion, ans)},
		},
	}
	bs, _ := json.Marshal(reqBody)
	endpoint := strings.TrimRight(c.LLM.BaseURL, "/") + "/chat/completions"
	status, body, err := llmPost(c, endpoint, apiKey, bs)
	if err != nil {
		return stanceResult{}, err
	}
	if status != 200 {
		return stanceResult{}, fmt.Errorf("LLM HTTP %d: %s", status, truncate(string(body), 200))
	}
	var cr struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &cr); err != nil || len(cr.Choices) == 0 {
		return stanceResult{}, fmt.Errorf("LLM 响应异常: %s", truncate(string(body), 200))
	}
	return extractStance(cr.Choices[0].Message.Content)
}

const judgeSystem = "你是社区评论审核助手。给定[屏蔽标准]和[一条评论], 判断该评论是否符合屏蔽标准 " +
	"(即是否应该拉黑发该评论的人)。注意识别辱骂、人身攻击、阴阳怪气、引战挑衅、无理抬杠。" +
	"只输出一个 JSON 对象, 不要任何多余文字。"

func judgeUserPrompt(criterion, comment string) string {
	if strings.TrimSpace(criterion) == "" {
		criterion = "对回答作者不友善: 辱骂、人身攻击、阴阳怪气、引战挑衅、无理抬杠"
	}
	return "[屏蔽标准]\n" + criterion + "\n\n[一条评论]\n" + comment + "\n\n" +
		"请输出 JSON: {\"block\": true 或 false, \"confidence\": 0到1的小数, \"reason\": \"不超过30字的理由\"}"
}

type judgeResult struct {
	Flag       bool
	Confidence float64
	Reason     string
}

func judgeComment(c *Config, apiKey, criterion, commentText string) (judgeResult, error) {
	txt := commentText
	if r := []rune(txt); len(r) > c.LLM.MaxChars {
		txt = string(r[:c.LLM.MaxChars])
	}
	reqBody := map[string]any{
		"model":       c.LLM.Model,
		"temperature": c.LLM.Temperature,
		"messages": []map[string]string{
			{"role": "system", "content": judgeSystem},
			{"role": "user", "content": judgeUserPrompt(criterion, txt)},
		},
	}
	bs, _ := json.Marshal(reqBody)
	endpoint := strings.TrimRight(c.LLM.BaseURL, "/") + "/chat/completions"
	status, body, err := llmPost(c, endpoint, apiKey, bs)
	if err != nil {
		return judgeResult{}, err
	}
	if status != 200 {
		return judgeResult{}, fmt.Errorf("LLM HTTP %d: %s", status, truncate(string(body), 200))
	}
	var cr struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &cr); err != nil || len(cr.Choices) == 0 {
		return judgeResult{}, fmt.Errorf("LLM 响应异常")
	}
	content := strings.TrimSpace(reFence.ReplaceAllString(strings.TrimSpace(cr.Choices[0].Message.Content), ""))
	var raw map[string]any
	if json.Unmarshal([]byte(content), &raw) != nil {
		m := reJSONObj.FindString(content)
		if m == "" || json.Unmarshal([]byte(m), &raw) != nil {
			return judgeResult{}, fmt.Errorf("无法解析: %s", truncate(content, 120))
		}
	}
	res := judgeResult{}
	switch v := raw["block"].(type) {
	case bool:
		res.Flag = v
	case string:
		res.Flag = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	switch v := raw["confidence"].(type) {
	case float64:
		res.Confidence = v
	case string:
		res.Confidence, _ = strconv.ParseFloat(strings.TrimSpace(v), 64)
	}
	if res.Confidence < 0 {
		res.Confidence = 0
	} else if res.Confidence > 1 {
		res.Confidence = 1
	}
	res.Reason, _ = raw["reason"].(string)
	if len([]rune(res.Reason)) > 60 {
		res.Reason = string([]rune(res.Reason)[:60])
	}
	return res, nil
}
