package main

import (
	"strings"
	"testing"
)

func TestXiaomanIsARealSecondPersonaWithoutDoubaoPrompt(t *testing.T) {
	path, db := newTestCoreConfig(t)
	defer db.Close()
	if err := migrateCoreConfig(db); err != nil {
		t.Fatal(err)
	}
	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	persona, found, err := store.persona("default", "xiaoman")
	if err != nil || !found {
		t.Fatalf("xiaoman persona missing: found=%v err=%v", found, err)
	}
	if persona.Name != "小满" || strings.Contains(persona.SystemPrompt, "群高级管家") || strings.Contains(persona.SystemPrompt, "豆包") {
		t.Fatalf("xiaoman is still coupled to doubao: %+v", persona)
	}
	for _, expected := range []string{"会撒娇", "脾气来得快", "不是客服、百科", "不裸露", "坦率说"} {
		if !strings.Contains(persona.SystemPrompt, expected) {
			t.Fatalf("xiaoman prompt missing %q: %s", expected, persona.SystemPrompt)
		}
	}
	if persona.CharacterVersion != "1.3.1" || !strings.Contains(persona.VisualDescription, "明确成年") ||
		!strings.Contains(persona.VisualDescription, "不得读取或复用豆包") || !strings.Contains(persona.VisualDescription, "场景、机位、动作") {
		t.Fatalf("xiaoman visual boundary = %s", persona.VisualDescription)
	}
	profile, err := store.personaRuntimeProfile("xiaoman")
	if err != nil || profile.MaxReplyChars == nil || *profile.MaxReplyChars != 64 {
		t.Fatalf("xiaoman runtime profile = %+v, err=%v", profile, err)
	}
	samples, err := store.selectPersonaSamples("xiaoman", "你觉得这两个哪个好", 2)
	if err != nil || len(samples) == 0 || samples[0].ID != "xiaoman-sample-opinion" {
		t.Fatalf("xiaoman samples = %+v, err=%v", samples, err)
	}
	teaseSamples, err := store.selectPersonaSamples("xiaoman", "今天穿丝袜拍个自拍给我看看", 4)
	if err != nil || len(teaseSamples) == 0 {
		t.Fatalf("xiaoman visual samples = %+v, err=%v", teaseSamples, err)
	}
	knowledgeSamples, err := store.selectPersonaSamples("xiaoman", "谁知道这个为什么，解释一下", 6)
	if err != nil || len(knowledgeSamples) == 0 {
		t.Fatalf("xiaoman selective knowledge samples = %+v, err=%v", knowledgeSamples, err)
	}
	foundSelective := false
	for _, sample := range knowledgeSamples {
		if sample.ID == "xiaoman-sample-not-encyclopedia" {
			foundSelective = true
		}
	}
	if !foundSelective {
		t.Fatalf("xiaoman selective knowledge sample not recalled: %+v", knowledgeSamples)
	}
	doubaoSamples, err := store.selectPersonaSamples("doubao", "你觉得这两个哪个好", 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range doubaoSamples {
		if strings.HasPrefix(sample.ID, "xiaoman-") {
			t.Fatalf("xiaoman sample leaked into doubao: %s", sample.ID)
		}
	}
}
