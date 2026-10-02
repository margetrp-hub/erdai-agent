package main

import "testing"

func TestEffectiveParticipationModeUsesOneCanonicalSwitch(t *testing.T) {
	falseValue := false
	trueValue := true
	tests := []struct {
		name    string
		policy  groupParticipationPolicy
		profile personaRuntimeProfile
		want    string
	}{
		{name: "profile canonical wins", policy: groupParticipationPolicy{ParticipationMode: "adaptive"}, profile: personaRuntimeProfile{ParticipationMode: "addressed_only"}, want: "addressed_only"},
		{name: "instance canonical wins over legacy fields", policy: groupParticipationPolicy{ParticipationMode: "adaptive"}, profile: personaRuntimeProfile{ParticipationMode: "social", ProactiveEnabled: &falseValue, UnaddressedMode: "off"}, want: "social"},
		{name: "legacy profile off maps to addressed only", policy: groupParticipationPolicy{ParticipationMode: "adaptive"}, profile: personaRuntimeProfile{ProactiveEnabled: &trueValue, UnaddressedMode: "off"}, want: "addressed_only"},
		{name: "legacy social maps to social", policy: groupParticipationPolicy{ParticipationMode: "adaptive"}, profile: personaRuntimeProfile{ParticipationStyle: "social"}, want: "social"},
		{name: "global addressed only", policy: groupParticipationPolicy{ParticipationMode: "addressed_only", ProactiveChatEnabled: true}, want: "addressed_only"},
		{name: "legacy global off maps to addressed only", policy: groupParticipationPolicy{ProactiveChatEnabled: false}, want: "addressed_only"},
		{name: "default is adaptive", policy: groupParticipationPolicy{ProactiveChatEnabled: true}, want: "adaptive"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := effectiveParticipationMode(test.policy, test.profile); got != test.want {
				t.Fatalf("effectiveParticipationMode() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAgentInstanceCanonicalParticipationModeWinsLegacyOverrides(t *testing.T) {
	legacyEnabled := false
	got := mergeAgentRuntimeProfile(personaRuntimeProfile{}, personaRuntimeProfile{
		ParticipationMode:  "social",
		ProactiveEnabled:   &legacyEnabled,
		ParticipationStyle: "service",
		UnaddressedMode:    "off",
	})
	if got.ParticipationMode != "social" || got.ProactiveEnabled == nil || !*got.ProactiveEnabled ||
		got.ParticipationStyle != "social" || got.UnaddressedMode != "adaptive" {
		t.Fatalf("canonical participation mode was overridden by legacy fields: %+v", got)
	}
}

func TestDirectlyAddressesKeywordHonorsChineseWordBoundary(t *testing.T) {
	keywords := []string{"豆包"}
	tests := []struct {
		message string
		want    bool
	}{
		{"豆包", true},
		{"豆包，过来", true},
		{"豆包帮我看看", true},
		{"@豆包 说句话", true},
		{"豆包奶茶", false},
		{"豆包头像", false},
	}
	for _, test := range tests {
		t.Run(test.message, func(t *testing.T) {
			if got := directlyAddressesKeyword(test.message, keywords); got != test.want {
				t.Fatalf("directlyAddressesKeyword(%q) = %v, want %v", test.message, got, test.want)
			}
		})
	}
}
