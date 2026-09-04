package textutil

import (
	"strings"
	"unicode"
)

var searchNoiseHanBigrams = map[string]struct{}{
	"查询": {}, "查找": {}, "搜索": {}, "查看": {}, "获取": {},
	"打开": {}, "开启": {}, "关闭": {}, "取消": {}, "添加": {},
	"新增": {}, "删除": {}, "更新": {}, "修改": {}, "设置": {},
	"管理": {}, "提交": {}, "完成": {}, "恢复": {}, "列出": {},
	"列表": {},
	"检查": {}, "请帮": {}, "帮我": {}, "我想": {}, "想要": {},
	"能否": {}, "是否": {}, "可以": {}, "一下": {}, "怎么": {},
	"如何": {}, "哪些": {}, "什么": {}, "当前": {}, "现在": {},
	"目前": {},
}

// MeaningfulSearchTokenMatches counts distinct semantic overlaps between one
// query token and a lower-cased search corpus. Han text is compared as
// bigrams because natural Chinese queries usually have no word boundaries;
// generic request verbs are ignored so they cannot select an unrelated tool.
func MeaningfulSearchTokenMatches(value, corpus string) int {
	value = strings.ToLower(strings.TrimSpace(value))
	corpus = strings.ToLower(corpus)
	if value == "" || corpus == "" {
		return 0
	}
	if !containsHan(value) {
		letters := 0
		for _, r := range value {
			if unicode.IsLetter(r) {
				letters++
			}
		}
		if letters < 2 {
			return 0
		}
		if strings.Contains(corpus, value) {
			return 1
		}
		return 0
	}
	runes := make([]rune, 0, len(value))
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			runes = append(runes, r)
		} else {
			runes = append(runes, 0)
		}
	}
	seen := make(map[string]struct{})
	matches := 0
	for index := 1; index < len(runes); index++ {
		if runes[index-1] == 0 || runes[index] == 0 {
			continue
		}
		bigram := string(runes[index-1 : index+1])
		if _, noise := searchNoiseHanBigrams[bigram]; noise {
			continue
		}
		if _, found := seen[bigram]; found {
			continue
		}
		seen[bigram] = struct{}{}
		if strings.Contains(corpus, bigram) {
			matches++
		}
	}
	return matches
}

func containsHan(value string) bool {
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}
