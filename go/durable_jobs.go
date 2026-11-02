package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func boolPtr(value bool) *bool { return &value }

// durableJobStatuses are intentionally separate from agent_runs. A run is one
// wake-up/message turn; a durable job is the user-owned work that may span
// multiple wake-ups, approvals, retries, or process restarts.
const (
	durableJobPlanned         = "planned"
	durableJobQueued          = "queued"
	durableJobLeased          = "leased"
	durableJobRunning         = "running"
	durableJobCheckpointed    = "checkpointed"
	durableJobWaitingApproval = "waiting_approval"
	durableJobWaitingExternal = "waiting_external"
	durableJobPaused          = "paused"
	durableJobRetrying        = "retrying"
	durableJobSucceeded       = "succeeded"
	durableJobFailed          = "failed"
	durableJobCancelled       = "cancelled"
	durableJobDeadLetter      = "dead_letter"
	durableJobUnknownManual   = "unknown_manual"
)

var durableJobStatusSet = map[string]struct{}{
	durableJobPlanned: {}, durableJobQueued: {}, durableJobLeased: {}, durableJobRunning: {},
	durableJobCheckpointed: {}, durableJobWaitingApproval: {}, durableJobWaitingExternal: {},
	durableJobPaused: {}, durableJobRetrying: {}, durableJobSucceeded: {}, durableJobFailed: {},
	durableJobCancelled: {}, durableJobDeadLetter: {}, durableJobUnknownManual: {},
}

// ensureDurableJobsSchema is deliberately idempotent and does not change the
// Core PRAGMA user_version. The release line can therefore introduce the
// control-plane tables without invalidating an existing runtime database.
func ensureDurableJobsSchema(db interface {
	Exec(query string, args ...any) (sql.Result, error)
}) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS durable_jobs (
  id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL DEFAULT '',
  workspace_id TEXT NOT NULL DEFAULT '',
  bot_id TEXT NOT NULL DEFAULT '',
  source_transport TEXT NOT NULL DEFAULT '',
  source_conversation_ref TEXT NOT NULL DEFAULT '',
  source_message_id TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  goal_cipher BLOB NOT NULL,
  plan_json TEXT NOT NULL DEFAULT '{}',
  status TEXT NOT NULL CHECK (status IN ('planned','queued','leased','running','checkpointed','waiting_approval','waiting_external','paused','retrying','succeeded','failed','cancelled','dead_letter','unknown_manual')),
  requires_approval INTEGER NOT NULL DEFAULT 0 CHECK (requires_approval IN (0,1)),
  priority INTEGER NOT NULL DEFAULT 100,
  next_action TEXT NOT NULL DEFAULT '',
  next_run_at TEXT,
  lease_owner TEXT,
  lease_expires_at TEXT,
  heartbeat_at TEXT,
  attempts INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts BETWEEN 1 AND 100),
  idempotency_key TEXT NOT NULL DEFAULT '',
  last_error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  started_at TEXT,
  finished_at TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_durable_jobs_idempotency
  ON durable_jobs(owner_id, workspace_id, idempotency_key)
  WHERE idempotency_key <> '';
CREATE INDEX IF NOT EXISTS idx_durable_jobs_queue
  ON durable_jobs(status, next_run_at, priority DESC, created_at);
CREATE INDEX IF NOT EXISTS idx_durable_jobs_owner
  ON durable_jobs(owner_id, workspace_id, updated_at DESC);
CREATE TABLE IF NOT EXISTS durable_job_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id TEXT NOT NULL REFERENCES durable_jobs(id) ON DELETE CASCADE,
  event_type TEXT NOT NULL,
  from_status TEXT NOT NULL DEFAULT '',
  to_status TEXT NOT NULL DEFAULT '',
  reason TEXT NOT NULL DEFAULT '',
  next_action TEXT NOT NULL DEFAULT '',
  actor TEXT NOT NULL DEFAULT 'system',
  payload_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_durable_job_events_job
  ON durable_job_events(job_id, id);
