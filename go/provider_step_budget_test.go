package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestModelStepBudgetStartingTarget(t *testing.T) {
	for _, test := range []struct {
		name, model                      string
		timeout, wantAttempt, wantBudget int
	}{
		{"grok45-unchanged", "grok-4.5", 60, 15, 20},
		{"other-model-unchanged", "gpt-6", 60, 15, 20},
		{"alias-is-not-exact", "grok-4.6-latest", 60, 15, 20},
		{"no-explicit-timeout", "grok-4.6", 0, 15, 20},
		{"invalid-timeout", "grok-4.6", -1, 15, 20},
		{"short-explicit-timeout", "grok-4.6", 10, 10, 15},
		{"configured-budget", "grok-4.6", 45, 45, 50},
		{"maximum-budget", "grok-4.6", 60, 60, 65},
		{"cap-excessive-budget", "grok-4.6", 600, 60, 65},
	} {
		t.Run(test.name, func(t *testing.T) {
			targets := []runtimeProviderTarget{
				{Model: test.model, TimeoutSeconds: test.timeout, ProviderRetries: 4},
				{Model: "grok-4.6", TimeoutSeconds: 60, ProviderRetries: 4},
				{Model: "fallback", TimeoutSeconds: 8, ProviderRetries: 4},
				{Model: "excluded", TimeoutSeconds: 120, ProviderRetries: 4},
			}
			before := append([]runtimeProviderTarget(nil), targets...)
			bounded, budget := boundedModelStepTargets("今天忙了一天，陪我聊两句。", false, targets, 0)
			if len(bounded) != 3 || budget != time.Duration(test.wantBudget)*time.Second {
				t.Fatalf("target count=%d budget=%v", len(bounded), budget)
			}
			for index, want := range []int{test.wantAttempt, 15, 8} {
				if bounded[index].TimeoutSeconds != want || bounded[index].ProviderRetries != 0 {
					t.Fatalf("target %d timeout=%d retries=%d", index, bounded[index].TimeoutSeconds, bounded[index].ProviderRetries)
				}
			}
			if !reflect.DeepEqual(before, targets) {
				t.Fatal("source connection targets were changed")
			}
		})
	}
}

func TestModelStepBudgetNonChatAndStartIndex(t *testing.T) {
	targets := []runtimeProviderTarget{{Model: "grok-4.5", TimeoutSeconds: 120, ProviderRetries: 2}, {Model: "grok-4.6", TimeoutSeconds: 45, ProviderRetries: 2}}
	for _, test := range []struct {
		name, message  string
		hasAttachments bool
	}{
		{"search-task", "搜索今天的 AI 新闻", false},
		{"media-task", "帮我生成一段视频", false},
		{"attachment-task", "看看这个", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			bounded, budget := boundedModelStepTargets(test.message, test.hasAttachments, targets, 1)
			if !reflect.DeepEqual(bounded, targets) || budget != 75*time.Second {
				t.Fatal("non-chat target policy or 75-second budget changed")
			}
		})
	}
	bounded, budget := boundedModelStepTargets("在吗", false, targets, 1)
	if budget != 50*time.Second || bounded[0].TimeoutSeconds != 15 || bounded[1].TimeoutSeconds != 45 {
		t.Fatal("budget did not follow the actual starting target")
	}
	_, budget = boundedModelStepTargets("在吗", false, targets, 99)
	if budget != 20*time.Second {
		t.Fatal("invalid starting index did not retain the first target's budget")
	}
}

type modelStepBudgetTransport func(*http.Request) (*http.Response, error)

func (transport modelStepBudgetTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestModelStepBudgetActualLoopAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name, primary, fallback      string
		wantAttempts                 []int
		parentDeadline, cancelParent bool
	}{
		{name: "grok46-start", primary: "grok-4.6", wantAttempts: []int{45}},
		{name: "grok45-to-grok46", primary: "grok-4.5", fallback: "grok-4.6", wantAttempts: []int{15, 15}},
		{name: "grok46-to-grok45", primary: "grok-4.6", fallback: "grok-4.5", wantAttempts: []int{45, 15}},
		{name: "shorter-parent-deadline", primary: "grok-4.6", wantAttempts: []int{1}, parentDeadline: true},
		{name: "parent-cancellation", primary: "grok-4.6", fallback: "grok-4.5", wantAttempts: []int{45}, cancelParent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent := newDormantRuntime(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.parentDeadline {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, time.Second)
				defer deadlineCancel()
			}
			calls := 0
			agent.client = &http.Client{Transport: modelStepBudgetTransport(func(request *http.Request) (*http.Response, error) {
				index := calls
				calls++
				if index >= len(test.wantAttempts) {
					t.Error("unexpected model retry or rewrite")
					return nil, errors.New("unexpected model call")
				}
				deadline, ok := request.Context().Deadline()
				remaining := time.Until(deadline)
				want := time.Duration(test.wantAttempts[index]) * time.Second
				if !ok || remaining <= want-time.Second || remaining > want {
					t.Errorf("attempt %d remaining=%v want at most %v", index, remaining, want)
				}
				var payload map[string]any
				if json.NewDecoder(request.Body).Decode(&payload) != nil || payload["reasoning_effort"] != nil {
					t.Error("main chat reasoning payload changed")
				}
				if test.cancelParent {
					cancel()
					<-request.Context().Done()
					return nil, request.Context().Err()
				}
				status, body := http.StatusOK, `{"choices":[{"message":{"role":"assistant","content":"在。"}}]}`
				if test.fallback != "" && index == 0 {
					status, body = http.StatusBadGateway, `{"error":"local test fallback"}`
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})}
			targets := []runtimeProviderTarget{{EndpointID: "primary", Model: test.primary, APIBase: "https://model-budget.invalid", TimeoutSeconds: 45, ProviderRetries: 4}}
			if test.fallback != "" {
				targets = append(targets, runtimeProviderTarget{EndpointID: "fallback", Model: test.fallback, APIBase: "https://model-budget.invalid", TimeoutSeconds: 60, ProviderRetries: 4})
			}
			started := time.Now()
			reply, err := agent.runAgentLoopWithTargets(ctx, runRecord{}, "在吗", "自然接话。", targets,
				runtimeToolPolicy{Authority: "member", MaxAgentSteps: 1}, runtimeMessagePolicy{})
			if test.cancelParent {
				if !errors.Is(err, context.Canceled) || time.Since(started) > time.Second {
					t.Fatalf("parent cancellation not honored promptly: %v", err)
				}
			} else if err != nil || reply.Text != "在。" {
				t.Fatalf("actual loop failed: %v", err)
			}
			if calls != len(test.wantAttempts) {
				t.Fatalf("model calls=%d want=%d", calls, len(test.wantAttempts))
			}
		})
	}
}
