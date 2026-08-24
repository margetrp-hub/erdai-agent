package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

type documentPolicy struct {
	Enabled                    bool `json:"enabled"`
	ImageUnderstandingEnabled  bool `json:"imageUnderstandingEnabled"`
	AllowText                  bool `json:"allowText"`
	AllowPDF                   bool `json:"allowPdf"`
	AllowDocx                  bool `json:"allowDocx"`
	AllowPptx                  bool `json:"allowPptx"`
	AllowXlsx                  bool `json:"allowXlsx"`
	MaxFileMB                  int  `json:"maxFileMb"`
	MaxExtractChars            int  `json:"maxExtractChars"`
	ExtractionTimeoutSeconds   int  `json:"extractionTimeoutSeconds"`
	RecentAttachmentTTLSeconds int  `json:"recentAttachmentTtlSeconds"`
	RecentAttachmentMax        int  `json:"recentAttachmentMax"`
	RecentAttachmentContextMax int  `json:"recentAttachmentContextMax"`
	MediaRetentionHours        int  `json:"mediaRetentionHours"`
	MediaGCIntervalMinutes     int  `json:"mediaGCIntervalMinutes"`
}

func defaultDocumentPolicy() documentPolicy {
	return documentPolicy{
		Enabled: true, ImageUnderstandingEnabled: true, AllowText: true,
		AllowPDF: true, AllowDocx: true, AllowPptx: true, AllowXlsx: true,
		MaxFileMB: 15, MaxExtractChars: 24000, ExtractionTimeoutSeconds: 90,
		RecentAttachmentTTLSeconds: 30 * 24 * 60 * 60,
		RecentAttachmentMax:        500,
		RecentAttachmentContextMax: 12,
	}
}

func (a *AgentRuntime) documentPolicy() documentPolicy {
	policy := defaultDocumentPolicy()
	if a == nil || a.configStore == nil {
		return policy
	}
	raw, err := a.configStore.integrationRaw("document_policy")
	if err == nil {
		_ = json.Unmarshal(raw, &policy)
	}
	if policy.MaxFileMB < 1 || policy.MaxFileMB > 100 {
		policy.MaxFileMB = 15
	}
	if policy.MaxExtractChars < 1000 || policy.MaxExtractChars > 200000 {
		policy.MaxExtractChars = 24000
	}
	if policy.ExtractionTimeoutSeconds < 1 || policy.ExtractionTimeoutSeconds > 300 {
		policy.ExtractionTimeoutSeconds = 90
	}
	if policy.RecentAttachmentTTLSeconds < 0 || policy.RecentAttachmentTTLSeconds > 365*24*60*60 {
		policy.RecentAttachmentTTLSeconds = 30 * 24 * 60 * 60
	}
	if policy.RecentAttachmentMax < 1 || policy.RecentAttachmentMax > 5000 {
		policy.RecentAttachmentMax = 500
	}
	if policy.RecentAttachmentContextMax < 1 || policy.RecentAttachmentContextMax > 50 {
		policy.RecentAttachmentContextMax = 12
	}
	if policy.MediaRetentionHours < 24 || policy.MediaRetentionHours > 5*365*24 {
		policy.MediaRetentionHours = 30 * 24
	}
	if policy.MediaGCIntervalMinutes < 15 || policy.MediaGCIntervalMinutes > 24*60 {
		policy.MediaGCIntervalMinutes = 60
	}
	return policy
}

func documentExtension(name string) string {
	return strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
}

func documentAllowed(extension string, policy documentPolicy) bool {
	if !policy.Enabled {
		return false
	}
	switch extension {
	case ".pdf":
		return policy.AllowPDF
	case ".docx":
		return policy.AllowDocx
	case ".pptx":
		return policy.AllowPptx
	case ".xlsx":
		return policy.AllowXlsx
	case ".txt", ".md", ".markdown", ".csv", ".tsv", ".json", ".xml", ".html", ".htm", ".yaml", ".yml", ".log":
		return policy.AllowText
	default:
		return false
	}
}

func extractPDF(data []byte, byteLimit int64) (text string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("PDF parser failed: %v", recovered)
		}
	}()
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	plain, err := reader.GetPlainText()
	if err != nil {
		return "", err
	}
	value, err := io.ReadAll(io.LimitReader(plain, byteLimit))
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(string(value))
	if text == "" {
		return "", errors.New("PDF contains no extractable text; OCR is required")
	}
	return text, nil
}

