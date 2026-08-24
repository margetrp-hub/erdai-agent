package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

func (s *MemoryGroupStore) RelationshipWithPulse(
	ctx context.Context,
	conversation, sender, personaID string,
	policy runtimeMemoryPolicy,
) (RelationshipState, bool, error) {
	state, found, err := s.Relationship(ctx, conversation, sender)
	if err != nil || !found || !policy.RelationshipPulseEnabled {
		return state, found, err
	}
	state.Pulse = s.relationshipPulse(
		ctx, personaID, sender,
		s.digest("conversation", conversation), s.digest("sender", sender),
		state, policy,
	)
	return state, true, nil
}

func (s *MemoryGroupStore) relationshipPulse(
	ctx context.Context,
	personaID, senderRef string,
	conversationDigest, senderDigest []byte,
	state RelationshipState,
	policy runtimeMemoryPolicy,
) *RelationshipPulse {
	pulse := &RelationshipPulse{PreferredHour: -1, ReplyCount: state.ReplyCount}
	if !policy.RelationshipPulseEnabled {
		pulse.Evidence = "关系脉动已关闭"
		return pulse
	}

	eventTimes := s.relationshipEventTimes(ctx, conversationDigest, senderDigest, policy.RhythmWindowEvents)
	pulse.RecentInteractions = len(eventTimes)
	pulse.Ready = state.InteractionCount >= policy.PulseMinInteractions && len(eventTimes) >= policy.PulseMinInteractions
	if !state.LastInteraction.IsZero() {
		pulse.HoursSinceInteraction = roundPulseValue(math.Max(0, s.now().Sub(state.LastInteraction).Hours()))
	}

	memoryCount, memoryKinds, accessCount, averageImportance, averageConfidence := s.relationshipMemoryStats(ctx, personaID, senderRef)
	pulse.MemoryCount = memoryCount
	pulse.MemoryKinds = memoryKinds
	if policy.MemoryResonanceEnabled && memoryCount > 0 {
		countSignal := 1 - math.Exp(-float64(memoryCount)/5)
		accessSignal := 1 - math.Exp(-float64(accessCount)/8)
		pulse.MemoryResonance = roundPulse(100 * (countSignal*0.42 + accessSignal*0.33 + averageImportance*0.25))
	}
	if memoryCount > 0 {
		kindCoverage := math.Min(1, float64(memoryKinds)/math.Min(5, float64(memoryCount)))
		pulse.BucketHealth = roundPulse(100 * (kindCoverage*0.45 + averageConfidence*0.35 + averageImportance*0.20))
	}

	if policy.OutputFeedbackEnabled {
		denominator := state.AddressedCount
		if denominator == 0 {
			denominator = state.InteractionCount
		}
		replyRatio := math.Min(1, float64(state.ReplyCount)/float64(maxInt(1, denominator)))
		evidence := math.Min(1, float64(denominator)/8)
		pulse.OutputReflow = roundPulse(100 * replyRatio * (0.55 + 0.45*evidence))
	}

	typicalGap, regularity, preferredHour := relationshipRhythm(eventTimes, policy.TimezoneOffsetMinutes)
	pulse.TypicalGapHours = roundPulseValue(typicalGap)
	pulse.PreferredHour = preferredHour
	if policy.CircadianAwarenessEnabled && pulse.Ready {
		pulse.RoutineExpectation = roundPulse(100 * regularity)
	}

	if policy.LongingEnabled && pulse.Ready && state.Intimacy >= 20 && typicalGap > 0 {
		ratio := pulse.HoursSinceInteraction / math.Max(6, typicalGap)
		arrivalGap := clampPulse((ratio-0.65)/1.75, 0, 1)
		pulse.Longing = roundPulse(math.Min(88, state.Intimacy*arrivalGap))
	}

	addressedRatio := float64(state.AddressedCount) / float64(maxInt(1, state.InteractionCount))
	pulse.Sharing = roundPulse(100 * clampPulse(
		state.Intimacy/100*0.48+addressedRatio*0.22+pulse.MemoryResonance/100*0.20+pulse.OutputReflow/100*0.10,
		0, 1,
	))
	pulse.Evidence = relationshipPulseEvidence(*pulse, policy.PulseMinInteractions)
	return pulse
}

func (s *MemoryGroupStore) relationshipEventTimes(ctx context.Context, conversationDigest, senderDigest []byte, limit int) []time.Time {
	if limit < 10 {
		limit = 60
	}
	rows, err := s.runtime.db.QueryContext(ctx, `
		SELECT occurred_at FROM relationship_events
		WHERE conversation_digest = ? AND sender_digest = ?
		ORDER BY occurred_at DESC LIMIT ?
	`, conversationDigest, senderDigest, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	result := make([]time.Time, 0, limit)
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) != nil {
			return nil
		}
		if value, parseErr := parseStoreTime(raw); parseErr == nil {
			result = append(result, value)
		}
	}
	return result
}

