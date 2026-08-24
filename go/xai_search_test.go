package main

import (
	"strings"
	"testing"
)

func TestParseXAIResponsesCollectsSummaryAndCitations(t *testing.T) {
	var response xaiResponsesResponse
	item := struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Content []struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Annotations []struct {
				Type  string `json:"type"`
				URL   string `json:"url"`
				Title string `json:"title"`
			} `json:"annotations"`
		} `json:"content"`
	}{}
	item.Type = "message"
	content := struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Annotations []struct {
			Type  string `json:"type"`
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"annotations"`
	}{Type: "output_text", Text: "xAI 发布了新版本。"}
	annotation := struct {
		Type  string `json:"type"`
		URL   string `json:"url"`
		Title string `json:"title"`
	}{Type: "url_citation", URL: "https://x.ai/news", Title: "xAI news"}
	content.Annotations = append(content.Annotations, annotation, annotation)
	item.Content = append(item.Content, content)
	response.Output = append(response.Output, item)
	text, sources := parseXAIResponses(response)
	if text != "xAI 发布了新版本。" || len(sources) != 1 || sources[0].URL != "https://x.ai/news" {
		t.Fatalf("parsed response = %q %+v", text, sources)
	}
}

func TestSearchResultRelevantRejectsWrongEntity(t *testing.T) {
	if searchResultRelevant("Edpuzzle 是什么", "这是一个天气应用。", []searchSource{{Title: "Weather"}}) {
		t.Fatal("unrelated search result was accepted")
	}
	if !searchResultRelevant("Edpuzzle 是什么", "Edpuzzle 是互动视频教学平台。", nil) {
		t.Fatal("relevant search result was rejected")
	}
}

func TestCompactSearchSystemPromptKeepsPersonaTail(t *testing.T) {
	prompt := "discard-head:" + strings.Repeat("x", 1900) + ":persona-tail"
	compact := compactSearchSystemPrompt(prompt)
	if len([]rune(compact)) > 1800 {
		t.Fatalf("compact prompt length = %d", len([]rune(compact)))
	}
	if !strings.Contains(compact, "persona-tail") || strings.Contains(compact, "discard-head") {
		t.Fatalf("compact prompt = %q", compact)
	}
}

func TestDecodeXAIResponsesStreamIgnoresReasoningAndKeepsOutput(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.reasoning_summary_text.delta","delta":"internal reasoning"}`,
		`data: {"type":"response.output_text.delta","delta":"Direct conclusion."}`,
		`data: {"type":"response.output_text.annotation.added","annotation":{"url":"https://x.ai/news","title":"xAI news"}}`,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":12,"output_tokens":4}}}`,
	}, "\n\n")
	var response xaiResponsesResponse
	if err := decodeXAIResponsesStream(strings.NewReader(stream), &response); err != nil {
		t.Fatal(err)
	}
	text, sources := parseXAIResponses(response)
	if text != "Direct conclusion." || strings.Contains(text, "reasoning") {
		t.Fatalf("stream text = %q", text)
	}
	if len(sources) != 1 || sources[0].URL != "https://x.ai/news" {
		t.Fatalf("stream sources = %+v", sources)
	}
}

func TestCompleteSearchStreamSentenceRejectsFragments(t *testing.T) {
	if completeSearchStreamSentence("unfinished fragment") != "" {
		t.Fatal("unfinished stream fragment was accepted")
	}
	if got := completeSearchStreamSentence("This is a complete result. trailing"); got != "This is a complete result." {
		t.Fatalf("complete sentence = %q", got)
	}
}
