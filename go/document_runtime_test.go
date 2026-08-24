package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func simpleTextPDF(text string) []byte {
	content := "BT /F1 12 Tf 72 720 Td (" + text + ") Tj ET"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var output bytes.Buffer
	output.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for index := 1; index < len(offsets); index++ {
		fmt.Fprintf(&output, "%010d 00000 n \n", offsets[index])
	}
	fmt.Fprintf(&output, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return output.Bytes()
}

func officeArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	for name, content := range files {
		part, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestExtractDocumentSupportsOfficeAndText(t *testing.T) {
	docx := officeArchive(t, map[string]string{
		"word/document.xml": `<document><body><p><r><t>你好</t></r><r><tab/></r><r><t>豆包</t></r></p></body></document>`,
	})
	pptx := officeArchive(t, map[string]string{
		"ppt/slides/slide2.xml": `<sld><p><r><t>第二页</t></r></p></sld>`,
		"ppt/slides/slide1.xml": `<sld><p><r><t>第一页</t></r></p></sld>`,
	})
	xlsx := officeArchive(t, map[string]string{
		"xl/sharedStrings.xml":     `<sst><si><t>姓名</t></si><si><t>豆包</t></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData><row><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row></sheetData></worksheet>`,
	})

	tests := []struct {
		name, extension string
		data            []byte
		contains        []string
	}{
		{"docx", ".docx", docx, []string{"你好", "豆包"}},
		{"pptx", ".pptx", pptx, []string{"第 1 页", "第一页", "第 2 页", "第二页"}},
		{"xlsx", ".xlsx", xlsx, []string{"工作表 1", "A1=姓名", "B1=豆包"}},
		{"pdf", ".pdf", simpleTextPDF("Quarter revenue increased twenty percent."), []string{"Quarter revenue", "twenty percent"}},
		{"text", ".txt", []byte("一二三四五"), []string{"一二三四五"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			text, truncated, err := extractDocument(test.data, test.extension, 1000)
			if err != nil || truncated {
				t.Fatalf("extract = %q, truncated=%v, err=%v", text, truncated, err)
			}
			for _, expected := range test.contains {
				if !strings.Contains(text, expected) {
					t.Fatalf("extracted text missing %q: %s", expected, text)
				}
			}
		})
	}
	text, truncated, err := extractDocument([]byte("一二三四五"), ".txt", 3)
	if err != nil || !truncated || text != "一二三" {
		t.Fatalf("unicode truncation = %q, truncated=%v, err=%v", text, truncated, err)
	}
}

func TestReadDocumentAttachmentUsesOnlyCurrentFileAttachment(t *testing.T) {
	docx := officeArchive(t, map[string]string{
		"word/document.xml": `<document><body><p><r><t>季度收入增长百分之二十。</t></r></p></body></document>`,
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(docx)
	}))
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.Host = "localhost:" + endpoint.Port()

	_, db := newTestCoreConfig(t)
	runtime := &AgentRuntime{configStore: &coreConfigStore{db: db}, client: server.Client()}
	defer runtime.configStore.Close()
	run := runRecord{Attachments: []transportAttachment{{
		ID: "report", Kind: "file", SourceURL: endpoint.String(), Name: "report.docx",
	}}}
	result, err := runtime.readDocumentAttachment(context.Background(), run, "report")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		OK        bool   `json:"ok"`
		Content   string `json:"content"`
		Truncated bool   `json:"truncated"`
	}
	if err = json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || payload.Truncated || !strings.Contains(payload.Content, "季度收入增长") {
		t.Fatalf("document result = %+v", payload)
	}
	if _, err = runtime.readDocumentAttachment(context.Background(), run, "missing"); err == nil {
		t.Fatal("missing attachment was accepted")
	}
	run.Attachments[0].Kind = "image"
	if _, err = runtime.readDocumentAttachment(context.Background(), run, "report"); err == nil {
		t.Fatal("non-file attachment was accepted")
	}
}

func TestReadDocumentToolIsExposedOnlyForReadableDocuments(t *testing.T) {
	tool := runtimeTool{
		Name: "read_document", AdapterRef: "core:read_document", ApprovalMode: "auto",
		InputSchema: map[string]any{"type": "object"},
	}
	policy := runtimeToolPolicy{Authority: "member", Tools: []runtimeTool{tool}}
	if definitions := authorizedToolDefinitions(policy, false, "看看附件", false); len(definitions) != 0 {
		t.Fatalf("document tool leaked without attachment: %+v", definitions)
	}
	if definitions := authorizedToolDefinitions(policy, false, "看看附件", true); len(definitions) != 1 {
		t.Fatalf("document tool missing with attachment: %+v", definitions)
	}
	documentPolicy := defaultDocumentPolicy()
	if !hasReadableDocumentAttachment([]transportAttachment{{Kind: "file", Name: "report.pdf"}}, documentPolicy) {
		t.Fatal("PDF attachment was not treated as readable")
	}
	if !hasReadableDocumentAttachment([]transportAttachment{{Kind: "file", Name: "report.xlsx"}}, documentPolicy) {
		t.Fatal("XLSX attachment was not treated as readable")
	}
}

