package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPersonaVisualReferenceUploadPreviewPromptAndDelete(t *testing.T) {
	store, err := openCoreConfigStore(filepath.Join(t.TempDir(), "core.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.mediaDir = filepath.Join(t.TempDir(), "media")

	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "doubao-reference.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(png)
	_ = writer.WriteField("category", "identity")
	_ = writer.WriteField("label", "清透素颜主参考")
	_ = writer.WriteField("promptNotes", "稳定鹅蛋脸、深棕杏眼和乌黑长发，表情克制。")
	_ = writer.WriteField("isPrimary", "true")
	_ = writer.WriteField("sortOrder", "3")
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/personas/doubao/visual-references?namespace=default", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	if err = store.handlePersonaRequest(response, request, request.URL.Path); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusCreated {
		t.Fatalf("upload status = %d: %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data personaVisualReference `json:"data"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	item := envelope.Data
	if item.ID == "" || !item.IsPrimary || item.MediaType != "image" || item.SortOrder != 3 || item.Label != "清透素颜主参考" {
		t.Fatalf("uploaded reference = %+v", item)
	}
	if prompt := store.personaVisualReferencePrompt("doubao"); !strings.Contains(prompt, "深棕杏眼") {
		t.Fatalf("visual reference prompt = %q", prompt)
	}
	dataURI, err := store.primaryPersonaVisualReferenceDataURI("doubao")
	if err != nil || !strings.HasPrefix(dataURI, "data:image/png;base64,") {
		t.Fatalf("primary reference data URI = %q, err = %v", dataURI, err)
	}

	contentRequest := httptest.NewRequest(http.MethodGet, item.ContentURL, nil)
	contentResponse := httptest.NewRecorder()
	if err = store.handlePersonaRequest(contentResponse, contentRequest, contentRequest.URL.Path); err != nil {
		t.Fatal(err)
	}
	if contentResponse.Code != http.StatusOK || !bytes.Equal(contentResponse.Body.Bytes(), png) {
		t.Fatalf("content status = %d, bytes = %d", contentResponse.Code, contentResponse.Body.Len())
	}
	targetName := "参考形象测试角色"
	target, err := store.createPersona(corePersonaPayload{Name: &targetName})
	if err != nil {
		t.Fatal(err)
	}
	exportRequest := httptest.NewRequest(http.MethodGet, "/api/v1/personas/doubao/visual-references/export?namespace=default", nil)
	exportResponse := httptest.NewRecorder()
	if err = store.handlePersonaRequest(exportResponse, exportRequest, exportRequest.URL.Path); err != nil {
		t.Fatal(err)
	}
	if exportResponse.Code != http.StatusOK || exportResponse.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("export status = %d, content-type = %q", exportResponse.Code, exportResponse.Header().Get("Content-Type"))
	}
	archive, err := zip.NewReader(bytes.NewReader(exportResponse.Body.Bytes()), int64(exportResponse.Body.Len()))
	if err != nil || len(archive.File) != 2 {
		t.Fatalf("export archive = %d files, err = %v", len(archive.File), err)
	}
	var manifestFound, referenceFound bool
	for _, entry := range archive.File {
		if entry.Name == "manifest.json" {
			manifestFound = true
		}
		if strings.HasPrefix(entry.Name, "references/") {
			reader, openErr := entry.Open()
			if openErr != nil {
				t.Fatal(openErr)
			}
			archiveBytes, readErr := io.ReadAll(reader)
			_ = reader.Close()
			if readErr != nil || !bytes.Equal(archiveBytes, png) {
				t.Fatalf("exported reference bytes mismatch: %v", readErr)
			}
			referenceFound = true
		}
	}
	if !manifestFound || !referenceFound {
		t.Fatalf("export archive missing manifest or reference")
	}
	var importBody bytes.Buffer
	importWriter := multipart.NewWriter(&importBody)
	importPart, err := importWriter.CreateFormFile("package", "references.erdai.zip")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = importPart.Write(exportResponse.Body.Bytes())
	if err = importWriter.Close(); err != nil {
		t.Fatal(err)
	}
	importRequest := httptest.NewRequest(http.MethodPost, "/api/v1/personas/"+target.ID+"/visual-references/import?namespace=default", &importBody)
	importRequest.Header.Set("Content-Type", importWriter.FormDataContentType())
	importResponse := httptest.NewRecorder()
	if err = store.handlePersonaRequest(importResponse, importRequest, importRequest.URL.Path); err != nil {
		t.Fatal(err)
	}
	if importResponse.Code != http.StatusCreated {
		t.Fatalf("import status = %d: %s", importResponse.Code, importResponse.Body.String())
	}
	var importedEnvelope struct {
		Data struct {
			Items []personaVisualReference `json:"items"`
		} `json:"data"`
	}
	if err = json.Unmarshal(importResponse.Body.Bytes(), &importedEnvelope); err != nil {
		t.Fatal(err)
	}
	if len(importedEnvelope.Data.Items) != 1 || !importedEnvelope.Data.Items[0].IsPrimary {
		t.Fatalf("imported references = %+v", importedEnvelope.Data.Items)
	}
	cloned, err := store.clonePersonaVisualReferences("default", "doubao", target.ID)
	if err != nil || len(cloned) != 1 || !cloned[0].IsPrimary || cloned[0].PersonaID != target.ID {
		t.Fatalf("cloned references = %+v, err = %v", cloned, err)
	}
	var clonedStorageName string
	if err = store.db.QueryRow("SELECT storage_name FROM persona_visual_references WHERE id = ?", cloned[0].ID).Scan(&clonedStorageName); err != nil {
		t.Fatal(err)
	}
	clonedBytes, err := os.ReadFile(filepath.Join(store.mediaDir, clonedStorageName))
	if err != nil || !bytes.Equal(clonedBytes, png) {
		t.Fatalf("cloned reference bytes = %d, err = %v", len(clonedBytes), err)
	}

	var storageName string
	if err = store.db.QueryRow("SELECT storage_name FROM persona_visual_references WHERE id = ?", item.ID).Scan(&storageName); err != nil {
		t.Fatal(err)
	}
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/v1/personas/doubao/visual-references/"+item.ID+"?namespace=default", nil)
	deleteResponse := httptest.NewRecorder()
	if err = store.handlePersonaRequest(deleteResponse, deleteRequest, deleteRequest.URL.Path); err != nil {
		t.Fatal(err)
	}
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("delete status = %d: %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	if _, err = os.Stat(filepath.Join(store.mediaDir, storageName)); !os.IsNotExist(err) {
		t.Fatalf("deleted reference file still exists: %v", err)
	}
}

func TestPersonaVisualReferenceRejectsDisguisedMedia(t *testing.T) {
	if mediaType, _ := personaVisualReferenceMediaType("video/mp4", "text/plain", ".mp4", []byte("not a video")); mediaType != "" {
		t.Fatalf("disguised media accepted as %q", mediaType)
	}
}

func TestPersonaVisualReferenceStylePromptKeepsIdentitySeparate(t *testing.T) {
	store, err := openCoreConfigStore(filepath.Join(t.TempDir(), "core.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = store.db.Exec(`INSERT INTO persona_visual_references
		(id, persona_id, media_type, mime_type, original_name, storage_name, byte_size,
		 category, label, prompt_notes, is_primary, enabled, sort_order, created_at, updated_at)
		VALUES ('style-ref', 'doubao', 'video', 'video/mp4', 'style.mp4', 'style-ref.mp4', 3,
		 'style', '御姐风格参考', '只提取动作、镜头和氛围，不复制人物脸部或身份。', 0, 1, 4, ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	prompt := store.personaVisualReferencePrompt("doubao")
	if !strings.Contains(prompt, "参考风格（仅提取动作、镜头、光线、服装和氛围") {
		t.Fatalf("style reference prompt = %q", prompt)
	}
}

func TestPersonaVisualReferenceIsProtectedFromMediaGC(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := runtime.configStore.db.Exec(`INSERT INTO persona_visual_references
		(id, persona_id, media_type, mime_type, original_name, storage_name, byte_size,
		 category, label, prompt_notes, is_primary, enabled, sort_order, created_at, updated_at)
		VALUES ('ref-gc', 'doubao', 'image', 'image/png', 'reference.png', 'persona_ref_gc.png', 3,
		 'identity', '主参考', '', 1, 1, 0, ?, ?)`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	protected, err := runtime.protectedMediaFiles(time.Now().UTC(), time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !protected["persona_ref_gc.png"]["persona_reference"] {
		t.Fatalf("persona reference protection = %+v", protected)
	}
}
