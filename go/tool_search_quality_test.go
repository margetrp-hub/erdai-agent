package main

import (
	"strings"
	"testing"
)

func TestHumanizeSearchReplyRemovesReportOpeners(t *testing.T) {
	for _, input := range []string{
		"目前能确认的是：OpenAI 在本周发布了更新。",
		"资料显示：这个项目由社区维护。",
		"根据搜索结果，价格已经调整。",
	} {
		got := humanizeSearchReply(input)
		if strings.Contains(got, "资料显示") || strings.Contains(got, "搜索结果") || strings.Contains(got, "目前能确认") {
			t.Fatalf("search report opener remained: %q -> %q", input, got)
		}
	}
}
