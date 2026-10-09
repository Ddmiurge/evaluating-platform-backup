package handler

import "strings"

// uniqueNonEmptyStrings 保序去重并剔除空白项（合并自 uniqueStringsPreserve /
// normalizeGrantStringList / normalizeMCPRefs 三份逐字相同的实现）。
func uniqueNonEmptyStrings(items []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

// firstNonEmpty 返回第一个非空白字符串（合并自三份同名同体实现）。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
