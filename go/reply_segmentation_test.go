package main

import (
	"strings"
	"testing"
)

func TestSplitReplyTextUsesCompleteNaturalSentences(t *testing.T) {
	enabled := true
	policy := runtimeMessagePolicy{
		SegmentedReplyEnabled: &enabled,
		SegmentMinChars:       8,
		SegmentMaxChars:       20,
		MaxReplySegments:      2,
	}
	text := "先别急，这个问题能修。把报错发来，我继续看。"
	segments := splitReplyText(text, policy, false)
	want := []string{"先别急，这个问题能修。", "把报错发来，我继续看。"}
	if len(segments) != len(want) {
		t.Fatalf("segments = %#v", segments)
	}
	for index := range want {
		if segments[index] != want[index] {
			t.Fatalf("segment %d = %q, want %q", index, segments[index], want[index])
		}
	}
	if strings.Join(segments, "") != text {
		t.Fatal("segmentation changed or duplicated the reply")
	}
}

func TestSplitReplyTextKeepsOverlongSentenceWhole(t *testing.T) {
	text := "这是一句没有自然边界而且明显超过二十个汉字的完整说明"
	segments := splitReplyText(text, runtimeMessagePolicy{SegmentMaxChars: 20}, false)
	if len(segments) != 1 || segments[0] != text {
		t.Fatalf("overlong sentence was hard-truncated: %#v", segments)
	}
}

func TestTrimReplyAtNaturalBoundaryDoesNotCutIncompleteTail(t *testing.T) {
	text := "第一句完整。第二句也完整。第三句还没有说完但很长很长很长。"
	trimmed := trimReplyAtNaturalBoundary(text, 18)
	if trimmed != "第一句完整。第二句也完整。" {
		t.Fatalf("trimmed reply = %q", trimmed)
	}
}

func TestSplitReplyTextCanBeDisabledByCorePolicy(t *testing.T) {
	disabled := false
	text := "第一句说完。第二句也说完。"
	segments := splitReplyText(text, runtimeMessagePolicy{
		SegmentedReplyEnabled: &disabled,
		SegmentMaxChars:       10,
	}, false)
	if len(segments) != 1 || segments[0] != text {
		t.Fatalf("disabled segmentation changed reply: %#v", segments)
	}
}

func TestSplitReplyTextKeepsClosingQuoteWithSentence(t *testing.T) {
	enabled := true
	text := "他说：“这版可以。”我看也是。"
	segments := splitReplyText(text, runtimeMessagePolicy{SegmentedReplyEnabled: &enabled, SegmentMaxChars: 10, MaxReplySegments: 2}, false)
	if len(segments) != 2 || segments[0] != "他说：“这版可以。”" || segments[1] != "我看也是。" {
		t.Fatalf("quoted sentence split incorrectly: %#v", segments)
	}
}

func TestSplitReplyTextPreservesCodeURLsAndToolResults(t *testing.T) {
	policy := runtimeMessagePolicy{SegmentMaxChars: 10, MaxReplySegments: 2}
	for _, test := range []struct {
		name     string
		text     string
		preserve bool
	}{
		{name: "code", text: "用 `go test ./...` 验证。然后看结果。"},
		{name: "url", text: "文档：https://example.com/docs。请先看原文。"},
		{name: "tool result", text: "第一项正常。第二项异常。", preserve: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			segments := splitReplyText(test.text, policy, test.preserve)
			if len(segments) != 1 || segments[0] != test.text {
				t.Fatalf("formal reply was segmented: %#v", segments)
			}
		})
	}
}

func TestSplitReplyTextLimitsMessagesWithoutLosingSentences(t *testing.T) {
	enabled := true
	text := "第一句说完。第二句也说完。第三句仍然完整。"
	segments := splitReplyText(text, runtimeMessagePolicy{SegmentedReplyEnabled: &enabled, SegmentMaxChars: 8, MaxReplySegments: 2}, false)
	if len(segments) != 2 || strings.Join(segments, "") != text {
		t.Fatalf("bounded segments changed reply: %#v", segments)
	}
	if segments[1] != "第二句也说完。第三句仍然完整。" {
		t.Fatalf("tail sentences were not kept intact: %#v", segments)
	}
}

func TestSplitReplyTextKeepsShortDefaultReplyTogether(t *testing.T) {
	text := "这句说完。下一句也说完。"
	segments := splitReplyText(text, runtimeMessagePolicy{}, false)
	if len(segments) != 1 || segments[0] != text {
		t.Fatalf("default reply was segmented: %#v", segments)
	}
}
