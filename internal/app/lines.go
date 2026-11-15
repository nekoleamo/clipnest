package app

import "strings"

// joinLines 把字符串列表拼成多行文本。
func joinLines(list []string) string { return strings.Join(list, "\n") }

// splitLines 把多行文本拆成列表（去空行与首尾空白）。
func splitLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
