package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

type mediaFollowupTask struct {
	RunID     string
	Kind      string
	State     string
	ErrorCode string
}

func classifyMediaFollowup(message string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(message))
	normalized = strings.NewReplacer(" ", "", "\t", "", "，", "", "。", "", "！", "", "!", "", "？", "", "?", "").Replace(normalized)
	if normalized == "" {
		return "", false
	}
	for _, marker := range []string{"视频呢", "视频还", "视频怎么", "视频没发", "成片呢", "片呢"} {
		if strings.Contains(normalized, marker) {
			return "video", true
		}
	}
	for _, marker := range []string{"图片呢", "图呢", "照片呢", "自拍呢", "图还", "图片怎么", "照片怎么"} {
		if strings.Contains(normalized, marker) {
			return "image", true
		}
	}
	for _, marker := range []string{"还没好吗", "还没好", "怎么没发", "没发出来", "怎么还没", "弄好了吗", "好了吗"} {
		if strings.Contains(normalized, marker) {
			return "", true
		}
	}
	return "", false
}

func (a *AgentRuntime) mediaFollowupReply(ctx context.Context, run runRecord, message string) (agentReply, bool, error) {
	desiredKind, matched := classifyMediaFollowup(message)
	if !matched {
		return agentReply{}, false, nil
	}

	task, found, err := a.latestMediaFollowupTask(ctx, run, desiredKind)
	if err != nil {
		return agentReply{}, true, err
	}
	if !found {
		text := "我这边没查到还在做的媒体任务。"
		switch desiredKind {
		case "video":
			text = "我这边没查到正在做的视频。"
		case "image":
			text = "我这边没查到正在做的图片。"
		}
		_ = a.recordRunStage(run.ID, "media_followup_checked", time.Now(), map[string]any{
			"found": false, "kind": desiredKind,
		})
		return agentReply{Text: text}, true, nil
	}

	_ = a.recordRunStage(run.ID, "media_followup_checked", time.Now(), map[string]any{
		"found": true, "kind": task.Kind, "sourceRunId": task.RunID,
		"state": task.State, "errorCode": task.ErrorCode,
	})
	return agentReply{Text: mediaFollowupStatusText(task)}, true, nil
}

func (a *AgentRuntime) latestMediaFollowupTask(ctx context.Context, run runRecord, desiredKind string) (mediaFollowupTask, bool, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT prior.id, prior.state, COALESCE(prior.error_code, ''), stage.details_json
		FROM agent_runs prior
		JOIN run_stage_events stage ON stage.run_id = prior.id AND stage.stage = 'media_task_created'
		WHERE prior.id <> ?
		  AND prior.agent_instance_id = ?
		  AND prior.transport = ? AND prior.transport_instance = ?
		  AND prior.conversation_ref = ? AND prior.sender_ref = ?
		  AND prior.persona_id = ? AND prior.created_at < ?
		ORDER BY prior.created_at DESC, stage.id DESC
		LIMIT 20`, run.ID, run.AgentInstanceID, run.Transport, run.TransportInstance,
		run.ConversationRef, run.SenderRef, run.PersonaID, run.CreatedAt)
	if err != nil {
		return mediaFollowupTask{}, false, err
	}
	defer rows.Close()

	for rows.Next() {
		var candidate mediaFollowupTask
		var detailsJSON string
		if err := rows.Scan(&candidate.RunID, &candidate.State, &candidate.ErrorCode, &detailsJSON); err != nil {
			return mediaFollowupTask{}, false, err
		}
		var details struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal([]byte(detailsJSON), &details) != nil {
			continue
		}
		candidate.Kind = strings.ToLower(strings.TrimSpace(details.Kind))
		if candidate.Kind != "video" && candidate.Kind != "image" {
			continue
		}
		if desiredKind != "" && candidate.Kind != desiredKind {
			continue
		}
		return candidate, true, nil
	}
	if err := rows.Err(); err != nil {
		return mediaFollowupTask{}, false, err
	}
	return mediaFollowupTask{}, false, nil
}

func mediaFollowupStatusText(task mediaFollowupTask) string {
	noun := "图片"
	if task.Kind == "video" {
		noun = "视频"
	}
	switch task.State {
	case "running", "queued", "waiting_approval":
		return noun + "还在跑，目前没出成片。"
	case "completed", "responding":
		return noun + "已经做好了，正在发。"
	case "delivered":
		return "刚才的" + noun + "已经发了，往上翻一下。"
	case "failed":
		if strings.Contains(task.ErrorCode, "timeout") {
			return "刚才的" + noun + "超时了，没做出来。"
		}
		return "刚才的" + noun + "没做出来。"
	case "cancelled":
		return "刚才的" + noun + "任务已经停了，没发出来。"
	default:
		return "刚才的" + noun + "状态还没同步出来。"
	}
}