func readZipFile(archive *zip.Reader, name string, limit int64) ([]byte, error) {
	for _, file := range archive.File {
		if file.Name != name {
			continue
		}
		if file.UncompressedSize64 > uint64(limit) {
			return nil, errors.New("document part is too large")
		}
		reader, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		value, err := io.ReadAll(io.LimitReader(reader, limit+1))
		if err != nil {
			return nil, err
		}
		if int64(len(value)) > limit {
			return nil, errors.New("document part is too large")
		}
		return value, nil
	}
	return nil, errors.New("document part is missing")
}

func extractXMLText(value []byte) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(value))
	var output strings.Builder
	insideText := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if typed.Name.Local == "t" || typed.Name.Local == "v" {
				insideText = true
			}
			if typed.Name.Local == "tab" {
				output.WriteByte('\t')
			}
		case xml.CharData:
			if insideText {
				output.Write([]byte(typed))
			}
		case xml.EndElement:
			if typed.Name.Local == "t" || typed.Name.Local == "v" {
				insideText = false
			}
			if typed.Name.Local == "p" || typed.Name.Local == "tr" {
				output.WriteByte('\n')
			}
		}
	}
	return output.String(), nil
}

func extractDocx(archive *zip.Reader, partLimit int64) (string, error) {
	value, err := readZipFile(archive, "word/document.xml", partLimit)
	if err != nil {
		return "", err
	}
	return extractXMLText(value)
}

func extractPptx(archive *zip.Reader, partLimit int64) (string, error) {
	names := []string{}
	for _, file := range archive.File {
		if strings.HasPrefix(file.Name, "ppt/slides/slide") && strings.HasSuffix(file.Name, ".xml") {
			names = append(names, file.Name)
		}
	}
	sort.Strings(names)
	var output strings.Builder
	for index, name := range names {
		value, err := readZipFile(archive, name, partLimit)
		if err != nil {
			return "", err
		}
		text, err := extractXMLText(value)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&output, "第 %d 页\n%s\n", index+1, strings.TrimSpace(text))
	}
	if len(names) == 0 {
		return "", errors.New("presentation contains no slides")
	}
	return output.String(), nil
}

func extractSharedStrings(value []byte) ([]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(value))
	stringsList := []string{}
	var current strings.Builder
	insideItem, insideText := false, false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if typed.Name.Local == "si" {
				insideItem = true
				current.Reset()
			}
			if insideItem && typed.Name.Local == "t" {
				insideText = true
			}
		case xml.CharData:
			if insideItem && insideText {
				current.Write([]byte(typed))
			}
		case xml.EndElement:
			if typed.Name.Local == "t" {
				insideText = false
			}
			if typed.Name.Local == "si" {
				stringsList = append(stringsList, current.String())
				insideItem = false
			}
		}
	}
	return stringsList, nil
}

func extractWorksheet(value []byte, shared []string) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(value))
	var output strings.Builder
	cellRef, cellType, cellValue := "", "", ""
	insideCell, insideValue := false, false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if typed.Name.Local == "c" {
				insideCell, cellRef, cellType, cellValue = true, "", "", ""
				for _, attribute := range typed.Attr {
					if attribute.Name.Local == "r" {
						cellRef = attribute.Value
					} else if attribute.Name.Local == "t" {
						cellType = attribute.Value
					}
				}
			}
			if insideCell && (typed.Name.Local == "v" || typed.Name.Local == "t") {
				insideValue = true
			}
		case xml.CharData:
			if insideValue {
				cellValue += string(typed)
			}
		case xml.EndElement:
			if typed.Name.Local == "v" || typed.Name.Local == "t" {
				insideValue = false
			}
			if typed.Name.Local == "c" {
				value := cellValue
				if cellType == "s" {
					index, convertErr := strconv.Atoi(strings.TrimSpace(cellValue))
					if convertErr == nil && index >= 0 && index < len(shared) {
						value = shared[index]
					}
				}
				if strings.TrimSpace(value) != "" {
					fmt.Fprintf(&output, "%s=%s\t", cellRef, strings.TrimSpace(value))
				}
				insideCell = false
			}
			if typed.Name.Local == "row" {
				output.WriteByte('\n')
			}
		}
	}
	return output.String(), nil
}

