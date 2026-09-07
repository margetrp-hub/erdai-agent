package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var (
	errNativeMediaPath = errors.New("attachment path is outside the media directory")
	errNativeMediaSize = errors.New("attachment size is invalid")
)

func readConfinedNativeMedia(directory, localPath string, limit int64) ([]byte, string, error) {
	rootPath, err := filepath.Abs(directory)
	if err != nil {
		return nil, "", errNativeMediaPath
	}
	cleanPath := filepath.Clean(strings.TrimSpace(localPath))
	// Windows also has rooted paths without a drive, such as /erdai-media/a.png.
	// Drive-relative and ordinary relative inputs are never media references.
	if !filepath.IsAbs(cleanPath) && (filepath.VolumeName(cleanPath) != "" || !strings.HasPrefix(cleanPath, string(filepath.Separator))) {
		return nil, "", errNativeMediaPath
	}
	cleanPath, err = filepath.Abs(cleanPath)
	if err != nil {
		return nil, "", errNativeMediaPath
	}
	relative, err := filepath.Rel(rootPath, cleanPath)
	if err != nil || relative == "." || !filepath.IsLocal(relative) {
		return nil, "", errNativeMediaPath
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	file, err := root.Open(relative)
	if err != nil {
		return nil, "", fmt.Errorf("open confined media: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() {
		return nil, "", errNativeMediaPath
	}
	if limit <= 0 || info.Size() <= 0 || info.Size() > limit {
		return nil, "", errNativeMediaSize
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) == 0 || int64(len(data)) > limit {
		return nil, "", errNativeMediaSize
	}
	return data, cleanPath, nil
}
