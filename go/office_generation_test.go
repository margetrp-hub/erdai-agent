package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratedOfficeFilesCanBeReadBack(t *testing.T) {
	tests := []struct {
		format, title, content, extension string
		contains                          []string
	}{
		{"docx", "项目简报", "# 结论\n收入增长 20%", ".docx", []string{"项目简报", "收入增长 20%"}},
		{"pptx", "季度汇报", "# 第一页\n- 核心结论\n---\n# 第二页\n- 下一步", ".pptx", []string{"第 1 页", "核心结论", "第 2 页", "下一步"}},
		{"xlsx", "数据", "姓名,分数\n豆包,98", ".xlsx", []string{"A1=姓名", "B2=98"}},
	}
	for _, test := range tests {
		t.Run(test.format, func(t *testing.T) {
			var data []byte
			var err error
			switch test.format {
			case "docx":
				data, err = createDOCX(test.title, test.content)
			case "pptx":
				data, err = createPPTX(test.title, test.content)
			case "xlsx":
				data, err = createXLSX(test.title, test.content)
			}
			if err != nil {
				t.Fatal(err)
			}
			extracted, truncated, err := extractDocument(data, test.extension, 20000)
			if err != nil || truncated {
				t.Fatalf("extract = %q truncated=%v err=%v", extracted, truncated, err)
			}
			for _, expected := range test.contains {
				if !strings.Contains(extracted, expected) {
					t.Fatalf("%s missing %q: %s", test.format, expected, extracted)
				}
			}
		})
	}
}

func TestCreateOfficeDocumentStoresDeliverableAttachment(t *testing.T) {
	mediaDir := filepath.Join(t.TempDir(), "media")
	_, db := newTestCoreConfig(t)
	runtime := &AgentRuntime{configStore: &coreConfigStore{db: db}, mediaDir: mediaDir}
	defer runtime.configStore.Close()
	result, err := runtime.createOfficeDocument(context.Background(), map[string]any{
		"format": "docx", "title": "会议纪要", "content": "结论明确。", "filename": "纪要",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Attachments) != 1 || result.Attachments[0].Kind != "file" || result.Attachments[0].Name != "纪要.docx" || result.UserMessage == "" {
		t.Fatalf("office result = %+v", result)
	}
	if _, err = os.Stat(filepath.Join(mediaDir, filepath.Base(result.Attachments[0].LocalPath))); err != nil {
		t.Fatal(err)
	}
}