func (s *MemoryGroupStore) relationshipMemoryStats(ctx context.Context, personaID, senderRef string) (count, kinds, accesses int, importance, confidence float64) {
	if strings.TrimSpace(personaID) == "" || strings.TrimSpace(senderRef) == "" {
		return
	}
	err := s.runtime.db.QueryRowContext(ctx, `
		SELECT count(*), count(DISTINCT kind), COALESCE(sum(access_count), 0),
			COALESCE(avg(importance), 0), COALESCE(avg(confidence), 0)
		FROM agent_memories
		WHERE scope_digest = ? AND (expires_at IS NULL OR expires_at > ?)
	`, s.digest("scope", personaMemoryScope(personaID, "user", senderRef)), formatStoreTime(s.now().UTC())).Scan(
		&count, &kinds, &accesses, &importance, &confidence,
	)
	if err != nil {
		return 0, 0, 0, 0, 0
	}
	return
}

func relationshipRhythm(values []time.Time, timezoneOffsetMinutes int) (typicalGap, regularity float64, preferredHour int) {
	preferredHour = -1
	if len(values) < 2 {
		return 0, 0, preferredHour
	}
	gaps := make([]float64, 0, len(values)-1)
	for index := 1; index < len(values); index++ {
		gap := values[index-1].Sub(values[index]).Hours()
		if gap > 0 && gap <= 24*90 {
			gaps = append(gaps, gap)
		}
	}
	if len(gaps) == 0 {
		return 0, 0, preferredHour
	}
	typicalGap = medianPulse(gaps)
	deviation := make([]float64, 0, len(gaps))
	for _, gap := range gaps {
		deviation = append(deviation, math.Abs(gap-typicalGap))
	}
	gapRegularity := 1 - math.Min(1, medianPulse(deviation)/math.Max(6, typicalGap))

	location := time.FixedZone("memory-pulse", timezoneOffsetMinutes*60)
	hours := [24]int{}
	for _, value := range values {
		hours[value.In(location).Hour()]++
	}
	bestCount := 0
	for hour := 0; hour < 24; hour++ {
		count := hours[(hour+23)%24] + hours[hour] + hours[(hour+1)%24]
		if count > bestCount {
			bestCount = count
			preferredHour = hour
		}
	}
	hourConcentration := float64(bestCount) / float64(len(values))
	evidence := math.Min(1, float64(len(values))/12)
	regularity = clampPulse((gapRegularity*0.55+hourConcentration*0.45)*evidence, 0, 1)
	return typicalGap, regularity, preferredHour
}

func relationshipPulseEvidence(pulse RelationshipPulse, minimum int) string {
	if !pulse.Ready {
		return fmt.Sprintf("还需至少 %d 次有效互动，当前证据不足", minimum)
	}
	parts := []string{fmt.Sprintf("近窗 %d 次互动", pulse.RecentInteractions)}
	if pulse.MemoryCount > 0 {
		parts = append(parts, fmt.Sprintf("%d 条稳定记忆", pulse.MemoryCount))
	}
	if pulse.TypicalGapHours > 0 {
		parts = append(parts, fmt.Sprintf("常见间隔约 %.0f 小时", pulse.TypicalGapHours))
	}
	return strings.Join(parts, " · ")
}

func relationshipPulsePrompt(pulse RelationshipPulse) string {
	if !pulse.Ready {
		return "证据不足，按普通关系自然交流，不擅自表现熟悉。"
	}
	parts := make([]string, 0, 5)
	if pulse.MemoryResonance >= 55 {
		parts = append(parts, "可以自然接住已确认的偏好和共同经历")
	}
	if pulse.RoutineExpectation >= 55 && pulse.PreferredHour >= 0 {
		parts = append(parts, "互动节律较稳定，但不要声称监控作息")
	}
	if pulse.Longing >= 42 {
		parts = append(parts, "可以有一点自然挂念，但不催促、不责备、不报时长")
	}
	if pulse.Sharing >= 58 {
		parts = append(parts, "可主动分享一个与当前话题有关的小细节")
	}
	if pulse.OutputReflow < 30 {
		parts = append(parts, "降低追问密度，给对方留出回应空间")
	}
	if len(parts) == 0 {
		return "维持当前关系距离，顺着本轮内容自然回应。"
	}
	return strings.Join(parts, "；") + "。"
}

func imaginaryMemoryContext(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	for _, marker := range []string{
		"梦到", "做梦", "梦里", "噩梦", "幻想", "脑补", "假如", "假设", "如果我是",
		"设定里", "小说里", "游戏里", "角色扮演", "开玩笑", "编个故事", "fictional",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func medianPulse(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	copyValues := append([]float64(nil), values...)
	sort.Float64s(copyValues)
	middle := len(copyValues) / 2
	if len(copyValues)%2 == 1 {
		return copyValues[middle]
	}
	return (copyValues[middle-1] + copyValues[middle]) / 2
}

func clampPulse(value, minimum, maximum float64) float64 {
	return math.Max(minimum, math.Min(maximum, value))
}

func roundPulse(value float64) float64 {
	return math.Round(clampPulse(value, 0, 100)*10) / 10
}

func roundPulseValue(value float64) float64 {
	return math.Round(math.Max(0, value)*10) / 10
}
