package app

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// loadSMTPEnvFile 读取 KEY=VALUE 形式的 SMTP 配置文件（# 开头注释与空行跳过，
// 值允许成对单/双引号包裹）。文件不存在返回 (nil, nil)——SMTP 是可选配置，
// 缺失时日报与失败告警不启用、服务照常运行；文件存在但格式非法则报错
// （配置错误应当暴露，不静默降级）。
func loadSMTPEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 SMTP 配置 %s 失败: %w", path, err)
	}

	out := make(map[string]string)
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("SMTP 配置 %s 第 %d 行格式非法（应为 KEY=VALUE）: %q", path, i+1, raw)
		}
		out[strings.TrimSpace(key)] = unquote(strings.TrimSpace(value))
	}
	return out, nil
}

// unquote 去掉成对的首尾单引号或双引号；不成对则原样返回。
func unquote(s string) string {
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		return s[1 : len(s)-1]
	}
	return s
}