CREATE TABLE IF NOT EXISTS durable_job_results (
  job_id TEXT NOT NULL REFERENCES durable_jobs(id) ON DELETE CASCADE,
  attempt INTEGER NOT NULL,
  result_cipher BLOB NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(job_id, attempt)
);
`)
	return err
}

type durableJobCreatePayload struct {
	OwnerID               string         `json:"ownerId"`
	WorkspaceID           string         `json:"workspaceId"`
	BotID                 string         `json:"botId"`
	SourceTransport       string         `json:"sourceTransport"`
	SourceConversationRef string         `json:"sourceConversationRef"`
	SourceMessageID       string         `json:"sourceMessageId"`
	Title                 string         `json:"title"`
	Goal                  string         `json:"goal"`
	Instruction           string         `json:"instruction"`
	Plan                  map[string]any `json:"plan"`
	RequiresApproval      bool           `json:"requiresApproval"`
	Priority              int            `json:"priority"`
	NextRunAt             string         `json:"nextRunAt"`
	MaxAttempts           int            `json:"maxAttempts"`
	IdempotencyKey        string         `json:"idempotencyKey"`
	StartImmediately      *bool          `json:"startImmediately"`
}

var durableJobCreateFields = coreFieldSet(
	"ownerId", "workspaceId", "botId", "sourceTransport", "sourceConversationRef", "sourceMessageId",
	"title", "goal", "instruction", "plan", "requiresApproval", "priority", "nextRunAt", "maxAttempts",
	"idempotencyKey", "startImmediately",
)

type durableJobRecord struct {
	ID, OwnerID, WorkspaceID, BotID                         string
	SourceTransport, SourceConversationRef, SourceMessageID string
	Title, Goal, Status, NextAction, NextRunAt              string
	Plan                                                    map[string]any
	RequiresApproval                                        bool
	Priority, Attempts, MaxAttempts                         int
	LeaseOwner, LeaseExpiresAt, HeartbeatAt, LastError      string
	CreatedAt, UpdatedAt, StartedAt, FinishedAt             string
	ResultText                                              string
}

func (a *AgentRuntime) updateDurableJobGoal(ctx context.Context, id, goal, actor string) (durableJobRecord, error) {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return durableJobRecord{}, coreInvalid("goal is required")
	}
	goal, err := normalizeCoreText(goal, "goal", 8000, true)
	if err != nil {
		return durableJobRecord{}, err
	}
	if a.aead == nil {
		return durableJobRecord{}, errors.New("durable job encryption is unavailable")
	}
	ciphertext, err := a.encrypt([]byte(goal))
	if err != nil {
		return durableJobRecord{}, err
	}
	planJSON := "{}"
	if plan := durableJobPlanFromGoal(goal); plan != nil {
		encoded, marshalErr := json.Marshal(plan)
		if marshalErr != nil {
			return durableJobRecord{}, marshalErr
		}
		planJSON = string(encoded)
	}
	tx, err := a.durableJobsDatabase().BeginTx(ctx, nil)
	if err != nil {
		return durableJobRecord{}, err
	}
	defer tx.Rollback()
	var status string
	if err = tx.QueryRowContext(ctx, "SELECT status FROM durable_jobs WHERE id=?", id).Scan(&status); errors.Is(err, sql.ErrNoRows) {
		return durableJobRecord{}, mgmtNotFound("durable job")
	} else if err != nil {
		return durableJobRecord{}, err
	}
	if status == durableJobSucceeded || status == durableJobCancelled || status == durableJobDeadLetter {
		return durableJobRecord{}, coreInvalid("terminal durable job cannot be edited")
	}
	now := mgmtNow()
	// A revised goal is a new execution attempt. Clear an old lease/error and
	// queue it immediately unless the job still requires approval.
	target := durableJobQueued
	// Re-evaluate the approval policy from the replacement goal. Callers may
	// explicitly request approval, but they must not be able to clear the
	// built-in high-risk gate by editing a task through the API.
	requiresApproval := durableJobNeedsApproval(goal)
	if requiresApproval {
		target = durableJobWaitingApproval
	}
	_, err = tx.ExecContext(ctx, `UPDATE durable_jobs SET goal_cipher=?, plan_json=?, requires_approval=?, status=?, next_action=?, next_run_at=NULL,
		last_error='', lease_owner=NULL, lease_expires_at=NULL, heartbeat_at=NULL, finished_at=NULL, updated_at=? WHERE id=?`,
		ciphertext, planJSON, boolInt(requiresApproval), target, "等待调度器领取", now, id)
	if err != nil {
		return durableJobRecord{}, err
	}
	if err = a.appendDurableJobEvent(ctx, tx, id, "goal_updated", status, target, "user changed the goal", "等待调度器领取", actor, map[string]any{"goalLength": len([]rune(goal))}); err != nil {
		return durableJobRecord{}, err
	}
	if err = tx.Commit(); err != nil {
		return durableJobRecord{}, err
	}
	return a.durableJobRecord(ctx, id)
}

func durableJobNeedsApproval(goal string) bool {
	goal = strings.ToLower(strings.TrimSpace(goal))
	for _, word := range []string{"删除", "部署", "发布", "付款", "支付", "发送", "群发", "修改生产", "清理服务器"} {
		if strings.Contains(goal, word) {
			return true
		}
	}
	return false
}

func durableJobTitle(goal string) string {
	goal = strings.TrimSpace(goal)
	if len([]rune(goal)) > 80 {
		return string([]rune(goal)[:80]) + "…"
	}
	return goal
}

func durableJobPlanFromGoal(goal string) map[string]any {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return nil
	}
	for _, item := range []struct {
		markers  []string
		interval string
	}{
		{[]string{"每分钟", "每1分钟"}, "minutely"},
		{[]string{"每小时", "每1小时"}, "hourly"},
		{[]string{"每天", "每日"}, "daily"},
		{[]string{"每周", "每星期"}, "weekly"},
	} {
		for _, marker := range item.markers {
			if strings.Contains(goal, marker) {
				return map[string]any{"kind": "recurring", "interval": item.interval}
			}
		}
	}
	return nil
}

func durableJobNextRun(plan map[string]any, now time.Time) (time.Time, bool) {
	interval, _ := plan["interval"].(string)
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "minutely":
		return now.Add(time.Minute), true
	case "hourly":
		return now.Add(time.Hour), true
	case "daily":
		return now.Add(24 * time.Hour), true
	case "weekly":
		return now.Add(7 * 24 * time.Hour), true
	default:
		return time.Time{}, false
	}
}

func (a *AgentRuntime) handleDurableJobDirectCommand(ctx context.Context, run runRecord, command coreDirectCommand) (agentReply, error) {
	actor := strings.TrimSpace(run.SenderRef)
	if actor == "" {
		return agentReply{}, coreInvalid("sender identity is required for durable jobs")
	}
	if command.DurableJobAction == "create" {
		goal := strings.TrimSpace(command.DurableJobGoal)
		value, _, err := a.createDurableJob(ctx, durableJobCreatePayload{
			OwnerID: actor, WorkspaceID: strings.TrimSpace(run.ConversationRef), BotID: strings.TrimSpace(run.AgentInstanceID),
			SourceTransport: run.Transport, SourceConversationRef: run.ConversationRef, SourceMessageID: run.MessageID,
			Title: durableJobTitle(goal), Goal: goal, Instruction: goal, Plan: durableJobPlanFromGoal(goal), RequiresApproval: durableJobNeedsApproval(goal),
			StartImmediately: boolPtr(true),
		})
		if err != nil {
			return agentReply{}, err
		}
		status := "已排队"
		if value.RequiresApproval {
			status = "等待确认"
		}
		return agentReply{Text: fmt.Sprintf("已创建长期任务：%s\n任务编号：%s\n状态：%s", value.Title, value.ID, status)}, nil
	}
	value, err := a.durableJobRecord(ctx, command.DurableJobID)
	if err != nil {
		return agentReply{}, err
	}
	if !run.IsAdmin && value.OwnerID != actor {
		return agentReply{}, coreInvalid("只能操作自己创建的任务")
	}
	if command.DurableJobAction == "update" {
		value, err = a.updateDurableJobGoal(ctx, value.ID, command.DurableJobGoal, actor)
	} else {
		value, err = a.transitionDurableJob(ctx, value.ID, command.DurableJobAction, actor)
	}
	if err != nil {
		return agentReply{}, err
	}
	labels := map[string]string{"pause": "已暂停", "resume": "已继续", "cancel": "已取消", "retry": "已安排重试", "approve": "已批准", "update": "已更新目标"}
	label := labels[command.DurableJobAction]
	if label == "" {
		label = "已更新"
	}
	return agentReply{Text: fmt.Sprintf("%s：%s\n当前状态：%s", label, value.ID, value.Status)}, nil
}

func (a *AgentRuntime) durableJobsDatabase() *sql.DB {
	if a != nil && a.configStore != nil && a.configStore.db != nil {
		return a.configStore.db
	}
	if a == nil {
		return nil
	}
	return a.db
}

func normalizeDurableJobStatus(value string) string {
	value = strings.TrimSpace(value)
	if _, ok := durableJobStatusSet[value]; ok {
		return value
	}
	return ""
}

func durableJobTransitionAllowed(from, to string) bool {
	if from == to {
		return true
	}
	switch from {
	case durableJobPlanned:
		return to == durableJobQueued || to == durableJobWaitingApproval || to == durableJobCancelled
	case durableJobQueued:
		return to == durableJobLeased || to == durableJobPaused || to == durableJobCancelled
	case durableJobLeased:
		return to == durableJobRunning || to == durableJobQueued || to == durableJobPaused || to == durableJobCancelled
	case durableJobRunning:
		// A running tool round may already have produced an external side effect.
		// Pause/cancel therefore cannot pretend to be terminal while the worker
		// is still executing; lease expiry or the worker result must settle it.
		return to == durableJobCheckpointed || to == durableJobWaitingApproval || to == durableJobWaitingExternal || to == durableJobRetrying || to == durableJobSucceeded || to == durableJobFailed || to == durableJobDeadLetter || to == durableJobPlanned || to == durableJobUnknownManual
	case durableJobCheckpointed:
		return to == durableJobQueued || to == durableJobRunning || to == durableJobPaused || to == durableJobCancelled
	case durableJobWaitingApproval, durableJobWaitingExternal, durableJobPaused:
		return to == durableJobQueued || to == durableJobRunning || to == durableJobCancelled
	case durableJobRetrying:
		return to == durableJobQueued || to == durableJobRunning || to == durableJobFailed || to == durableJobDeadLetter || to == durableJobCancelled
	case durableJobFailed, durableJobUnknownManual:
		return to == durableJobRetrying || to == durableJobQueued || to == durableJobCancelled || to == durableJobDeadLetter
	case durableJobCancelled:
		return to == durableJobRetrying
	case durableJobSucceeded, durableJobDeadLetter:
		return false
	default:
		return false
	}
}

func decodeDurableJobPlan(raw string) map[string]any {
	value := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &value)
	}
	return value
}

func (a *AgentRuntime) durableJobRecord(ctx context.Context, id string) (durableJobRecord, error) {
	var value durableJobRecord
	var goalCipher []byte
	var planJSON string
	var approval int
	var nextRun, leaseOwner, leaseExpires, heartbeat, started, finished sql.NullString
	err := a.durableJobsDatabase().QueryRowContext(ctx, `SELECT id,owner_id,workspace_id,bot_id,source_transport,source_conversation_ref,
		source_message_id,title,goal_cipher,plan_json,status,requires_approval,priority,next_action,next_run_at,
		lease_owner,lease_expires_at,heartbeat_at,attempts,max_attempts,last_error,created_at,updated_at,started_at,finished_at
		FROM durable_jobs WHERE id = ?`, id).Scan(
		&value.ID, &value.OwnerID, &value.WorkspaceID, &value.BotID, &value.SourceTransport, &value.SourceConversationRef,
		&value.SourceMessageID, &value.Title, &goalCipher, &planJSON, &value.Status, &approval, &value.Priority,
		&value.NextAction, &nextRun, &leaseOwner, &leaseExpires, &heartbeat, &value.Attempts, &value.MaxAttempts,
		&value.LastError, &value.CreatedAt, &value.UpdatedAt, &started, &finished)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return durableJobRecord{}, mgmtNotFound("durable job")
		}
		return durableJobRecord{}, err
	}
	if len(goalCipher) > 0 && a.aead != nil {
		if plain, decryptErr := a.decrypt(goalCipher); decryptErr == nil {
			value.Goal = string(plain)
		} else {
			value.Goal = "[无法解密]"
		}
	}
	value.Plan = decodeDurableJobPlan(planJSON)
	value.RequiresApproval = approval == 1
	value.NextRunAt, value.LeaseOwner, value.LeaseExpiresAt, value.HeartbeatAt, value.StartedAt, value.FinishedAt = nextRun.String, leaseOwner.String, leaseExpires.String, heartbeat.String, started.String, finished.String
	var resultCipher []byte
	if err := a.durableJobsDatabase().QueryRowContext(ctx, `SELECT result_cipher FROM durable_job_results WHERE job_id=? ORDER BY attempt DESC LIMIT 1`, id).Scan(&resultCipher); err == nil && len(resultCipher) > 0 && a.aead != nil {
		if plain, decryptErr := a.decrypt(resultCipher); decryptErr == nil {
			var reply agentReply
			if json.Unmarshal(plain, &reply) == nil {
				value.ResultText = strings.TrimSpace(reply.Text)
			}
		}
	}
	return value, nil
}

func durableJobResponse(value durableJobRecord) map[string]any {
	return map[string]any{
		"id": value.ID, "ownerId": value.OwnerID, "workspaceId": value.WorkspaceID, "botId": value.BotID,
		"sourceTransport": value.SourceTransport, "sourceConversationRef": value.SourceConversationRef,
		"sourceMessageId": value.SourceMessageID, "title": value.Title, "goal": value.Goal, "plan": value.Plan,
		"status": value.Status, "requiresApproval": value.RequiresApproval, "priority": value.Priority,
		"nextAction": value.NextAction, "nextRunAt": value.NextRunAt, "leaseOwner": value.LeaseOwner,
		"leaseExpiresAt": value.LeaseExpiresAt, "heartbeatAt": value.HeartbeatAt, "attempts": value.Attempts,
		"maxAttempts": value.MaxAttempts, "lastError": value.LastError, "createdAt": value.CreatedAt,
		"updatedAt": value.UpdatedAt, "startedAt": value.StartedAt, "finishedAt": value.FinishedAt, "result": value.ResultText,
	}
}

func (a *AgentRuntime) appendDurableJobEvent(ctx context.Context, tx *sql.Tx, jobID, eventType, fromStatus, toStatus, reason, nextAction, actor string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO durable_job_events
		(job_id,event_type,from_status,to_status,reason,next_action,actor,payload_json,created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, jobID, eventType, fromStatus, toStatus, reason, nextAction, actor, string(encoded), mgmtNow())
	return err
}

func (a *AgentRuntime) createDurableJob(ctx context.Context, input durableJobCreatePayload) (durableJobRecord, bool, error) {
	if db := a.durableJobsDatabase(); db == nil {
		return durableJobRecord{}, false, errors.New("runtime database is unavailable")
	} else if err := ensureDurableJobsSchema(db); err != nil {
		return durableJobRecord{}, false, err
	}
	goal := strings.TrimSpace(input.Instruction)
	if goal == "" {
		goal = strings.TrimSpace(input.Goal)
	}
	goal, err := normalizeCoreText(goal, "goal", 8000, true)
	if err != nil {
		return durableJobRecord{}, false, err
	}
	owner, err := normalizeCoreText(input.OwnerID, "ownerId", 160, false)
	if err != nil {
		return durableJobRecord{}, false, err
	}
	if owner == "" {
		owner = "admin"
	}
	workspace, err := normalizeCoreText(input.WorkspaceID, "workspaceId", 160, false)
	if err != nil {
		return durableJobRecord{}, false, err
	}
	if workspace == "" {
		workspace = "default"
	}
	bot, err := normalizeCoreText(input.BotID, "botId", 160, false)
	if err != nil {
		return durableJobRecord{}, false, err
	}
	if bot == "" {
		bot = legacyAgentInstanceID
	}
	title, err := normalizeCoreText(input.Title, "title", 240, false)
	if err != nil {
		return durableJobRecord{}, false, err
	}
	if input.Priority == 0 {
		input.Priority = 100
	}
	if input.Priority < -10000 || input.Priority > 10000 {
		return durableJobRecord{}, false, coreInvalid("priority is out of range")
	}
	if input.MaxAttempts == 0 {
		input.MaxAttempts = 3
	}
	if input.MaxAttempts < 1 || input.MaxAttempts > 100 {
		return durableJobRecord{}, false, coreInvalid("maxAttempts is out of range")
	}
	if input.NextRunAt != "" {
		if _, err = time.Parse(time.RFC3339Nano, input.NextRunAt); err != nil {
			return durableJobRecord{}, false, coreInvalid("nextRunAt must be RFC3339")
		}
	}
	if len(input.IdempotencyKey) > 160 {
		return durableJobRecord{}, false, coreInvalid("idempotencyKey is too long")
	}
	if a.aead == nil {
		return durableJobRecord{}, false, errors.New("durable job encryption is unavailable")
	}
	ciphertext, err := a.encrypt([]byte(goal))
	if err != nil {
		return durableJobRecord{}, false, err
	}
	planJSON := "{}"
	if input.Plan == nil {
		input.Plan = durableJobPlanFromGoal(goal)
	}
	if input.Plan != nil {
		encoded, marshalErr := json.Marshal(input.Plan)
		if marshalErr != nil {
			return durableJobRecord{}, false, coreInvalid("plan must be JSON")
		}
		planJSON = string(encoded)
	}
	status := durableJobQueued
	// High-risk goals are gated even when the API caller omitted the optional
	// flag. The direct-command path already sets the flag; keeping the policy
	// here closes the management API path as well.
	input.RequiresApproval = input.RequiresApproval || durableJobNeedsApproval(goal)
	if input.RequiresApproval {
		status = durableJobWaitingApproval
	}
	if input.StartImmediately != nil && !*input.StartImmediately {
		status = durableJobPlanned
	}
	if input.NextRunAt != "" && status == durableJobQueued {
		status = durableJobPlanned
	}
	id, err := newCoreUUID()
	if err != nil {
		return durableJobRecord{}, false, err
	}
	now := mgmtNow()
	tx, err := a.durableJobsDatabase().BeginTx(ctx, nil)
	if err != nil {
		return durableJobRecord{}, false, err
	}
	defer tx.Rollback()
	if input.IdempotencyKey != "" {
		var existing string
		lookupErr := tx.QueryRowContext(ctx, "SELECT id FROM durable_jobs WHERE owner_id=? AND workspace_id=? AND idempotency_key=?", owner, workspace, input.IdempotencyKey).Scan(&existing)
		if lookupErr == nil {
			if err = tx.Commit(); err != nil {
				return durableJobRecord{}, false, err
			}
			value, readErr := a.durableJobRecord(ctx, existing)
			return value, true, readErr
		}
		if !errors.Is(lookupErr, sql.ErrNoRows) {
			return durableJobRecord{}, false, lookupErr
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO durable_jobs
		(id,owner_id,workspace_id,bot_id,source_transport,source_conversation_ref,source_message_id,title,goal_cipher,plan_json,status,requires_approval,priority,next_action,next_run_at,attempts,max_attempts,idempotency_key,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, owner, workspace, bot, input.SourceTransport, input.SourceConversationRef, input.SourceMessageID,
		title, ciphertext, planJSON, status, boolInt(input.RequiresApproval), input.Priority, "等待调度器领取", nullable(input.NextRunAt), 0, input.MaxAttempts, input.IdempotencyKey, now, now)
	if err != nil {
		return durableJobRecord{}, false, mgmtConstraintError(err, "durable job idempotency key already exists")
	}
	if err = a.appendDurableJobEvent(ctx, tx, id, "created", "", status, "", "等待调度器领取", "admin", map[string]any{"goalLength": len([]rune(goal))}); err != nil {
		return durableJobRecord{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return durableJobRecord{}, false, err
	}
	if a.configStore != nil {
		_ = a.configStore.mgmtAudit("create", "durable_job", id, []string{"goal", "status", "plan"})
	}
	value, err := a.durableJobRecord(ctx, id)
	return value, false, err
}

func (a *AgentRuntime) transitionDurableJob(ctx context.Context, id, action, actor string) (durableJobRecord, error) {
	target := map[string]string{"pause": durableJobPaused, "resume": durableJobQueued, "cancel": durableJobCancelled, "retry": durableJobRetrying, "approve": durableJobQueued}[action]
	if target == "" {
		return durableJobRecord{}, coreInvalid("unsupported durable job action")
	}
	now := mgmtNow()
	tx, err := a.durableJobsDatabase().BeginTx(ctx, nil)
	if err != nil {
		return durableJobRecord{}, err
	}
	defer tx.Rollback()
	var status string
	if err = tx.QueryRowContext(ctx, "SELECT status FROM durable_jobs WHERE id=?", id).Scan(&status); errors.Is(err, sql.ErrNoRows) {
		return durableJobRecord{}, mgmtNotFound("durable job")
	} else if err != nil {
		return durableJobRecord{}, err
	}
	// Approval is a separate gate. A generic resume must not move an approval
	// waiting job into the queue while leaving requires_approval set, because
	// the worker deliberately refuses such a job and it would become stuck.
	if status == durableJobWaitingApproval && action != "approve" && action != "cancel" {
		return durableJobRecord{}, coreInvalid("durable job is waiting for approval")
	}
	if action == "approve" && status != durableJobWaitingApproval {
		return durableJobRecord{}, coreInvalid("durable job is not waiting for approval")
	}
	if !durableJobTransitionAllowed(status, target) {
		return durableJobRecord{}, coreInvalid("durable job cannot transition from " + status + " to " + target)
	}
	nextAction := "等待调度器领取"
	if target == durableJobPaused {
		nextAction = "等待恢复"
	}
	if target == durableJobWaitingApproval {
		nextAction = "等待管理员批准"
	}
	if target == durableJobSucceeded || target == durableJobFailed || target == durableJobCancelled || target == durableJobDeadLetter {
		nextAction = "已结束"
	}
	approvalReset := 0
	if action == "approve" {
		approvalReset = 1
	}
	_, err = tx.ExecContext(ctx, `UPDATE durable_jobs SET status=?, requires_approval=CASE WHEN ?=1 THEN 0 ELSE requires_approval END, next_action=?, last_error=CASE WHEN ?='retrying' THEN '' ELSE last_error END,
		lease_owner=NULL, lease_expires_at=NULL, heartbeat_at=NULL, updated_at=?, finished_at=CASE WHEN ? IN ('succeeded','failed','cancelled','dead_letter') THEN ? ELSE finished_at END WHERE id=?`, target, approvalReset, nextAction, target, now, target, now, id)
	if err != nil {
		return durableJobRecord{}, err
	}
	if err = a.appendDurableJobEvent(ctx, tx, id, action, status, target, "", nextAction, actor, nil); err != nil {
		return durableJobRecord{}, err
	}
	if err = tx.Commit(); err != nil {
		return durableJobRecord{}, err
	}
	if a.configStore != nil {
		_ = a.configStore.mgmtAudit(action, "durable_job", id, []string{"status"})
	}
	return a.durableJobRecord(ctx, id)
}

func (a *AgentRuntime) durableJobEvents(ctx context.Context, id string) ([]map[string]any, error) {
	rows, err := a.durableJobsDatabase().QueryContext(ctx, `SELECT id,event_type,from_status,to_status,reason,next_action,actor,payload_json,created_at
		FROM durable_job_events WHERE job_id=? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var eventID int64
		var eventType, from, to, reason, nextAction, actor, payload, created string
		if err = rows.Scan(&eventID, &eventType, &from, &to, &reason, &nextAction, &actor, &payload, &created); err != nil {
			return nil, err
		}
		var decoded any
		if json.Unmarshal([]byte(payload), &decoded) != nil {
			decoded = map[string]any{}
		}
		items = append(items, map[string]any{"id": eventID, "eventType": eventType, "fromStatus": from, "toStatus": to, "reason": reason, "nextAction": nextAction, "actor": actor, "payload": decoded, "createdAt": created})
	}
	return items, rows.Err()
}