func extractXlsx(archive *zip.Reader, partLimit int64) (string, error) {
	shared := []string{}
	if value, err := readZipFile(archive, "xl/sharedStrings.xml", partLimit); err == nil {
		shared, err = extractSharedStrings(value)
		if err != nil {
			return "", err
		}
	}
	names := []string{}
	for _, file := range archive.File {
		if strings.HasPrefix(file.Name, "xl/worksheets/sheet") && strings.HasSuffix(file.Name, ".xml") {
			names = append(names, file.Name)
		}
	}
	sort.Strings(names)
	var output strings.Builder
	for index, name := range names {
		value, err := readZipFile(archive, name, partLimit)
		if err != nil {
			return "", err
		}
		text, err := extractWorksheet(value, shared)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&output, "工作表 %d\n%s\n", index+1, strings.TrimSpace(text))
	}
	if len(names) == 0 {
		return "", errors.New("workbook contains no worksheets")
	}
	return output.String(), nil
}

func extractDocument(data []byte, extension string, maxChars int) (string, bool, error) {
	var text string
	var err error
	switch extension {
	case ".pdf":
		byteLimit := int64(maxChars)*4 + 4096
		text, err = extractPDF(data, byteLimit)
	case ".docx", ".pptx", ".xlsx":
		archive, openErr := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if openErr != nil {
			return "", false, openErr
		}
		partLimit := int64(len(data))*8 + 1024*1024
		if partLimit > 64*1024*1024 {
			partLimit = 64 * 1024 * 1024
		}
		switch extension {
		case ".docx":
			text, err = extractDocx(archive, partLimit)
		case ".pptx":
			text, err = extractPptx(archive, partLimit)
		case ".xlsx":
			text, err = extractXlsx(archive, partLimit)
		}
	default:
		if !utf8.Valid(data) {
			return "", false, errors.New("text attachment is not UTF-8")
		}
		text = string(data)
	}
	if err != nil {
		return "", false, err
	}
	text = strings.TrimSpace(strings.ReplaceAll(text, "\x00", ""))
	runes := []rune(text)
	truncated := len(runes) > maxChars
	if truncated {
		text = string(runes[:maxChars])
	}
	return text, truncated, nil
}

