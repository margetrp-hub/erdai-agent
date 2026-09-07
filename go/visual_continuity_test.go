package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func visualContinuityRun(t *testing.T, a *AgentRuntime, run runRecord, at time.Time) {
	t.Helper()
	stamp := at.UTC().Format(time.RFC3339Nano)
	_, err := a.db.Exec(`INSERT INTO agent_runs (id,event_id,reply_handle,transport,transport_instance,
		agent_instance_id,memory_namespace,conversation_ref,sender_ref,persona_id,thread_key,state,created_at,updated_at)
		VALUES (?,?,'reply',?,?,?,?,?,?,?,?,'delivered',?,?)`, run.ID, run.ID, run.Transport, run.TransportInstance,
		run.AgentInstanceID, run.MemoryNamespace, run.ConversationRef, run.SenderRef, run.PersonaID, run.ThreadKey, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
}

func visualContinuitySource(t *testing.T, a *AgentRuntime, run runRecord, plan visualGenerationPlan, at time.Time) string {
	t.Helper()
	visualContinuityRun(t, a, run, at)
	attachment := agentAttachment{Kind: "image", LocalPath: "selected.png", Name: "selected.png"}
	qa := mediaQualityReceipt{mediaQualityReport: mediaQualityReport{Version: 1, MediaType: "image", OperationID: plan.OperationID,
		SelectedAttempt: 1, Status: "passed", Attempts: []mediaQualityAttempt{{Attempt: 0, GenerationStatus: "completed"}, {Attempt: 1, GenerationStatus: "completed"}}},
		Results: []toolResult{{}, {Attachments: []agentAttachment{attachment}}}}
	qaID, err := a.beginTaskStep(run.ID, "", "tool", "media_quality:image", 0, map[string]string{"mediaType": "image"})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.finishTaskStep(qaID, "succeeded", "", qa); err != nil {
		t.Fatal(err)
	}
	if err = a.writeVisualRecord(run, visualPlanName(plan.AppearanceID), 1, "selected", plan); err != nil {
		t.Fatal(err)
	}
	plan.Attempt = 0
	plan.Variables = map[string]string{"scene": "wrong", "outfit": "wrong", "camera": "wrong"}
	if err = a.writeVisualRecord(run, visualPlanName(plan.AppearanceID), 0, "not-selected", plan); err != nil {
		t.Fatal(err)
	}
	stamp := at.UTC().Format(time.RFC3339Nano)
	if _, err = a.db.Exec(`INSERT INTO agent_task_artifacts (run_id,step_id,kind,name,local_path,created_at)
		VALUES (?,?,'image','selected.png','selected.png',?)`, run.ID, qaID, stamp); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(transportDeliveryMessage{Attachments: []agentAttachment{attachment}})
	if _, err = a.db.Exec(`INSERT INTO agent_deliveries (id,run_id,reply_handle,payload_json,status,created_at,updated_at)
		VALUES (?,?,'reply',?,'delivered',?,?)`, run.ID+"-delivery", run.ID, string(payload), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err = a.recordTaskOutboundMessage(context.Background(), run.ID+"-delivery", "attachment:0", run.ID+"-sent"); err != nil {
		t.Fatal(err)
	}
	return qaID
}

func TestVisualContinuityRequiresDeliveredSelectedScopedPlan(t *testing.T) {
	for _, change := range []string{"same", "transport", "transport_instance", "agent_instance_id", "memory_namespace", "conversation_ref", "sender_ref", "persona_id", "thread_key",
		"expired", "future", "failed", "undelivered", "no-artifact", "not-sent", "unselected", "generation-failed", "appearance", "revision", "binding", "reference", "pruned", "missing-variables", "placeholder", "reply", "unknown-reply"} {
		t.Run(change, func(t *testing.T) {
			a := newTaskIntentTestRuntime(t)
			defer a.Close()
			now := time.Now().UTC()
			prior, run := intentTestRun("prior"), intentTestRun("current")
			plan := visualGenerationPlan{Version: 1, OperationID: "operation", MediaType: "image", Attempt: 1,
				AppearanceID: "library", AppearanceRevision: "revision", BindingRevision: "binding", ReferenceDigest: "digest",
				UserPrompt: "自拍", Prompt: "compiled", Variables: map[string]string{"scene": "桌边", "outfit": "红色T恤", "camera": "手机前置"}}
			current := plan
			current.UserPrompt = "同一场景接着拍视频"
			if change == "pruned" {
				plan.UserPrompt = ""
			}
			if change == "placeholder" {
				plan.Variables["scene"] = "按用户明确场景；未指定的场景细节随机变化"
			}
			if change == "missing-variables" {
				plan.Variables = nil
			}
			at := now.Add(-time.Minute)
			if change == "expired" {
				at = now.Add(-31 * time.Minute)
			}
			if change == "future" {
				at = now.Add(time.Minute)
			}
			qaID := visualContinuitySource(t, a, prior, plan, at)
			visualContinuityRun(t, a, run, now)
			var query string
			switch change {
			case "transport", "transport_instance", "agent_instance_id", "memory_namespace", "conversation_ref", "sender_ref", "persona_id", "thread_key":
				query = "UPDATE agent_runs SET " + change + "='other' WHERE id='prior'"
			case "failed":
				query = "UPDATE agent_runs SET state='failed' WHERE id='prior'"
			case "undelivered":
				query = "UPDATE agent_deliveries SET status='pending'"
			case "no-artifact":
				query = "DELETE FROM agent_task_artifacts"
			case "not-sent":
				query = "UPDATE agent_deliveries SET payload_json='{}'"
			case "appearance":
				current.AppearanceID = "other"
			case "revision":
				current.AppearanceRevision = "other"
			case "binding":
				current.BindingRevision = "other"
			case "reference":
				current.ReferenceDigest = "other"
			case "reply", "unknown-reply":
				current.UserPrompt = "刚才那张，同一套衣服"
				run.ReplyToMessageID = "prior-sent"
				other := plan
				other.OperationID = "newer-operation"
				other.Variables = map[string]string{"scene": "海边", "outfit": "白色连衣裙", "camera": "手机前置"}
				visualContinuitySource(t, a, intentTestRun("newer"), other, now.Add(-time.Second))
				if change == "unknown-reply" {
					run.ReplyToMessageID = "unknown"
				}
			case "unselected", "generation-failed":
				var qa mediaQualityReceipt
				if _, err := a.loadMediaReceipt(context.Background(), qaID, &qa); err != nil {
					t.Fatal(err)
				}
				if change == "unselected" {
					qa.SelectedAttempt = -1
				} else {
					qa.Attempts[1].GenerationStatus = "failed"
				}
				if err := a.saveMediaReceipt(context.Background(), qaID, "succeeded", qa); err != nil {
					t.Fatal(err)
				}
			}
			if query != "" {
				if _, err := a.db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			got, err := a.continuingVisualPlan(context.Background(), run, current, now)
			want := change == "same" || change == "reply"
			if err != nil || (got != nil) != want || got != nil && (got.Attempt != 1 || got.Variables["scene"] != "桌边") {
				t.Fatalf("plan=%+v err=%v want=%v", got, err, want)
			}
		})
	}
}

func TestVisualContinuityOrdinaryAndNegatedRequestsDoNotQuery(t *testing.T) {
	a := newTaskIntentTestRuntime(t)
	a.Close()
	for _, prompt := range []string{"来张自拍", "上一张照片真不错", "不要同一场景", "不想同一场景", "别接着拍", "不是刚才那张", "换个场景，别保持场景"} {
		plan, err := a.continuingVisualPlan(context.Background(), intentTestRun("missing"), visualGenerationPlan{
			AppearanceID: "library", ReferenceDigest: "digest", UserPrompt: prompt}, time.Now())
		if err != nil || plan != nil {
			t.Fatalf("ordinary/negated request queried closed DB: %q %+v %v", prompt, plan, err)
		}
	}
}

func TestVisualContinuityPositivePhrases(t *testing.T) {
	for _, prompt := range []string{"同一场景", "同个地方", "接着拍", "刚才那张", "同一套", "衣服不换", "保持场景", "同一个场景", "同一件衣服"} {
		if !explicitVisualContinuity(prompt) {
			t.Fatalf("positive continuation ignored: %q", prompt)
		}
	}
}

func TestVisualContinuityCurrentChangesWin(t *testing.T) {
	prior := visualGenerationPlan{OutfitLength: "short", Variables: map[string]string{
		"scene": "桌边", "outfit": "短款棉质上衣配短裙", "primaryColor": "紫色", "activity": "桌边休息", "action": "托腮", "camera": "近景自拍"}}
	for _, sample := range []struct {
		prompt               string
		scene, outfit, color bool
	}{
		{"同一场景接着拍视频", true, true, true},
		{"同一场景接着拍视频，不要紫色，改成红色", true, true, false},
		{"同一场景，换套衣服", true, false, false},
		{"同一套衣服，在海边拍", false, true, true},
		{"同一套衣服，不要在桌边", false, true, true},
		{"同一场景，换成蓝色衬衫", true, false, false},
		{"同一场景，穿绿色T恤", true, false, false},
		{"刚才那张，换个场景，穿红色长裙，全身", false, false, false},
	} {
		now := time.Date(2026, 9, 7, 13, 0, 0, 0, shanghaiTime)
		current := visualGenerationPlan{UserPrompt: sample.prompt, OutfitLength: "short",
			Variables: allocateVisualVariables(sample.prompt, now, 17, defaultImageVisualDirectorPolicy(), "short", nil)}
		before := map[string]string{}
		for key, value := range current.Variables {
			before[key] = value
		}
		applyVisualContinuity(&current, &prior)
		for key, inherited := range map[string]bool{"scene": sample.scene, "outfit": sample.outfit, "primaryColor": sample.color} {
			want := before[key]
			if inherited {
				want = prior.Variables[key]
			}
			if current.Variables[key] != want {
				t.Fatalf("%s: %s=%q want %q", sample.prompt, key, current.Variables[key], want)
			}
		}
		if current.Variables["camera"] != before["camera"] {
			t.Fatal("continuity changed framing")
		}
	}
	current := visualGenerationPlan{UserPrompt: "同一场景，站着拍", OutfitLength: "short",
		Variables: map[string]string{"action": "按用户明确动作", "activity": "按当前动作"}}
	applyVisualContinuity(&current, &prior)
	if strings.Contains(current.Variables["action"], "托腮") || current.Variables["activity"] != "按当前动作" {
		t.Fatal("continuity changed explicit action")
	}
	current.UserPrompt = "同一场景，不要托腮"
	applyVisualContinuity(&current, &prior)
	if strings.Contains(current.Variables["action"], "托腮") || current.Variables["activity"] != "按当前动作" {
		t.Fatal("continuity restored forbidden action")
	}
	prior.Variables["scene"] = "窗边翻书"
	current.UserPrompt = "同一场景，不要看书"
	applyVisualContinuity(&current, &prior)
	if current.Variables["scene"] != "窗边" {
		t.Fatal("scene description restored a forbidden activity")
	}
	current = visualGenerationPlan{UserPrompt: "同一场景", Variables: map[string]string{"scene": "new"}}
	before := map[string]string{"scene": "new"}
	applyVisualContinuity(&current, nil)
	if !reflect.DeepEqual(current.Variables, before) {
		t.Fatal("missing verified source invented continuity")
	}
}