func (a *AgentRuntime) handleDurableJobManagement(w http.ResponseWriter, r *http.Request, path string) error {
	db := a.durableJobsDatabase()
	if db == nil {
		return errors.New("runtime database is unavailable")
	}
	if err := ensureDurableJobsSchema(db); err != nil {
		return err
	}
	if path == "/api/v1/jobs" {
		if r.Method == http.MethodPost {
			var input durableJobCreatePayload
			if _, err := decodeCoreObject(r, durableJobCreateFields, "durable job", &input); err != nil {
				return err
			}
			value, existing, err := a.createDurableJob(r.Context(), input)
			if err == nil {
				status := http.StatusCreated
				if existing {
					status = http.StatusOK
				}
				mgmtWriteData(w, status, durableJobResponse(value))
			}
			return err
		}
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit < 1 || limit > 200 {
			limit = 50
		}
		status := normalizeDurableJobStatus(r.URL.Query().Get("status"))
		query := `SELECT id FROM durable_jobs`
		args := []any{}
		if status != "" {
			query += " WHERE status=?"
			args = append(args, status)
		}
		query += " ORDER BY priority DESC, updated_at DESC LIMIT ?"
		args = append(args, limit)
		rows, err := db.QueryContext(r.Context(), query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				return err
			}
			value, readErr := a.durableJobRecord(r.Context(), id)
			if readErr != nil {
				return readErr
			}
			items = append(items, durableJobResponse(value))
		}
		mgmtWriteData(w, http.StatusOK, items)
		return rows.Err()
	}
	idPath := strings.TrimPrefix(path, "/api/v1/jobs/")
	parts := strings.Split(strings.TrimSuffix(idPath, "/"), "/")
	if len(parts) == 0 || parts[0] == "" || strings.Contains(parts[0], "?") {
		return mgmtNotFound("durable job")
	}
	id := parts[0]
	if len(parts) == 2 && parts[1] == "events" {
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		if _, err := a.durableJobRecord(r.Context(), id); err != nil {
			return err
		}
		events, err := a.durableJobEvents(r.Context(), id)
		if err == nil {
			mgmtWriteData(w, http.StatusOK, events)
		}
		return err
	}
	if len(parts) == 2 {
		if r.Method != http.MethodPost {
			return mgmtMethodNotAllowed()
		}
		value, err := a.transitionDurableJob(r.Context(), id, parts[1], "admin")
		if err == nil {
			mgmtWriteData(w, http.StatusAccepted, durableJobResponse(value))
		}
		return err
	}
	if r.Method != http.MethodGet {
		return mgmtMethodNotAllowed()
	}
	value, err := a.durableJobRecord(r.Context(), id)
	if err == nil {
		mgmtWriteData(w, http.StatusOK, durableJobResponse(value))
	}
	return err
}

