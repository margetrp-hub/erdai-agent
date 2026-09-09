package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
)

const maxImageProviderResponseBytes = ((maxImageBytes + 2) / 3 * 4) + 1024*1024

func isGPTImageModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gpt-image-")
}

func (a *AgentRuntime) postGPTImageEdit(ctx context.Context, endpoint, key, model, prompt, reference string, target any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Appearance references and downloaded inbound attachments are already
	// local data URIs. Never turn an invalid reference into a text-only request.
	header, encoded, ok := strings.Cut(strings.TrimSpace(reference), ",")
	if !ok || !strings.HasPrefix(header, "data:image/") || !strings.HasSuffix(header, ";base64") {
		return errors.New("GPT Image edit requires a base64 image reference")
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(maxImageBytes) {
		return errors.New("GPT Image reference exceeds the image size limit")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > maxImageBytes {
		return errors.New("GPT Image reference data is invalid")
	}
	extension, mimeType := imageFormat(data)
	if (mimeType != "image/png" && mimeType != "image/jpeg" && mimeType != "image/webp") || header != "data:"+mimeType+";base64" {
		return errors.New("GPT Image reference must contain matching PNG, JPEG or WebP data")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, field := range [][2]string{{"model", model}, {"prompt", prompt}, {"n", "1"}} {
		if err := writer.WriteField(field[0], field[1]); err != nil {
			return err
		}
	}
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", `form-data; name="image"; filename="reference`+extension+`"`)
	partHeader.Set("Content-Type", mimeType)
	part, err := writer.CreatePart(partHeader)
	if err != nil {
		return err
	}
	if _, err = part.Write(data); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Accept", "application/json")
	// GPT Image returns base64 in JSON by default. Accommodate one image up to
	// the existing decoded limit, plus bounded response metadata.
	return a.doProviderRequest(request, target, maxImageProviderResponseBytes)
}
