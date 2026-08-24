package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

const (
	defaultLearningPoll = 10 * time.Minute
	minimumLearningPoll = time.Minute
	maximumLearningPoll = time.Hour
	maxLearningTopics   = 8
)

type grokLearningPolicy struct {
	Enabled               bool `json:"enabled"`
	LearningWorkerEnabled bool `json:"learningWorkerEnabled"`
	LearningPollSeconds   int  `json:"learningPollSeconds"`
}

type learningCandidate struct {
	ID        string
	Title     string
	Content   string
	SourceURI string
	Tags      []string
}

func (a *AgentRuntime) startLearningWorker(ctx context.Context) {
	a.workers.Add(1)
	go a.learningWorker(ctx)
}

func (a *AgentRuntime) learningWorker(ctx context.Context) {
	defer a.workers.Done()
	for {
		timer := time.NewTimer(a.learningPollInterval(ctx))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
		if _, err := a.collectLearningCandidates(ctx, time.Now().UTC()); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("Grok learning collection skipped: %v", err)
		}
	}
}

func (a *AgentRuntime) learningPollInterval(ctx context.Context) time.Duration {
	var policy grokLearningPolicy
	if err := a.integrationConfig(ctx, "grok_policy", &policy); err != nil || policy.LearningPollSeconds <= 0 {
		return defaultLearningPoll
	}
	interval := time.Duration(policy.LearningPollSeconds) * time.Second
	if interval < minimumLearningPoll {
		return minimumLearningPoll
	}
	if interval > maximumLearningPoll {
		return maximumLearningPoll
	}
	return interval
}

func (a *AgentRuntime) collectLearningCandidates(ctx context.Context, now time.Time) (int, error) {
	if strings.TrimSpace(a.grokAPIKey) == "" {
		return 0, nil
	}
	var policy grokLearningPolicy
	if err := a.integrationConfig(ctx, "grok_policy", &policy); err != nil {
		return 0, err
	}
	if !policy.Enabled || !policy.LearningWorkerEnabled {
		return 0, nil
	}
	config, err := a.configStore.runtimeConfig()
	if err != nil {
		return 0, err
	}
	if !config.LearningEnabled || len(config.LearningTopics) == 0 {
		return 0, nil
	}
	interval := time.Duration(config.LearningIntervalHours) * time.Hour
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	if config.LastCollectedAt != nil {
		last, parseErr := time.Parse(time.RFC3339Nano, *config.LastCollectedAt)
		if parseErr == nil && now.Sub(last) < interval {
			return 0, nil
		}
	}

	topics := config.LearningTopics
	if len(topics) > maxLearningTopics {
		topics = topics[:maxLearningTopics]
	}
	candidates := make([]learningCandidate, 0, len(topics))
	var collectionErrors []error
	cycle := now.Unix() / int64(interval/time.Second)
	for _, topic := range topics {
		topic = strings.TrimSpace(topic)
		if topic == "" {
			continue
		}
		query := fmt.Sprintf("截至 %s，检索并整理与“%s”相关的近期变化。只保留来源能支持的事实，标明不确定性和日期，给出来源 URL。", now.Format("2006-01-02"), topic)
		content, sources, researchErr := a.grokResearch(ctx, query)
		if researchErr != nil {
			collectionErrors = append(collectionErrors, fmt.Errorf("%s: %w", topic, researchErr))
			continue
		}
		digest := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", topic, cycle)))
		sourceURI := ""
		if len(sources) > 0 {
			sourceURI = sources[0].URL
		}
		candidates = append(candidates, learningCandidate{
			ID:        "grok-learning-" + hex.EncodeToString(digest[:12]),
			Title:     fmt.Sprintf("自动学习：%s（%s）", topic, now.Format("2006-01-02")),
			Content:   truncateRunes(content, 20000),
			SourceURI: sourceURI,
			Tags:      []string{"auto-learning", "grok", topic},
		})
	}
	if len(candidates) == 0 {
		return 0, errors.Join(collectionErrors...)
	}
	if err = a.configStore.storeLearningCandidates(candidates, now); err != nil {
		return 0, err
	}
	return len(candidates), errors.Join(collectionErrors...)
}

func (s *coreConfigStore) storeLearningCandidates(candidates []learningCandidate, collectedAt time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := collectedAt.UTC().Format(time.RFC3339Nano)
	for _, candidate := range candidates {
		if candidate.ID, err = mgmtIdentifier(candidate.ID, "id"); err != nil {
			return err
		}
		if candidate.Title, err = normalizeCoreText(candidate.Title, "title", 500, true); err != nil {
			return err
		}
		if candidate.Content, err = normalizeCoreText(candidate.Content, "content", 1000000, true); err != nil {
			return err
		}
		if candidate.SourceURI, err = normalizeCoreText(candidate.SourceURI, "sourceUri", 2000, false); err != nil {
			return err
		}
		if candidate.Tags, err = normalizeCoreStrings(candidate.Tags, "tags", 64, 100); err != nil {
			return err
		}
		result, execErr := tx.Exec(`
			INSERT OR IGNORE INTO knowledge_candidates (
				id, status, title, content, source_uri, tags_json, created_at, reviewed_at
			) VALUES (?, 'pending', ?, ?, ?, ?, ?, NULL)
		`, candidate.ID, candidate.Title, candidate.Content, candidate.SourceURI, mgmtJSON(candidate.Tags), now)
		if execErr != nil {
			return execErr
		}
		if inserted, _ := result.RowsAffected(); inserted > 0 {
			_, err = tx.Exec(`
				INSERT INTO audit_events (actor, action, target_type, target_id, details_json, created_at)
				VALUES ('system:grok-learning', 'create', 'knowledge_candidate', ?, ?, ?)
			`, candidate.ID, mgmtJSON(map[string]any{"tags": candidate.Tags}), now)
			if err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec(`
		UPDATE runtime_config SET last_collected_at = ?, updated_at = ? WHERE id = 1
	`, now, now); err != nil {
		return err
	}
	return tx.Commit()
}
