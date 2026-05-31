package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type LLMConfig struct {
	BaseURL     string  `json:"base_url"`
	Model       string  `json:"model"`
	APIKeyEnv   string  `json:"api_key_env"`
	APIKey      string  `json:"api_key"` // 运行时注入 (Web 表单), 不落盘
	Temperature float64 `json:"temperature"`
	MaxChars    int     `json:"max_chars"`
	Concurrency int     `json:"concurrency"`
	Timeout     int     `json:"timeout"`
}

type StanceConfig struct {
	Threshold    float64  `json:"threshold"`
	BlockStances []string `json:"block_stances"`
}

type RateLimit struct {
	BlockMinDelay   float64 `json:"block_min_delay"`
	BlockMaxDelay   float64 `json:"block_max_delay"`
	BlockBatchLimit int     `json:"block_batch_limit"`
}

type Config struct {
	CookieFile string       `json:"cookie_file"`
	Question   string       `json:"question"`
	DataDir    string       `json:"data_dir"`
	UserAgent  string       `json:"user_agent"`
	CrawlDelay []float64    `json:"crawl_delay"`
	LLM        LLMConfig    `json:"llm"`
	Stance     StanceConfig `json:"stance"`
	RateLimit  RateLimit    `json:"rate_limit"`
	Proxy      string       `json:"proxy"`
}

func DefaultConfig() *Config {
	return &Config{
		CookieFile: "secret_cookies.txt",
		Question:   "1991274395218491186",
		DataDir:    "data",
		UserAgent:  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36 Edg/147.0.0.0",
		CrawlDelay: []float64{0.6, 1.2},
		LLM: LLMConfig{
			BaseURL:     "https://api.openai.com/v1",
			Model:       "gpt-4o-mini",
			APIKeyEnv:   "LLM_API_KEY",
			Temperature: 0.0,
			MaxChars:    1800,
			Concurrency: 6,
			Timeout:     90,
		},
		Stance:    StanceConfig{Threshold: 0.8, BlockStances: []string{"oppose"}},
		RateLimit: RateLimit{BlockMinDelay: 3.0, BlockMaxDelay: 8.0, BlockBatchLimit: 50},
		Proxy:     "",
	}
}

func LoadConfig(path string) *Config {
	c := DefaultConfig()
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, c) // 仅覆盖出现的字段, 其余保留默认
	}
	return c
}

func (c *Config) CookieStr() (string, error) {
	b, err := os.ReadFile(c.CookieFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func XsrfFromCookie(cookie string) string {
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "_xsrf=") {
			return strings.TrimPrefix(part, "_xsrf=")
		}
	}
	return ""
}

func (c *Config) LLMAPIKey() string {
	if strings.TrimSpace(c.LLM.APIKey) != "" {
		return strings.TrimSpace(c.LLM.APIKey)
	}
	return strings.TrimSpace(os.Getenv(c.LLM.APIKeyEnv))
}

func (c *Config) DataPath(name string) string {
	_ = os.MkdirAll(c.DataDir, 0o755)
	return filepath.Join(c.DataDir, name)
}
