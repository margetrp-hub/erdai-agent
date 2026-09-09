package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderResponseNegotiatesRequestedFormat(t *testing.T) {
	for _, test := range []struct {
		name   string
		input  any
		accept string
	}{
		{"default-json", map[string]any{"model": "test"}, "application/json"},
		{"explicit-json", map[string]any{"stream": false}, "application/json"},
		{"explicit-stream", map[string]any{"stream": true}, "text/event-stream"},
		{"typed-stream", struct {
			Stream bool `json:"stream"`
		}{true}, "text/event-stream"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Accept") != test.accept || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Error("request format negotiation or authentication changed")
				}
				writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			}))
			defer server.Close()
			runtime := &AgentRuntime{client: server.Client()}
			var result map[string]bool
			if err := runtime.postProviderJSON(t.Context(), server.URL, "test-key", test.input, &result); err != nil || !result["ok"] {
				t.Fatalf("negotiated response failed: result=%v err=%v", result, err)
			}
		})
	}
}

func TestProviderResponsePrimaryChatKeepsDefaultReasoning(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if _, found := payload["reasoning_effort"]; found {
			t.Error("primary chat reasoning was overridden by a latency optimization")
		}
		writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": "在。"}}}})
	}))
	defer server.Close()
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	runtime.client = server.Client()
	reply, err := runtime.runAgentLoopWithTargets(t.Context(), runRecord{}, "在吗", "自然接话。",
		[]runtimeProviderTarget{{EndpointID: "chat", Model: "grok-4.5", APIBase: server.URL}},
		runtimeToolPolicy{Authority: "member"}, runtimeMessagePolicy{})
	if err != nil || reply.Text != "在。" {
		t.Fatalf("primary chat changed: reply=%+v err=%v", reply, err)
	}
}

func TestProviderResponseDecodesJSONMislabeledAsSSE(t *testing.T) {
	for _, test := range []struct {
		name    string
		padding string
	}{
		{"no-padding", ""},
		{"leading-whitespace", " \r\n\t"},
		{"buffered-whitespace", strings.Repeat(" ", 8192)},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				_, _ = w.Write([]byte(test.padding + `{"choices":[{"message":{"role":"assistant","content":"{\"action\":\"reply\"}"}}],"usage":{"prompt_tokens":9,"completion_tokens":4}}`))
			}))
			defer server.Close()
			runtime := &AgentRuntime{client: server.Client()}
			var result chatCompletion
			if err := runtime.postProviderJSON(t.Context(), server.URL, "test-key", map[string]any{"stream": false}, &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Choices) != 1 || result.Choices[0].Message.Content != `{"action":"reply"}` || result.Usage.PromptTokens != 9 || result.Usage.CompletionTokens != 4 {
				t.Fatalf("mislabeled JSON completion was lost: %+v", result)
			}
		})
	}
}

func TestProviderResponseKeepsTrueChatStreamsAndRejectsMissingChoices(t *testing.T) {
	for _, test := range []struct {
		name      string
		body      string
		wantText  string
		wantTools int
		wantError bool
	}{
		{"text-and-usage", ": keepalive\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4}}\n\ndata: [DONE]\n", "hello", 0, false},
		{"tool-deltas", strings.Join([]string{
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"test\"}"}}]}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`data: [DONE]`,
		}, "\n\n"), "", 1, false},
		{"empty-finish-delta", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n", "", 0, false},
		{"comment-only", ": keepalive\n\n", "", 0, true},
		{"usage-only", "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4}}\n\ndata: [DONE]\n", "", 0, true},
		{"unknown-event-only", "event: message\ndata: {\"event\":\"unknown\"}\n\n", "", 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			runtime := &AgentRuntime{client: server.Client()}
			var result chatCompletion
			err := runtime.postProviderJSON(t.Context(), server.URL, "test-key", map[string]bool{"stream": true}, &result)
			if test.wantError {
				if err == nil || len(result.Choices) != 0 {
					t.Fatalf("missing completion chunks fabricated a choice: %+v err=%v", result, err)
				}
				return
			}
			if err != nil || len(result.Choices) != 1 || result.Choices[0].Message.Content != test.wantText || len(result.Choices[0].Message.ToolCalls) != test.wantTools {
				t.Fatalf("valid stream changed: %+v err=%v", result, err)
			}
			if test.wantTools == 1 {
				call := result.Choices[0].Message.ToolCalls[0]
				if call.ID != "call-1" || call.Function.Name != "lookup" || call.Function.Arguments != `{"q":"test"}` {
					t.Fatalf("tool delta assembly changed: %+v", call)
				}
			}
			if test.name == "text-and-usage" && (result.Usage.PromptTokens != 9 || result.Usage.CompletionTokens != 4) {
				t.Fatal("stream usage was lost")
			}
		})
	}
}

func TestProviderResponseSupportsXAIJSONAndTrueStreams(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "mislabeled-json", true: "true-sse"}[stream], func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				body := `{"output":[{"type":"message","content":[{"type":"output_text","text":"Direct conclusion.","annotations":[{"type":"url_citation","url":"https://example.test/news","title":"News"}]}]}],"usage":{"input_tokens":12,"output_tokens":4}}`
				if stream {
					body = strings.Join([]string{
						`data: {"type":"response.reasoning_summary_text.delta","delta":"private reasoning"}`,
						`data: {"type":"response.output_text.delta","delta":"Direct conclusion."}`,
						`data: {"type":"response.output_text.annotation.added","annotation":{"url":"https://example.test/news","title":"News"}}`,
						`data: {"type":"response.completed","response":{"usage":{"input_tokens":12,"output_tokens":4}}}`,
					}, "\n\n")
				}
				_, _ = w.Write([]byte(" \r\n" + body))
			}))
			defer server.Close()
			runtime := &AgentRuntime{client: server.Client()}
			var result xaiResponsesResponse
			if err := runtime.postProviderJSON(t.Context(), server.URL, "test-key", map[string]bool{"stream": stream}, &result); err != nil {
				t.Fatal(err)
			}
			text, sources := parseXAIResponses(result)
			if text != "Direct conclusion." || len(sources) != 1 || sources[0].URL != "https://example.test/news" || result.Usage.InputTokens != 12 || result.Usage.OutputTokens != 4 {
				t.Fatalf("XAI content, citations or usage changed: text=%q sources=%+v usage=%+v", text, sources, result.Usage)
			}
		})
	}
}

func TestProviderResponseMislabeledJSONRemainsBoundedAndValid(t *testing.T) {
	for _, test := range []struct {
		name      string
		body      string
		wantError bool
	}{
		{"array", " \n[1,2]", false},
		{"malformed-object", " \n{invalid", true},
		{"empty", " \r\n\t", true},
		{"over-limit-whitespace", strings.Repeat(" ", maxToolBody) + "[1,2]", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			runtime := &AgentRuntime{client: server.Client()}
			var result json.RawMessage
			err := runtime.postProviderJSON(context.Background(), server.URL, "test-key", map[string]bool{"stream": false}, &result)
			if (err != nil) != test.wantError {
				t.Fatalf("bounded JSON result=%s err=%v", result, err)
			}
			if !test.wantError && string(result) != "[1,2]" {
				t.Fatalf("JSON array changed: %s", result)
			}
		})
	}
}