func documentFollowUpIntent(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	for _, marker := range []string{
		"附件", "文件", "文档", "pdf", "总结", "概括", "摘要", "提炼", "分析",
		"归纳", "要点", "重点", "待办", "结论", "这份", "刚才那份", "上一个",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func officeDocumentRequestIntent(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	if message == "" {
		return false
	}
	format := false
	for _, marker := range []string{"word", ".docx", "文档", "ppt", ".pptx", "幻灯片", "excel", ".xlsx", "表格", "csv"} {
		if strings.Contains(message, marker) {
			format = true
			break
		}
	}
	if !format {
		return false
	}
	create := false
	for _, marker := range []string{"做", "生成", "写", "导出", "创建", "制作", "整理成", "转成", "弄个"} {
		if strings.Contains(message, marker) {
			create = true
			break
		}
	}
	if !create {
		return false
	}
	for _, marker := range []string{"看看", "查看", "读取", "解析", "总结", "分析", "打开"} {
		if strings.Contains(message, marker) && !strings.Contains(message, "做") && !strings.Contains(message, "生成") && !strings.Contains(message, "导出") {
			return false
		}
	}
	return true
}

func imageFollowUpIntent(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	for _, marker := range []string{
		"\u539f\u578b\u662f\u8c01", "\u91cc\u9762\u4eba\u7269", "\u56fe\u4e2d\u4eba\u7269", "\u8fd9\u662f\u8c01",
		"\u4ed6\u662f\u8c01", "\u5979\u662f\u8c01", "\u51fa\u81ea\u54ea\u91cc", "\u4ec0\u4e48\u4f5c\u54c1",
		"\u89d2\u8272\u540d", "\u5361\u540d", "\u4eba\u7269\u6765\u6e90", "\u89d2\u8272\u6765\u6e90",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	for _, marker := range []string{
		"这是谁", "这张图", "这张图片", "这张照片", "看看这张图", "看看图片",
		"图里", "图中", "图片里", "照片里", "识图", "认一下这张", "刚才发的图",
		"上面的图", "刚才那张图",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func recentAttachmentFollowUpIntent(message string) bool {
	return documentFollowUpIntent(message) || imageFollowUpIntent(message)
}

func (a *AgentRuntime) rememberRecentDocuments(run runRecord) {
	if a == nil {
		return
	}
	policy := a.documentPolicy()
	if !policy.Enabled || policy.RecentAttachmentTTLSeconds == 0 {
		return
	}
	attachments := make([]transportAttachment, 0, len(run.Attachments))
	for _, attachment := range run.Attachments {
		attachment.SenderRef = run.SenderRef
		attachment.MessageID = run.MessageID
		attachment.ThreadKey = run.ThreadKey
		if attachment.Kind == "image" && policy.ImageUnderstandingEnabled && strings.TrimSpace(attachment.SourceURL) != "" {
			attachments = append(attachments, attachment)
			continue
		}
		if attachment.Kind == "file" && documentAllowed(documentExtension(attachment.Name), policy) {
			attachments = append(attachments, attachment)
		}
	}
	if len(attachments) == 0 {
		return
	}
	merged := attachments
	if previous := a.loadAllRecentAttachments(run); len(previous) > 0 {
		merged = append(merged, previous...)
	}
	seen := map[string]struct{}{}
	ordered := make([]transportAttachment, 0, len(merged))
	for _, attachment := range merged {
		identity := attachment.Kind + "\x00" + attachment.SourceURL + "\x00" + attachment.Name +
			"\x00" + attachment.SenderRef + "\x00" + attachment.MessageID + "\x00" + attachment.ThreadKey
		if _, exists := seen[identity]; exists {
			continue
		}
		seen[identity] = struct{}{}
		ordered = append(ordered, attachment)
		if len(ordered) >= policy.RecentAttachmentMax {
			break
		}
	}
	encoded, err := json.Marshal(ordered)
	if err != nil {
		return
	}
	ciphertext, err := a.encrypt(encoded)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	_, _ = a.db.Exec(`
		INSERT INTO agent_recent_attachments (
			agent_instance_id, transport, transport_instance, conversation_ref, attachments_cipher, expires_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_instance_id, transport, transport_instance, conversation_ref) DO UPDATE SET
			attachments_cipher = excluded.attachments_cipher,
			expires_at = excluded.expires_at,
			updated_at = excluded.updated_at
	`, runtimeInstanceScopeID(run), run.Transport, runtimeTransportInstanceScopeID(run), run.ConversationRef, ciphertext,
		now.Add(time.Duration(policy.RecentAttachmentTTLSeconds)*time.Second).Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano))
}

func (a *AgentRuntime) recentDocuments(run runRecord, message string) []transportAttachment {
	if a == nil || !recentAttachmentFollowUpIntent(message) {
		return nil
	}
	attachments := a.loadRecentAttachments(run)
	if len(attachments) == 0 {
		return nil
	}
	wantsImages := imageFollowUpIntent(message)
	wantsDocuments := documentFollowUpIntent(message)
	contextMax := a.documentPolicy().RecentAttachmentContextMax
	filtered := make([]transportAttachment, 0, len(attachments))
	appendMatch := func(attachment transportAttachment) {
		if len(filtered) >= contextMax {
			return
		}
		filtered = append(filtered, attachment)
	}
	// A quoted message is the strongest reference, even when another member
	// posted the original attachment in the same group.
	if run.ReplyToMessageID != "" {
		for _, attachment := range attachments {
			if attachment.MessageID == run.ReplyToMessageID &&
				((wantsImages && attachment.Kind == "image") || (wantsDocuments && attachment.Kind == "file")) {
				appendMatch(attachment)
			}
		}
		if len(filtered) > 0 {
			return filtered
		}
	}
	for _, attachment := range attachments {
		if len(filtered) >= contextMax {
			break
		}
		if attachment.SenderRef != "" && attachment.SenderRef != run.SenderRef {
			continue
		}
		if run.ReplyToMessageID != "" && attachment.MessageID == run.ReplyToMessageID {
			continue
		}
		if (wantsImages && attachment.Kind == "image") ||
			(wantsDocuments && attachment.Kind == "file") {
			appendMatch(attachment)
		}
	}
	return filtered
}

func (a *AgentRuntime) loadRecentAttachments(run runRecord) []transportAttachment {
	if a == nil || a.db == nil {
		return nil
	}
	attachments := a.loadAllRecentAttachments(run)
	if len(attachments) == 0 {
		return nil
	}
	filtered := make([]transportAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		if recentAttachmentMatchesScope(attachment, run) {
			filtered = append(filtered, attachment)
		}
	}
	return filtered
}

// loadAllRecentAttachments is only used while merging the encrypted conversation
// cache. Callers that build model context must use loadRecentAttachments so a
// member or thread never inherits another scope's attachment.
func (a *AgentRuntime) loadAllRecentAttachments(run runRecord) []transportAttachment {
	if a == nil || a.db == nil {
		return nil
	}
	var ciphertext []byte
	var expiresAt string
	err := a.db.QueryRow(`
		SELECT attachments_cipher, expires_at FROM agent_recent_attachments
		WHERE agent_instance_id = ? AND transport = ? AND transport_instance = ? AND conversation_ref = ?
	`, runtimeInstanceScopeID(run), run.Transport, runtimeTransportInstanceScopeID(run), run.ConversationRef).Scan(&ciphertext, &expiresAt)
	if err != nil {
		return nil
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !time.Now().UTC().Before(expires) {
		_, _ = a.db.Exec(`DELETE FROM agent_recent_attachments
			WHERE agent_instance_id = ? AND transport = ? AND transport_instance = ? AND conversation_ref = ?`,
			runtimeInstanceScopeID(run), run.Transport, runtimeTransportInstanceScopeID(run), run.ConversationRef)
		return nil
	}
	encoded, err := a.decrypt(ciphertext)
	if err != nil {
		return nil
	}
	var attachments []transportAttachment
	if json.Unmarshal(encoded, &attachments) != nil {
		return nil
	}
	return attachments
}

func recentAttachmentMatchesScope(attachment transportAttachment, run runRecord) bool {
	// A missing thread key is an explicit "no thread" scope. Do not let it
	// match a threaded message, and never allow a threaded attachment to leak
	// into an unthreaded conversation.
	if attachment.ThreadKey != run.ThreadKey {
		return false
	}
	// An explicit quote is a message-level reference. It may refer to another
	// member's attachment, but only after the thread scope has matched.
	if run.ReplyToMessageID != "" && attachment.MessageID == run.ReplyToMessageID {
		return true
	}
	return attachment.SenderRef == run.SenderRef
}

func (a *AgentRuntime) readDocumentAttachment(ctx context.Context, run runRecord, attachmentID string) (toolResult, error) {
	policy := a.documentPolicy()
	attachmentID = strings.TrimSpace(attachmentID)
	var attachment *transportAttachment
	for index := range run.Attachments {
		if run.Attachments[index].ID == attachmentID {
			attachment = &run.Attachments[index]
			break
		}
	}
	if attachment == nil || attachment.Kind != "file" {
		return toolResult{}, errors.New("document attachment was not found")
	}
	extension := documentExtension(attachment.Name)
	if !documentAllowed(extension, policy) {
		return toolResult{}, errors.New("document type is disabled or unsupported")
	}
	limit := int64(policy.MaxFileMB) * 1024 * 1024
	var data []byte
	var err error
	if strings.TrimSpace(attachment.LocalPath) != "" {
		data, _, err = a.readLocalMedia(attachment.LocalPath, limit)
	} else {
		endpoint, endpointErr := validateNativeMCPEndpoint(ctx, attachment.SourceURL, net.DefaultResolver)
		if endpointErr != nil {
			return toolResult{}, endpointErr
		}
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if requestErr != nil {
			return toolResult{}, requestErr
		}
		client := *a.client
		client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many document redirects")
			}
			_, redirectErr := validateNativeMCPEndpoint(request.Context(), request.URL.String(), net.DefaultResolver)
			return redirectErr
		}
		response, responseErr := client.Do(request)
		if responseErr != nil {
			return toolResult{}, responseErr
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return toolResult{}, fmt.Errorf("document download returned HTTP %d", response.StatusCode)
		}
		data, err = io.ReadAll(io.LimitReader(response.Body, limit+1))
	}
	if err != nil {
		return toolResult{}, err
	}
	if int64(len(data)) > limit {
		return toolResult{}, errors.New("document exceeds configured size limit")
	}
	text, truncated, err := extractDocument(data, extension, policy.MaxExtractChars)
	if err != nil {
		return toolResult{}, err
	}
	encoded, _ := json.Marshal(map[string]any{
		"ok": true, "attachmentId": attachment.ID, "name": attachment.Name,
		"content": text, "truncated": truncated,
	})
	return toolResult{Content: string(encoded)}, nil
}
