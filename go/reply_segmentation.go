package main

import "strings"

var replyClosingRunes = map[rune]bool{
	'"': true, '\'': true, '”': true, '’': true,
	'）': true, ')': true, '】': true, ']': true,
	'》': true, '〉': true, '」': true, '』': true,
}

func splitReplyText(text string, policy runtimeMessagePolicy, preserveFormatting bool) []string {
	cleaned := strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if cleaned == "" {
		return nil
	}
	if preserveFormatting || replyHasFormalLayout(cleaned) ||
		(policy.SegmentedReplyEnabled != nil && !*policy.SegmentedReplyEnabled) {
		return []string{cleaned}
	}

	minimum := boundedReplySetting(policy.SegmentMinChars, 10, 1, 80)
	maximum := boundedReplySetting(policy.SegmentMaxChars, 20, minimum, 120)
	if maximum < minimum {
		maximum = minimum
	}
	maxSegments := boundedReplySetting(policy.MaxReplySegments, 2, 1, 5)
	if runeCount(cleaned) <= maximum || maxSegments == 1 {
		return []string{cleaned}
	}

	units := naturalReplyUnits(cleaned)
	segments := make([]string, 0, min(len(units), maxSegments))
	current := ""
	for _, unit := range units {
		candidate := current + unit
		if current != "" && runeCount(candidate) > maximum {
			segments = append(segments, current)
			current = unit
			continue
		}
		current = candidate
	}
	if current != "" {
		segments = append(segments, current)
	}
	if len(segments) > maxSegments {
		tail := strings.Join(segments[maxSegments-1:], "")
		segments = append(segments[:maxSegments-1], tail)
	}
	return segments
}

func naturalReplyUnits(text string) []string {
	runes := []rune(text)
	units := make([]string, 0, 3)
	start := 0
	for index := 0; index < len(runes); index++ {
		if !isReplyBoundary(runes[index]) {
			continue
		}
		end := index + 1
		for end < len(runes) && replyClosingRunes[runes[end]] {
			end++
		}
		if unit := strings.TrimSpace(string(runes[start:end])); unit != "" {
			units = append(units, unit)
		}
		start = end
		index = end - 1
	}
	if tail := strings.TrimSpace(string(runes[start:])); tail != "" {
		units = append(units, tail)
	}
	if len(units) == 0 {
		return []string{strings.TrimSpace(text)}
	}
	return units
}

func isReplyBoundary(value rune) bool {
	switch value {
	case '。', '！', '？', '!', '?', '；', ';', '\n':
		return true
	default:
		return false
	}
}

func replyHasFormalLayout(text string) bool {
	lower := strings.ToLower(text)
	if strings.Contains(text, "`") || strings.Contains(text, "~~~") ||
		strings.Contains(lower, "https://") || strings.Contains(lower, "http://") ||
		strings.Contains(lower, "www.") {
		return true
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") ||
			strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "1. ") {
			return true
		}
	}
	return false
}

func boundedReplySetting(value, fallback, minimum, maximum int) int {
	if value == 0 {
		return fallback
	}
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func runeCount(value string) int {
	return len([]rune(value))
}
