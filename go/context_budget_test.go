package main

import (
	"strings"
	"testing"
)

func TestContextBudgetPrioritizesCurrentThreadThenMemoryThenColdHistory(t *testing.T) {
	sections := []contextBudgetSection{
		{Priority: 1, Items: []string{"冷历史/低优先级材料很长很长很长很长"}},
		{Priority: 2, Items: []string{"热点记忆：喜欢茉莉花茶"}},
		{Priority: 3, KeepNewest: true, Items: []string{"实时/旧消息", "实时/当前线程最新消息"}},
	}
	selected := assembleContextWithinBudget(sections, 30)
	joined := strings.Join(selected, "\n")
	if !strings.Contains(joined, "当前线程最新消息") {
		t.Fatalf("current thread was not retained: %q", joined)
	}
	if !strings.Contains(joined, "热点记忆") {
		t.Fatalf("hot memory was not retained: %q", joined)
	}
	if strings.Contains(joined, "低优先级") {
		t.Fatalf("cold history displaced higher-priority context: %q", joined)
	}
}

func TestContextBudgetKeepsNewestThreadItemsFirst(t *testing.T) {
	selected := assembleContextWithinBudget([]contextBudgetSection{{
		Priority: 3, KeepNewest: true, Items: []string{"第一条很长的消息", "第二条最新消息"},
	}}, approximateContextTokens("第二条最新消息"))
	if len(selected) != 1 || selected[0] != "第二条最新消息" {
		t.Fatalf("newest context selection = %#v", selected)
	}
}