func TestRecentDocumentReferenceIsScopedAndRequiresFollowUpIntent(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	run := runRecord{
		Transport: "qq_official", ConversationRef: "private:member-1",
		Attachments: []transportAttachment{{
			ID: "report", Kind: "file", SourceURL: "https://example.com/report.pdf", Name: "report.pdf",
		}},
	}
	runtime.rememberRecentDocuments(run)

	continued := run
	continued.Attachments = nil
	attachments := runtime.recentDocuments(continued, "帮我总结一下")
	if len(attachments) != 1 || attachments[0].Name != "report.pdf" {
		t.Fatalf("recent attachment = %+v", attachments)
	}
	if values := runtime.recentDocuments(continued, "今天天气怎么样"); len(values) != 0 {
		t.Fatalf("unrelated message reused attachments: %+v", values)
	}
	continued.ConversationRef = "private:member-2"
	if values := runtime.recentDocuments(continued, "总结这份文件"); len(values) != 0 {
		t.Fatalf("attachment crossed conversation: %+v", values)
	}
}

func TestRecentImageReferenceIsScopedAndRequiresImageFollowUpIntent(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	run := runRecord{
		Transport: "qq_official", ConversationRef: "group:group-1",
		Attachments: []transportAttachment{{
			ID: "image-1", Kind: "image", SourceURL: "https://example.com/image.jpg", Name: "image.jpg",
		}},
	}
	runtime.rememberRecentDocuments(run)
	continued := run
	continued.Attachments = nil
	attachments := runtime.recentDocuments(continued, "@豆包 这是谁")
	if len(attachments) != 1 || attachments[0].Kind != "image" {
		t.Fatalf("recent image attachment = %+v", attachments)
	}
	if values := runtime.recentDocuments(continued, "今天天气怎么样"); len(values) != 0 {
		t.Fatalf("unrelated message reused image: %+v", values)
	}
	continued.ConversationRef = "group:group-2"
	if values := runtime.recentDocuments(continued, "看看这张图"); len(values) != 0 {
		t.Fatalf("image crossed conversation: %+v", values)
	}
}

func TestImageFollowUpIntentRecognizesOriginQuestions(t *testing.T) {
	for _, message := range []string{"告诉我里面人物的原型是谁", "这张图出自什么作品", "图中人物是谁"} {
		if !imageFollowUpIntent(message) {
			t.Fatalf("image origin question not recognized: %q", message)
		}
	}
}

func TestRecentAttachmentHistoryKeepsMultipleImagesInNewestFirstOrder(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	base := runRecord{AgentInstanceID: "instance-a", Transport: "qq_official", TransportInstance: "qq-a", ConversationRef: "group:group-1"}
	for _, name := range []string{"one.jpg", "two.jpg", "three.jpg"} {
		run := base
		run.Attachments = []transportAttachment{{Kind: "image", SourceURL: "https://example.com/" + name, Name: name}}
		runtime.rememberRecentDocuments(run)
	}
	continued := base
	images := runtime.recentDocuments(continued, "看看刚才发的图")
	if len(images) != 3 || images[0].Name != "three.jpg" || images[2].Name != "one.jpg" {
		t.Fatalf("recent image history = %+v", images)
	}
}

func TestRecentAttachmentsAreIsolatedBySenderThreadAndMessage(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	base := runRecord{AgentInstanceID: "instance-a", Transport: "qq_official", TransportInstance: "qq-a", ConversationRef: "group:group-1"}
	for _, value := range []struct {
		sender, thread, messageID, name string
	}{
		{sender: "member-a", thread: "thread-1", messageID: "message-a", name: "a.jpg"},
		{sender: "member-b", thread: "thread-1", messageID: "message-b", name: "b.jpg"},
		{sender: "member-a", thread: "thread-2", messageID: "message-c", name: "c.jpg"},
	} {
		run := base
		run.SenderRef, run.ThreadKey, run.MessageID = value.sender, value.thread, value.messageID
		run.Attachments = []transportAttachment{{Kind: "image", SourceURL: "https://example.com/" + value.name, Name: value.name}}
		runtime.rememberRecentDocuments(run)
	}
	for _, value := range []struct {
		sender, thread, want string
	}{
		{sender: "member-a", thread: "thread-1", want: "a.jpg"},
		{sender: "member-b", thread: "thread-1", want: "b.jpg"},
		{sender: "member-a", thread: "thread-2", want: "c.jpg"},
	} {
		run := base
		run.SenderRef, run.ThreadKey = value.sender, value.thread
		got := runtime.recentDocuments(run, "\u56fe\u4e2d\u4eba\u7269\u662f\u8c01")
		if len(got) != 1 || got[0].Name != value.want {
			t.Fatalf("scope %s/%s returned %+v, want %s", value.sender, value.thread, got, value.want)
		}
	}
	otherInstance := base
	otherInstance.AgentInstanceID = "instance-b"
	if values := runtime.recentDocuments(otherInstance, "\u56fe\u4e2d\u4eba\u7269\u662f\u8c01"); len(values) != 0 {
		t.Fatalf("attachment crossed agent instance: %+v", values)
	}
	quoted := base
	quoted.SenderRef, quoted.ThreadKey, quoted.ReplyToMessageID = "member-b", "thread-1", "message-a"
	got := runtime.recentDocuments(quoted, "\u56fe\u4e2d\u4eba\u7269\u662f\u8c01")
	if len(got) != 1 || got[0].Name != "a.jpg" {
		t.Fatalf("explicit quote did not resolve the referenced attachment: %+v", got)
	}
}

func TestOfficeDocumentRequestIntentSeparatesCreateFromRead(t *testing.T) {
	for _, message := range []string{"帮我做一个word，里面放豆包", "生成一份 Word 文档", "导出excel表格", "做个ppt"} {
		if !officeDocumentRequestIntent(message) {
			t.Fatalf("document creation not recognized: %q", message)
		}
	}
	for _, message := range []string{"我有个word附件", "帮我看看这个word", "读取这份文档"} {
		if officeDocumentRequestIntent(message) {
			t.Fatalf("document read was mistaken for creation: %q", message)
		}
	}
}