// startDurableJobReconciler owns the single low-concurrency durable worker.
// SQLite remains the queue and lease source of truth; no extra broker is
// required for a single VPS deployment.
func (a *AgentRuntime) startDurableJobReconciler(ctx context.Context) {
	a.workers.Add(2)
	go func() {
		defer a.workers.Done()
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		a.reconcileDurableJobs(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.reconcileDurableJobs(ctx)
			}
		}
	}()
	go func() {
		defer a.workers.Done()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.runOneDurableJob(ctx)
			}
		}
	}()
}

func (a *AgentRuntime) reconcileDurableJobs(ctx context.Context) {
	db := a.durableJobsDatabase()
	if db == nil || ensureDurableJobsSchema(db) != nil {
		return
	}
	now := mgmtNow()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,status FROM durable_jobs
		WHERE (status='planned' AND requires_approval=0 AND next_run_at IS NOT NULL AND julianday(next_run_at) <= julianday(?))
		   OR (status='retrying' AND (next_run_at IS NULL OR julianday(next_run_at) <= julianday(?)))
		   OR (status='leased' AND lease_expires_at IS NOT NULL AND julianday(lease_expires_at) <= julianday(?))
		   OR (status='running' AND lease_expires_at IS NOT NULL AND julianday(lease_expires_at) <= julianday(?))`, now, now, now, now)
	if err != nil {
		return
	}
	type dueJob struct{ id, status string }
	var due []dueJob
	for rows.Next() {
		var value dueJob
		if rows.Scan(&value.id, &value.status) != nil {
			rows.Close()
			return
		}
		due = append(due, value)
	}
	if rows.Close() != nil {
		return
	}
	for _, item := range due {
		target := durableJobQueued
		eventType := "scheduled"
		nextAction := "等待调度器领取"
		reason := "next_run_at reached"
		if item.status == durableJobRunning {
			target = durableJobUnknownManual
			eventType = "lease_expired"
			nextAction = "需要人工确认执行结果"
			reason = "running lease expired; side effect may be unknown"
		}
		if item.status == durableJobRetrying {
			target = durableJobQueued
			eventType = "retry_due"
			reason = "retry backoff elapsed"
		}
		if _, err = tx.ExecContext(ctx, `UPDATE durable_jobs SET status=?, next_action=?, lease_owner=NULL,
			lease_expires_at=NULL, heartbeat_at=NULL, updated_at=?, last_error=? WHERE id=? AND status=?`,
			target, nextAction, now, reason, item.id, item.status); err != nil {
			return
		}
		if err = a.appendDurableJobEvent(ctx, tx, item.id, eventType, item.status, target, reason, nextAction, "reconciler", nil); err != nil {
			return
		}
	}
	if len(due) > 0 {
		_ = tx.Commit()
	}
}

const durableJobLeaseDuration = 2 * time.Minute

func (a *AgentRuntime) claimDurableJob(ctx context.Context, workerID string) (durableJobRecord, bool, error) {
	db := a.durableJobsDatabase()
	if db == nil {
		return durableJobRecord{}, false, errors.New("runtime database is unavailable")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return durableJobRecord{}, false, err
	}
	defer tx.Rollback()
	now := mgmtNow()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM durable_jobs
		WHERE status='queued' AND requires_approval=0
		  AND (next_run_at IS NULL OR julianday(next_run_at) <= julianday(?))
		ORDER BY priority DESC, COALESCE(next_run_at, created_at), created_at LIMIT 1`, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return durableJobRecord{}, false, nil
	}
	if err != nil {
		return durableJobRecord{}, false, err
	}
	leaseUntil := time.Now().UTC().Add(durableJobLeaseDuration).Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE durable_jobs SET status='leased', lease_owner=?, lease_expires_at=?, heartbeat_at=?, attempts=attempts+1, started_at=COALESCE(started_at,?), updated_at=?, next_action='准备执行' WHERE id=? AND status='queued'`, workerID, leaseUntil, now, now, now, id)
	if err != nil {
		return durableJobRecord{}, false, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return durableJobRecord{}, false, nil
	}
	if err = a.appendDurableJobEvent(ctx, tx, id, "leased", durableJobQueued, durableJobLeased, "worker claimed job", "准备执行", workerID, map[string]any{"leaseExpiresAt": leaseUntil}); err != nil {
		return durableJobRecord{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return durableJobRecord{}, false, err
	}
	value, err := a.durableJobRecord(ctx, id)
	return value, true, err
}

func (a *AgentRuntime) setDurableJobRunning(ctx context.Context, jobID, workerID string) error {
	now := mgmtNow()
	leaseUntil := time.Now().UTC().Add(durableJobLeaseDuration).Format(time.RFC3339Nano)
	tx, err := a.durableJobsDatabase().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE durable_jobs SET status='running', lease_expires_at=?, heartbeat_at=?, next_action='正在执行', updated_at=? WHERE id=? AND status='leased' AND lease_owner=?`, leaseUntil, now, now, jobID, workerID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return coreInvalid("durable job lease is no longer active")
	}
	if err = a.appendDurableJobEvent(ctx, tx, jobID, "started", durableJobLeased, durableJobRunning, "worker started execution", "正在执行", workerID, nil); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *AgentRuntime) heartbeatDurableJob(ctx context.Context, jobID, workerID string) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := mgmtNow()
			leaseUntil := time.Now().UTC().Add(durableJobLeaseDuration).Format(time.RFC3339Nano)
			_, _ = a.durableJobsDatabase().ExecContext(ctx, `UPDATE durable_jobs SET heartbeat_at=?, lease_expires_at=?, updated_at=? WHERE id=? AND status='running' AND lease_owner=?`, now, leaseUntil, now, jobID, workerID)
		}
	}
}

