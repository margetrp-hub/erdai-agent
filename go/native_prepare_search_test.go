package main

import "testing"

func TestExplicitWebSearchIntentCatchesNaturalChineseRequests(t *testing.T) {
	for _, message := range []string{
		"豆包，帮我去找一下今天的AI新鲜事",
		"帮我查查这个人物的出处",
		"网上看看最近发生了什么",
	} {
		if !explicitWebSearchIntent(message) {
			t.Fatalf("search intent not detected: %q", message)
		}
	}
	if inferNativeLane("帮我去找一下今天的AI新鲜事", false, false) != "search" {
		t.Fatal("natural search request did not enter search lane")
	}
}

func TestSearchIntentGateFiftyScenarios(t *testing.T) {
	positive := []string{
		"帮我搜索一下今天的 AI 新闻", "查一下 Grok 4.5 最新价格", "网上看看这个项目", "帮我找官方文档",
		"今天的 AI 新鲜事", "最近发生了什么", "有没有新的 Codex 版本", "给我查这个人物出处",
		"帮我查目前的汇率", "找资料说明 MCP", "search latest xAI news", "look up the current release",
		"这个人是谁？", "它的来源是什么？", "官网现在写了什么？", "价格是多少？", "目前谁是 CEO？",
		"什么时候发布的？", "哪家公司做的？", "最新版本是什么？", "请搜索项目许可证", "找一下今天的天气",
		"搜一下官方公告", "帮我去找论文原文", "网上看看最近更新",
	}
	negative := []string{
		"Edpuzzle 是教育科技平台", "这是一张猫猫表情包", "我今天有点累", "豆包在吗", "数据库",
		"资料挺多的", "新闻看得头疼", "最近很忙", "现在不想说话", "价格不重要", "官网设计不错",
		"来源写在页脚", "这是 Grok 4.5", "AI 真有意思", "帮我画一张图", "生成一个视频", "记住这句话",
		"总结这个附件", "给我做个 PPT", "/渠道", "你好", "哈哈哈哈", "这个人很眼熟", "我不知道是谁",
		"晚安",
	}
	if len(positive)+len(negative) != 50 {
		t.Fatalf("scenario count = %d", len(positive)+len(negative))
	}
	for _, message := range positive {
		if !explicitWebSearchIntent(message) {
			t.Errorf("positive intent missed: %q", message)
		}
	}
	for _, message := range negative {
		if explicitWebSearchIntent(message) {
			t.Errorf("negative intent searched: %q", message)
		}
	}
}


func TestSocialQuestionsStayInConversation(t *testing.T) {
	for _, message := range []string{"你是谁", "你是谁呀？", "在吗", "你在干嘛?", "认识我吗"} {
		if explicitWebSearchIntent(message) {
			t.Fatalf("%q should not trigger web search", message)
		}
		if inferNativeLane(message, false, false) != "chat" {
			t.Fatalf("%q should remain chat lane", message)
		}
	}
}
