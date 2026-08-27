package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func insertMediaFollowupTask(t *testing.T, runtime *AgentRuntime, id, kind, state, errorCode string, createdAt time.Time) runRecord {
	t.Helper()
	run := insertHonestyTestRun(t, runtime, id, "group-media-followup", "sender-media", "group", state, createdAt)
	run.AgentInstanceID = "legacy-default"
	run.PersonaID = "doubao"
	if _, err := runtime.db.Exec(`UPDATE agent_runs SET agent_instance_id = ?, persona_id = ?,
		transport = ?, transport_instance = ?, error_code = ? WHERE id = ?`,
		run.AgentInstanceID, run.PersonaID, run.Transport, run.TransportInstance, nullable(errorCode), run.ID); err != nil {
		t.Fatal(err)
	}
	stamp := createdAt.UTC().Format(time.RFC3339Nano)
	if _, err := runtime.db.Exec(`INSERT INTO agent_task_steps
		(id, run_id, step_index, kind, name, status, attempts, created_at, updated_at)
		VALUES (?, ?, 0, 'tool', ?, 'running', 1, ?, ?)`,
		"step-"+id, run.ID, "generate_"+kind, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := runtime.recordRunStage(run.ID, "media_task_created", createdAt, map[string]any{
		"kind": kind, "taskId": "task-" + id,
	}); err != nil {
		t.Fatal(err)
	}
	return run
}

func mediaFollowupCurrentRun(t *testing.T, runtime *AgentRuntime, id string, createdAt time.Time) runRecord {
	t.Helper()
	run := insertHonestyTestRun(t, runtime, id, "group-media-followup", "sender-media", "group", "running", createdAt)
	run.AgentInstanceID = "legacy-default"
	run.PersonaID = "doubao"
	if _, err := runtime.db.Exec(`UPDATE agent_runs SET agent_instance_id = ?, persona_id = ?,
		transport = ?, transport_instance = ? WHERE id = ?`,
		run.AgentInstanceID, run.PersonaID, run.Transport, run.TransportInstance, run.ID); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestMediaFollowupReplyReportsRunningVideo(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	now := time.Now().UTC()
	prior := insertMediaFollowupTask(t, runtime, "prior-video", "video", "running", "", now.Add(-time.Minute))
	current := mediaFollowupCurrentRun(t, runtime, "current-followup", now)

	reply, handled, err := runtime.mediaFollowupReply(context.Background(), current, "视频呢？")
	if err != nil || !handled || !strings.Contains(reply.Text, "还在跑") {
		t.Fatalf("followup reply = %+v handled=%v err=%v", reply, handled, err)
	}
	var sourceRunID string
	if err := runtime.db.QueryRow(`SELECT json_extract(details_json, '$.sourceRunId') FROM run_stage_events
		WHERE run_id = ? AND stage = 'media_followup_checked' ORDER BY id DESC LIMIT 1`, current.ID).Scan(&sourceRunID); err != nil {
		t.Fatal(err)
	}
	if sourceRunID != prior.ID {
		t.Fatalf("followup source = %q, want %q", sourceRunID, prior.ID)
	}
}

func TestMediaFollowupReplyReportsFailedVideo(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	now := time.Now().UTC()
	insertMediaFollowupTask(t, runtime, "failed-video", "video", "failed", "video_generation_timeout", now.Add(-time.Minute))
	current := mediaFollowupCurrentRun(t, runtime, "failed-followup", now)

	reply, handled, err := runtime.mediaFollowupReply(context.Background(), current, "怎么还没发")
	if err != nil || !handled || !strings.Contains(reply.Text, "超时") || !strings.Contains(reply.Text, "没做出来") {
		t.Fatalf("failed followup reply = %+v handled=%v err=%v", reply, handled, err)
	}
}

func TestMediaFollowupReplyDoesNotInterceptNormalChat(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	current := mediaFollowupCurrentRun(t, runtime, "normal-chat", time.Now().UTC())

	_, handled, err := runtime.mediaFollowupReply(context.Background(), current, "你在干嘛")
	if err != nil || handled {
		t.Fatalf("normal chat handled=%v err=%v", handled, err)
	}
}

func TestEnqueueDeliveryKeepsBackedMediaTerminalAfterNewerText(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	now := time.Now().UTC()
	older := insertMediaFollowupTask(t, runtime, "older-media", "video", "running", "", now.Add(-30*time.Second))
	newer := insertHonestyTestRun(t, runtime, "newer-text", "group-media-followup", "sender-media", "group", "responding", now.Add(-5*time.Second))
	if err := runtime.enqueueDelivery(newer, agentReply{Text: "视频呢？我在查。"}, "terminal", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.db.Exec(`UPDATE agent_deliveries SET status = 'delivered' WHERE run_id = ?`, newer.ID); err != nil {
		t.Fatal(err)
	}

	err := runtime.enqueueDelivery(older, agentReply{
		Text:        "成片好了。",
		Attachments: []agentAttachment{{Kind: "video", LocalPath: "/erdai-media/result.mp4", Name: "result.mp4"}},
	}, "terminal", "")
	if err != nil {
		t.Fatalf("backed media terminal was discarded: %v", err)
	}
	var count int
	if err := runtime.db.QueryRow(`SELECT count(*) FROM agent_deliveries WHERE run_id = ? AND phase = 'terminal'`, older.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("backed media terminal count = %d", count)
	}
}
