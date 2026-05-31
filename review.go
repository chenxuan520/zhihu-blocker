package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

var blocklistHeader = []string{"confirmed", "url_token", "name", "stance", "confidence", "voteup", "reason", "answer_url", "excerpt"}

func loadStanceRows(c *Config) (map[string]string, []*Answer, error) {
	f, err := os.Open(c.DataPath("answers_stance.jsonl"))
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	meta := map[string]string{}
	var rows []*Answer
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.Contains(line, "\"_meta\"") {
			var m struct {
				Meta map[string]string `json:"_meta"`
			}
			if json.Unmarshal([]byte(line), &m) == nil {
				meta = m.Meta
			}
			continue
		}
		var a Answer
		if json.Unmarshal([]byte(line), &a) == nil {
			rows = append(rows, &a)
		}
	}
	return meta, rows, sc.Err()
}

func BuildBlocklist(c *Config) error {
	meta, rows, err := loadStanceRows(c)
	if err != nil {
		return fmt.Errorf("读取 answers_stance.jsonl 失败: %w", err)
	}
	blockSet := map[string]bool{}
	for _, s := range c.Stance.BlockStances {
		blockSet[s] = true
	}
	byAuthor := map[string]*Answer{}
	for _, r := range rows {
		if !blockSet[r.Stance] || r.StanceConfidence < c.Stance.Threshold {
			continue
		}
		if r.Author.IsAnonymous || r.Author.URLToken == "" {
			continue
		}
		t := r.Author.URLToken
		if cur, ok := byAuthor[t]; !ok || r.VoteupCount > cur.VoteupCount {
			byAuthor[t] = r
		}
	}
	final := make([]*Answer, 0, len(byAuthor))
	for _, r := range byAuthor {
		final = append(final, r)
	}
	sort.SliceStable(final, func(i, j int) bool {
		if final[i].StanceConfidence != final[j].StanceConfidence {
			return final[i].StanceConfidence > final[j].StanceConfidence
		}
		return final[i].VoteupCount > final[j].VoteupCount
	})

	f, err := os.Create(c.DataPath("blocklist.csv"))
	if err != nil {
		return err
	}
	defer f.Close()
	cw := csv.NewWriter(f)
	defer cw.Flush()
	cw.Write(blocklistHeader)
	for _, r := range final {
		excerpt := strings.ReplaceAll(r.ContentText, "\n", " ")
		if len([]rune(excerpt)) > 60 {
			excerpt = string([]rune(excerpt)[:60])
		}
		cw.Write([]string{
			"N", r.Author.URLToken, r.Author.Name, r.Stance,
			fmt.Sprintf("%.2f", r.StanceConfidence), strconv.Itoa(r.VoteupCount),
			r.StanceReason, r.AnswerURL, excerpt,
		})
	}
	jprintf("[review] 观点='%s' 引擎=%s\n", meta["opinion"], meta["engine"])
	jprintf("[review] 候选拉黑 %d 人 (阈值≥%.2f, 立场∈%v) -> %s\n", len(final), c.Stance.Threshold, c.Stance.BlockStances, c.DataPath("blocklist.csv"))
	return nil
}

func readCSV(c *Config) ([]map[string]string, error) {
	f, err := os.Open(c.DataPath("blocklist.csv"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	head := rows[0]
	var out []map[string]string
	for _, r := range rows[1:] {
		m := map[string]string{}
		for i, h := range head {
			if i < len(r) {
				m[h] = r[i]
			}
		}
		out = append(out, m)
	}
	return out, nil
}

func ReadCandidates(c *Config) []map[string]string {
	rows, _ := readCSV(c)
	if rows == nil {
		return []map[string]string{}
	}
	return rows
}

func LoadConfirmed(c *Config) []map[string]string {
	rows, _ := readCSV(c)
	var out []map[string]string
	for _, r := range rows {
		switch strings.ToLower(strings.TrimSpace(r["confirmed"])) {
		case "y", "yes", "true", "1", "是":
			out = append(out, r)
		}
	}
	return out
}
