package main

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Go 的 Transport 在未手动设置 Accept-Encoding 时会自动请求并透明解压 gzip。
func newClient(c *Config, timeout time.Duration) *http.Client {
	tr := &http.Transport{
		MaxIdleConns:    20,
		IdleConnTimeout: 60 * time.Second,
	}
	if c.Proxy != "" {
		if u, err := url.Parse(c.Proxy); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

// 知乎 GET (JSON 读接口)
func zGet(c *Config, rawurl, cookie, referer string) (int, []byte, error) {
	req, err := http.NewRequest("GET", rawurl, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("user-agent", c.UserAgent)
	req.Header.Set("accept", "*/*")
	req.Header.Set("accept-language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("cookie", cookie)
	req.Header.Set("x-requested-with", "fetch")
	req.Header.Set("x-api-version", "3.0.91")
	if referer != "" {
		req.Header.Set("referer", referer)
	}
	resp, err := newClient(c, 25*time.Second).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}

// 知乎写操作 (拉黑 POST / 取消拉黑 DELETE)
func zWrite(c *Config, method, rawurl, cookie, xsrf, referer string, body []byte) (int, []byte, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, rawurl, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("user-agent", c.UserAgent)
	req.Header.Set("accept", "*/*")
	req.Header.Set("cookie", cookie)
	req.Header.Set("x-xsrftoken", xsrf)
	req.Header.Set("x-requested-with", "fetch")
	if referer != "" {
		req.Header.Set("referer", referer)
	}
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	resp, err := newClient(c, 25*time.Second).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}

// LLM POST (OpenAI 兼容)
func llmPost(c *Config, rawurl, apiKey string, body []byte) (int, []byte, error) {
	req, err := http.NewRequest("POST", rawurl, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+apiKey)
	resp, err := newClient(c, time.Duration(c.LLM.Timeout)*time.Second).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}
