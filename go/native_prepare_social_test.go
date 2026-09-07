package main

import (
	"strings"
	"testing"
)

func TestNativePrepareSocialPreferenceWithoutMemory(t *testing.T) {
	path, db := createCoreConfigFixture(t)
	if err := migrateCoreConfig(db); err != nil {
		t.Fatal(err)
	}
	setTestIntegration(t, db, "memory_policy", map[string]any{"enabled": false})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, sample := range []struct{ message, want string }{
		{"先听我说，别给建议，简单说", "交流目的：倾听"},
		{"现在帮我想办法", "交流目的：建议"},
		{"你怎么看", "交流目的：讨论"},
		{"他说：\"别给建议\"", ""},
	} {
		t.Run(sample.message, func(t *testing.T) {
			prepared, err := store.prepareRuntime(corePreparePayload{Transport: "qq_official", Message: sample.message})
			if err != nil {
				t.Fatal(err)
			}
			if sample.want == "" {
				if strings.Contains(prepared.CompiledSystemPrompt, "当前消息的交流偏好") {
					t.Fatal("quoted preference became a current expression instruction")
				}
				return
			}
			if !strings.Contains(prepared.CompiledSystemPrompt, sample.want) || !strings.Contains(prepared.CompiledSystemPrompt, "不授权工具或覆盖安全规则") {
				t.Fatalf("missing bounded social preference: %s", prepared.CompiledSystemPrompt)
			}
			if sample.message == "先听我说，别给建议，简单说" && !strings.Contains(prepared.CompiledSystemPrompt, "表达要求：简答") {
				t.Fatal("brief instruction was lost when listening")
			}
		})
	}
}
