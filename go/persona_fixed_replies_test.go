package main

import (
	"context"
	"testing"
)

func TestNovelRandomMessageAvoidsRecentNearDuplicates(t *testing.T) {
	candidates := []string{"弄好了，给你看看。", "成品到了。", "成品到了。"}
	for range 20 {
		if got := novelRandomMessage(candidates, []string{"弄好了，给你看看。"}); got != "成品到了。" {
			t.Fatalf("novel reply = %q", got)
		}
	}
}

func TestPersonaFixedReplyUsesPersonaSample(t *testing.T) {
	path, db := newTestCoreConfig(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := &AgentRuntime{configStore: store}
	for _, test := range []struct {
		persona string
		want    map[string]bool
	}{
		{"doubao", map[string]bool{"给你，刚做好。": true, "这张还算顺眼。": true, "好了，自己看。": true, "成品到了。": true, "我挑了这版。": true, "弄完了，看图。": true, "这回没糊弄你。": true, "收好，别挑太狠。": true}},
		{"xiaoman", map[string]bool{"好啦，给你看。": true, "这张我挺满意。": true, "给你，刚拍好的。": true, "这回状态还行吧。": true, "我选了这张。": true, "看，今天还不错吧。": true, "刚弄好，别挑太细。": true, "喏，自己看。": true}},
	} {
		got := runtime.personaFixedReply(context.Background(), runRecord{PersonaID: test.persona}, "image-completion", []string{"fallback"})
		if !test.want[got] {
			t.Fatalf("%s image completion = %q", test.persona, got)
		}
	}
}

func TestRuntimePersonaReplySamplesAreEditableRecords(t *testing.T) {
	path, db := newTestCoreConfig(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, persona := range []string{"doubao", "xiaoman"} {
		var count int
		if err := store.db.QueryRow(`SELECT count(*) FROM persona_samples
			WHERE persona_id = ? AND scene_tags_json LIKE '%runtime:%' AND enabled = 1`, persona).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count < 21 {
			t.Fatalf("%s runtime sample count = %d", persona, count)
		}
	}
}

func TestDeletedRuntimePersonaReplyDoesNotReturnAfterRestart(t *testing.T) {
	path, db := newTestCoreConfig(t)
	if _, err := db.Exec(`DELETE FROM persona_samples WHERE id = 'doubao-runtime-image-completion'`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var count int
	if err = store.db.QueryRow(`SELECT count(*) FROM persona_samples
		WHERE id = 'doubao-runtime-image-completion'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("deleted runtime reply sample was seeded again")
	}
}
