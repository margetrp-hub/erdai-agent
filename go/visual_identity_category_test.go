package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func uploadIdentityCategoryFixture(t *testing.T, runtime *AgentRuntime, library, category string, primary bool) (string, bool, error) {
	t.Helper()
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", category+".png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write(png)
	_ = writer.WriteField("category", category)
	_ = writer.WriteField("promptNotes", "reference-"+category)
	_ = writer.WriteField("isPrimary", fmt.Sprint(primary))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	if library == "" {
		item, err := runtime.configStore.uploadPersonaVisualReference(response, request, "default", "doubao")
		return item.ID, item.IsPrimary, err
	}
	item, err := runtime.configStore.uploadAppearanceLibraryReference(response, request, "default", library)
	return item.ID, item.IsPrimary, err
}

func TestVisualIdentityCategoryIsRequiredAcrossReferencePaths(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(fmt.Sprint("owned=", owned), func(t *testing.T) {
			runtime := newIdleRuntime(t)
			defer runtime.Close()
			runtime.configStore.mediaDir = t.TempDir()
			library := ""
			if owned {
				library = "persona-appearance-doubao"
			}
			outfitID, primary, err := uploadIdentityCategoryFixture(t, runtime, library, "outfit", false)
			if err != nil {
				t.Fatal(err)
			}
			if primary {
				t.Fatal("outfit was automatically promoted to identity primary")
			}
			if _, _, err = uploadIdentityCategoryFixture(t, runtime, library, "scene", true); err == nil {
				t.Fatal("scene accepted as a primary identity reference")
			}
			primaryValue := true
			if owned {
				_, _, err = runtime.configStore.updateAppearanceLibraryReference("default", library, outfitID, personaVisualReferencePayload{IsPrimary: &primaryValue}, nil)
			} else {
				_, _, err = runtime.configStore.updatePersonaVisualReference("default", "doubao", outfitID, personaVisualReferencePayload{IsPrimary: &primaryValue}, nil)
			}
			if err == nil {
				t.Fatal("update accepted outfit as a primary identity reference")
			}
			if _, err = runtime.configStore.db.Exec("UPDATE personas SET avatar_data_uri='data:image/png;base64,b2xk' WHERE id='doubao'"); err != nil {
				t.Fatal(err)
			}
			if _, err = runtime.resolveVisualAppearance(context.Background(), runRecord{PersonaID: "doubao"}, "来一张你的自拍", "image"); err == nil {
				t.Fatal("selfie used non-identity material when identity was absent")
			}
			if uri, err := runtime.configStore.primaryPersonaVisualReferenceDataURI("doubao"); err != nil || uri != "" {
				t.Fatalf("legacy reference path used outfit/avatar: present=%v err=%v", uri != "", err)
			}
			if _, _, err = uploadIdentityCategoryFixture(t, runtime, library, "identity", false); err != nil {
				t.Fatal(err)
			}
			table := "persona_visual_references"
			if owned {
				table = "appearance_library_references"
			}
			// Malformed historical primary flags must not beat a valid identity image.
			if _, err = runtime.configStore.db.Exec("UPDATE " + table + " SET is_primary=0"); err != nil {
				t.Fatal(err)
			}
			if _, err = runtime.configStore.db.Exec("UPDATE "+table+" SET is_primary=1, sort_order=0 WHERE id=?", outfitID); err != nil {
				t.Fatal(err)
			}
			snapshot, err := runtime.resolveVisualAppearance(context.Background(), runRecord{PersonaID: "doubao"}, "来一张你的自拍", "image")
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Reference == "" || !strings.Contains(snapshot.Identity, "reference-identity") {
				t.Fatal("valid identity did not take precedence over historical outfit primary")
			}
		})
	}
}
