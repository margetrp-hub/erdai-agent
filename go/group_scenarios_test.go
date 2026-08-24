package main

import (
	"fmt"
	"testing"
)

func TestLowValueGroupMessageDetection(t *testing.T) {
	for _, message := range []string{"[表情]", "[图片]", "😂", "…"} {
		if !isLowValueGroupMessage(message) {
			t.Fatalf("low value message not filtered: %q", message)
		}
	}
	for _, message := range []string{"今天谁值班", "豆包帮我查一下", "这个梗什么意思"} {
		if isLowValueGroupMessage(message) {
			t.Fatalf("conversational message filtered: %q", message)
		}
	}
}

func TestLowValueGroupMessagePolicyIsConfigurable(t *testing.T) {
	if !isLowValueGroupMessageWithPolicy("[自定义贴图]", []string{"[自定义贴图]"}, 2) {
		t.Fatal("configured marker should be filtered")
	}
	if !isLowValueGroupMessageWithPolicy("好", nil, 0) {
		t.Fatal("zero minimum should use the default minimum")
	}
	if isLowValueGroupMessageWithPolicy("好", nil, 1) {
		t.Fatal("minimum text chars should be configurable")
	}
}

func TestUnaddressedAttachmentOnlyMessageIsLowValue(t *testing.T) {
	event := transportEvent{}
	event.Message.Attachments = []transportAttachment{{Kind: "image"}}
	message := nativeAttachmentOnlyPrompt(event.Message.Attachments)
	if !isUnaddressedAttachmentOnlyMessage(event, message) {
		t.Fatal("attachment-only group message was eligible for proactive reply")
	}
	if isUnaddressedAttachmentOnlyMessage(event, "What is in this image?") {
		t.Fatal("captioned image was incorrectly filtered")
	}
}

func TestAttachmentRequestDetectionIsTypedAndExplicit(t *testing.T) {
	for _, test := range []struct {
		kind, prompt string
	}{
		{"image", "把头像发来，我帮你优化。"},
		{"file", "把文档发来，我帮你整理。"},
		{"audio", "语音发过来，我听一下。"},
		{"video", "把视频传给我。"},
	} {
		if !assistantRequestsAttachment(test.prompt, []transportAttachment{{Kind: test.kind}}) {
			t.Fatalf("explicit %s request was not recognized", test.kind)
		}
	}
	if !assistantRequestsAttachment("行，把它发过来。", []transportAttachment{{Kind: "file"}}) {
		t.Fatal("generic attachment request was not recognized")
	}
	if assistantRequestsAttachment("我已经看到这张图片了。", []transportAttachment{{Kind: "image"}}) {
		t.Fatal("ordinary image comment was treated as an attachment request")
	}
	if assistantRequestsAttachment("把文件发来。", []transportAttachment{{Kind: "image"}}) {
		t.Fatal("file request incorrectly accepted an image continuation")
	}
}

func TestExplicitImageEditIntent(t *testing.T) {
	for _, message := range []string{"把我的头像优化一下", "让这张照片更清晰", "帮我修一下图"} {
		if !explicitImageEditIntent(message) {
			t.Fatalf("image edit intent not detected: %q", message)
		}
	}
	for _, message := range []string{"看看这是什么", "发一张你的自拍", "这个表情包好玩"} {
		if explicitImageEditIntent(message) {
			t.Fatalf("non-edit message detected as image edit: %q", message)
		}
	}
}

type anonymousGroupScenarioBucket struct {
	name     string
	messages []string
	check    func(string) bool
}

