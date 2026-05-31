package main

import (
	"encoding/json"
	"os"
	"strings"
)

// CheckSession 用已保存的 cookie 调 /api/v4/me 验证登录态。
func CheckSession(c *Config) map[string]any {
	if _, err := os.Stat(c.CookieFile); err != nil {
		return map[string]any{"valid": false, "reason": "尚未设置 cookie"}
	}
	cookie, err := c.CookieStr()
	if err != nil {
		return map[string]any{"valid": false, "reason": "读取 cookie 失败"}
	}
	if !strings.Contains(cookie, "z_c0=") {
		return map[string]any{"valid": false, "reason": "cookie 缺少 z_c0 登录态"}
	}
	status, body, err := zGet(c, "https://www.zhihu.com/api/v4/me", cookie, "https://www.zhihu.com/")
	if err != nil {
		return map[string]any{"valid": false, "reason": "请求失败: " + err.Error()}
	}
	if status == 200 {
		var me struct {
			Name     string `json:"name"`
			URLToken string `json:"url_token"`
			Headline string `json:"headline"`
		}
		if json.Unmarshal(body, &me) == nil && me.Name != "" {
			return map[string]any{"valid": true, "name": me.Name, "url_token": me.URLToken, "headline": me.Headline}
		}
	}
	return map[string]any{"valid": false, "reason": "登录态失效 (HTTP " + itoa(status) + ")"}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