func (a *AgentRuntime) durableJobRunRecord(ctx context.Context, job durableJobRecord) (runRecord, error) {
	runID, err := newCoreUUID()
	if err != nil {
		return runRecord{}, err
	}
	if a.aead == nil {
		return runRecord{}, errors.New("durable job encryption is unavailable")
	}
	inputCipher, err := a.encrypt([]byte(job.Goal))
	if err != nil {
		return runRecord{}, err
	}
	now := mgmtNow()
	conversation := strings.TrimSpace(job.SourceConversationRef)
	if conversation == "" {
		conversation = "durable:" + job.ID
	}
	transport := strings.TrimSpace(job.SourceTransport)
	if transport == "" {
		transport = "durable"
	}
	sender := strings.TrimSpace(job.OwnerID)
	if sender == "" {
		sender = "durable-worker"
	}
	_, err = a.db.ExecContext(ctx, `INSERT INTO agent_runs
		(id,event_id,message_id,transport,transport_instance,reply_handle,conversation_ref,conversation_kind,sender_ref,agent_instance_id,memory_namespace,persona_id,input_cipher,is_admin,is_wake,is_mention_bot,ownership_reason,state,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, runID, "durable-job:"+job.ID+":"+strconv.Itoa(job.Attempts), job.SourceMessageID, transport, job.BotID, "", conversation, "private", sender, job.BotID, job.WorkspaceID, job.BotID, inputCipher, 0, 1, 0, "durable_job", "running", now, now)
	if err != nil {
		return runRecord{}, err
	}
	// A durable task must never gain administrator authority merely because a
	// worker is executing it. The job's tool policy is evaluated as a member;
	// explicit high-risk work remains visible for approval/manual handling.
	return runRecord{ID: runID, EventID: "durable-job:" + job.ID + ":" + strconv.Itoa(job.Attempts), MessageID: job.SourceMessageID, Transport: transport, TransportInstance: job.BotID, ReplyHandle: "", ConversationRef: conversation, ConversationKind: "private", SenderRef: sender, AgentInstanceID: job.BotID, MemoryNamespace: job.WorkspaceID, PersonaID: job.BotID, InputCipher: inputCipher, State: "running", IsAdmin: false, IsWake: true, CreatedAt: now}, nil
}

func (a *AgentRuntime) finishDurableJob(ctx context.Context, job durableJobRecord, workerID string, run runRecord, reply agentReply, runErr error) error {
	status := durableJobSucceeded
	reason, nextAction := "执行完成", "已完成"
	if runErr != nil {
		if errors.Is(runErr, errTaskExecutionUncertain) || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			status, reason, nextAction = durableJobUnknownManual, runErr.Error(), "需要人工确认执行结果"
		} else if job.Attempts >= job.MaxAttempts {
			status, reason, nextAction = durableJobDeadLetter, runErr.Error(), "已超过最大重试次数"
		} else {
			status, reason, nextAction = durableJobRetrying, runErr.Error(), "等待自动重试"
		}
	}
	var nextRunValue any
	if runErr == nil {
		if nextRun, ok := durableJobNextRun(job.Plan, time.Now().UTC()); ok {
			status = durableJobPlanned
			reason = "本轮执行完成"
			nextAction = "等待下一次执行"
			nextRunValue = nextRun.Format(time.RFC3339Nano)
		}
	}
	now := mgmtNow()
	tx, err := a.durableJobsDatabase().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if runErr == nil {
		encoded, marshalErr := json.Marshal(reply)
		if marshalErr != nil {
			return marshalErr
		}
		ciphertext, encryptErr := a.encrypt(encoded)
		if encryptErr != nil {
			return encryptErr
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO durable_job_results(job_id,attempt,result_cipher,created_at) VALUES (?,?,?,?)`, job.ID, job.Attempts, ciphertext, now); err != nil {
			return err
		}
	}
	nextRun := nextRunValue
	if status == durableJobRetrying {
		backoff := time.Duration(1<<minInt(job.Attempts-1, 6)) * time.Minute
		nextRun = time.Now().UTC().Add(backoff).Format(time.RFC3339Nano)
	}
	finishedAt := any(nil)
	if status == durableJobSucceeded || status == durableJobDeadLetter || status == durableJobUnknownManual {
		finishedAt = now
	}
	result, err := tx.ExecContext(ctx, `UPDATE durable_jobs SET status=?, next_action=?, next_run_at=?, last_error=?, lease_owner=NULL, lease_expires_at=NULL, heartbeat_at=NULL, updated_at=?, finished_at=? WHERE id=? AND status='running' AND lease_owner=?`, status, nextAction, nextRun, strings.TrimSpace(reason), now, finishedAt, job.ID, workerID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return coreInvalid("durable job lease is no longer active")
	}
	if err = a.appendDurableJobEvent(ctx, tx, job.ID, "finished", durableJobRunning, status, reason, nextAction, workerID, map[string]any{"attempt": job.Attempts}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	// The runtime and control-plane databases are normally separate, but tests
	// and small installs may point both handles at the same SQLite file. Update
	// the synthetic run only after committing the job transaction so a single
	// connection cannot deadlock itself.
	if run.ID != "" {
		runState := "succeeded"
		if runErr != nil {
			runState = "failed"
		}
		_, _ = a.db.ExecContext(ctx, `UPDATE agent_runs SET state=?, error_code=?, input_cipher=NULL, updated_at=? WHERE id=?`, runState, strings.TrimSpace(reason), now, run.ID)
	}
	return nil
}

func (a *AgentRuntime) runOneDurableJob(ctx context.Context) {
	workerID := "durable-worker"
	job, claimed, err := a.claimDurableJob(ctx, workerID)
	if err != nil || !claimed {
		return
	}
	if err = a.setDurableJobRunning(ctx, job.ID, workerID); err != nil {
		return
	}
	job.Status = durableJobRunning
	run, err := a.durableJobRunRecord(ctx, job)
	if err != nil {
		_ = a.finishDurableJob(ctx, job, workerID, runRecord{}, agentReply{}, err)
		return
	}
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go a.heartbeatDurableJob(jobCtx, job.ID, workerID)
	var reply agentReply
	if a.durableJobRun != nil {
		reply, err = a.durableJobRun(jobCtx, run, job.Goal)
	} else {
		reply, err = a.generate(jobCtx, run, job.Goal)
	}
	if finishErr := a.finishDurableJob(ctx, job, workerID, run, reply, err); finishErr != nil {
		log.Printf("durable job %s finish failed: %v", job.ID, finishErr)
	}
}