func TestAnonymousGroupScenarioCorpus(t *testing.T) {
	buckets := []anonymousGroupScenarioBucket{
		{
			name: "question",
			messages: []string{
				"这个方案怎么选？", "这两种写法哪个好？", "今晚几点开始？", "有人知道原因吗？", "这个版本能升级吗？",
				"周末去哪比较合适？", "现在还有名额吗？", "这张卡值得买吗？", "要不要先备份？", "谁知道入口在哪？",
			},
			check: func(message string) bool { return assessGroupSocialMessage(message).IsQuestion },
		},
		{
			name: "help",
			messages: []string{
				"帮我看看这个报错", "求助，页面打不开", "这个要怎么做才对", "谁知道怎么恢复文件", "帮忙理一下思路",
				"求助一下配置问题", "怎么解决重复回复", "帮我查一下资料", "谁知道怎么关掉提示", "帮我看看图片内容",
			},
			check: func(message string) bool { return assessGroupSocialMessage(message).Intent == "求助" },
		},
		{
			name: "joke",
			messages: []string{
				"哈哈这也太巧了", "笑死，居然还能这样", "绷不住了你们继续", "这个梗有点东西", "狗头保命，开玩笑的",
				"哈哈我先看戏", "笑死我了真的", "绷不住，节目效果拉满", "又在玩什么梗", "开玩笑，别当真",
			},
			check: func(message string) bool {
				assessment := assessGroupSocialMessage(message)
				return assessment.IsJoke && assessment.Intent == "玩笑"
			},
		},
		{
			name: "hostile",
			messages: []string{
				"滚，别来烦我", "闭嘴吧你", "你就是个废物", "真是个傻逼", "操你别说了",
				"去死吧别回了", "滚远一点", "闭嘴没人问你", "废物还在装", "傻逼东西少说话",
			},
			check: func(message string) bool { return assessGroupSocialMessage(message).IsHostile },
		},
		{
			name: "sad",
			messages: []string{
				"今天真的很难过", "有点伤心，不想说话", "我现在特别想哭", "事情全砸了，快崩溃了", "感觉好委屈",
				"突然有点失落", "最近情绪很抑郁", "难过得睡不着", "这结果让人伤心", "忙完只剩失落",
			},
			check: func(message string) bool { return detectConversationEmotion(message) == "难过" },
		},
		{
			name: "anxious",
			messages: []string{
				"最近有点焦虑", "马上轮到我了好紧张", "外面打雷有点害怕", "我很担心明天的结果", "文件没了怎么办",
				"突然有点慌", "这事快把我急死了", "越想越焦虑", "第一次上台很紧张", "还没消息，挺担心的",
			},
			check: func(message string) bool { return detectConversationEmotion(message) == "焦虑" },
		},
		{
			name: "angry",
			messages: []string{
				"这事真的让我生气", "反复改需求快气死了", "今天堵车烦死", "这个结果太离谱", "看到这操作很无语",
				"越想越火大", "白等一天真生气", "临时取消太离谱", "又坏了，真无语", "一早就被气死",
			},
			check: func(message string) bool { return detectConversationEmotion(message) == "生气" },
		},
		{
			name: "happy",
			messages: []string{
				"今天特别开心", "终于通过了好高兴", "好耶，准时下班", "哈哈这次稳了", "笑死，运气也太好",
				"这个结果太棒了", "收到礼物很开心", "大家都过了好高兴", "好耶，问题解决", "太棒了，一次成功",
			},
			check: func(message string) bool { return detectConversationEmotion(message) == "开心" },
		},
		{
			name: "confused",
			messages: []string{
				"这里我还是不懂", "没明白这段逻辑", "为什么会变成这样", "这是怎么回事", "这句话啥意思",
				"日志完全看不懂", "不懂为什么失败", "还是没明白区别", "怎么回事，又断了", "这个缩写啥意思",
			},
			check: func(message string) bool { return detectConversationEmotion(message) == "困惑" },
		},
		{
			name: "direct-address",
			messages: []string{
				"豆包，在吗", "豆包 帮我看看", "@豆包 过来一下", "豆包，这个怎么弄", "豆包？",
				"豆包！别装睡", "豆包麻烦查一下", "豆包给个意见", "豆包看看图片", "豆包，说句话",
			},
			check: func(message string) bool { return directlyAddressesKeyword(message, []string{"豆包"}) },
		},
		{
			name: "command",
			messages: []string{
				"/渠道", "/雷达", "/帮助", "!status", "!ping",
				"#配置", "#模型", "/图片 一只猫", "/视频 海边", "/忘记 昵称",
			},
			check: func(message string) bool { return startsWithCommand(message, []string{"/", "!", "#"}) },
		},
		{
			name: "low-quality",
			messages: []string{
				"嗯", "哈", "？", "。", "1", "啊", "哦", "...", "！", "~",
			},
			check: func(message string) bool { return !hasConversationalContent(message) },
		},
	}

	seen := map[string]struct{}{}
	total := 0
	for _, bucket := range buckets {
		if len(bucket.messages) != 10 {
			t.Fatalf("bucket %s has %d scenarios, want 10", bucket.name, len(bucket.messages))
		}
		for index, message := range bucket.messages {
			total++
			if _, exists := seen[message]; exists {
				t.Fatalf("duplicate scenario message %q", message)
			}
			seen[message] = struct{}{}
			t.Run(fmt.Sprintf("%s-%02d", bucket.name, index+1), func(t *testing.T) {
				if !bucket.check(message) {
					t.Fatalf("scenario %q did not satisfy %s", message, bucket.name)
				}
			})
		}
	}
	if total != 120 {
		t.Fatalf("scenario corpus has %d cases, want 120", total)
	}
}
