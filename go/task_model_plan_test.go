package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestTaskModelPlanValidation(t *testing.T) {
	for _, scenario := range []string{"fallback", "missing_target", "history_changed", "version_changed", "ambiguous_step", "legacy_input"} {
		t.Run(scenario, func(t *testing.T) {
			f := newModelCheckpointFixture(t)
			input := taskModelInput{Version: 1, Contract: "policy", History: "history"}
			ctx := context.Background()
			id, _, _, found, err := f.runtime.prepareTaskModelStep(ctx, f.run, 0, "original", input, f.targets)
			if err != nil || found {
				t.Fatalf("prepare new step: found=%v err=%v", found, err)
			}
			completion := chatCompletion{}
			if err := json.Unmarshal([]byte(`{"choices":[{"message":{"role":"assistant","content":"Done."}}]}`), &completion); err != nil {
				t.Fatal(err)
			}
			output := taskModelOutput{Version: 1, Completion: completion, EndpointID: f.targets[0].EndpointID,
				Model: f.targets[0].Model, APIBase: f.targets[0].APIBase}
			targets := append([]runtimeProviderTarget{{EndpointID: "new-first", Model: "other", APIBase: "https://example.invalid"}}, f.targets...)
			switch scenario {
			case "missing_target":
				targets = targets[:1]
			case "history_changed":
				input.History = "changed"
			case "version_changed":
				output.Version++
			case "ambiguous_step":
				if _, err := f.runtime.beginTaskStep(f.run.ID, "", "model", "duplicate", 0, input); err != nil {
					t.Fatal(err)
				}
			case "legacy_input":
				ciphertext, err := f.runtime.encrypt([]byte(`{"messages":[]}`))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.runtime.db.Exec("UPDATE agent_task_steps SET input_cipher=? WHERE id=?", ciphertext, id); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.runtime.finishTaskStep(id, "succeeded", "", output); err != nil {
				t.Fatal(err)
			}
			_, restored, targetIndex, found, err := f.runtime.prepareTaskModelStep(ctx, f.run, 0, "new-preferred", input, targets)
			if scenario == "fallback" {
				if err != nil || !found || targetIndex != 1 || len(restored.Choices) != 1 {
					t.Fatalf("fallback restore: found=%v target=%d err=%v", found, targetIndex, err)
				}
			} else if !errors.Is(err, errTaskModelCheckpoint) || found {
				t.Fatalf("unsafe restore: found=%v err=%v", found, err)
			}
		})
	}
}

func TestTaskModelPersistenceFailureNotice(t *testing.T) {
	for err, expected := range map[error]string{
		errTaskModelCheckpoint: "task_plan_unavailable",
		errTaskPlanPersistence: "task_persistence_failed",
	} {
		for _, message := range []string{"hello", "生成图片", "生成视频"} {
			reply, code := naturalFailureReply(message, err)
			if code != expected || reply == "" || len(failureReplyOptions(code, reply)) != 1 {
				t.Fatalf("failure notice for %q: %q %q", message, reply, code)
			}
		}
	}
}
