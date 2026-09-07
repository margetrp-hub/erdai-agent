package main

import (
	"encoding/json"
	"testing"
)

func TestMemorySemanticRecallDoesNotBroadenForget(t *testing.T) {
	memory, mock := newMemorySemanticFixture(t, true, nil)
	memory.runtime.memory = memory
	run := memoryLedgerTestRun("cognition", "qq")
	scope := personaMemoryScope(run.PersonaID, "user", runtimeScopeFromRun(run).userMemoryRef())
	item := addSemanticMemory(t, memory, scope, "我习惯早上喝黑咖啡")
	if recalled, err := memory.SearchMemories(t.Context(), scope, "晨间饮品偏好", 5); err != nil || len(recalled) != 1 || recalled[0].ID != item.ID {
		t.Fatalf("fixture must demonstrate semantic-only recall: %+v err=%v", recalled, err)
	}
	for _, sample := range []struct {
		query   string
		deleted int
	}{
		{"晨间饮品偏好", 0},
		{"黑咖啡", 1},
	} {
		result, err := memory.runtime.forgetMemory(t.Context(), run, sample.query)
		if err != nil {
			t.Fatal(err)
		}
		var output struct {
			Deleted int `json:"deleted"`
		}
		if err := json.Unmarshal([]byte(result.Content), &output); err != nil || output.Deleted != sample.deleted {
			t.Fatalf("forget %q = %s err=%v", sample.query, result.Content, err)
		}
		if len(mock.batches()) != 1 {
			t.Fatal("forget consulted semantic provider")
		}
	}
}
