package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func createEscapingMediaLink(t *testing.T, mediaDirectory, outside string) string {
	t.Helper()
	link := filepath.Join(mediaDirectory, "outside-link.png")
	if err := os.Symlink(outside, link); err == nil {
		t.Cleanup(func() { _ = os.Remove(link) })
		return link
	} else if runtime.GOOS != "windows" || !errors.Is(err, syscall.Errno(1314)) {
		t.Fatalf("create media escape symlink: %v", err)
	}
	// Directory junctions need no symlink privilege and exercise a real reparse escape.
	junction := filepath.Join(mediaDirectory, "outside-junction")
	createMediaDirectoryLink(t, filepath.Dir(outside), junction)
	t.Log("symlink privilege unavailable; exercising a real outside-directory junction")
	return filepath.Join(junction, filepath.Base(outside))
}

func createMediaDirectoryLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS != "windows" || !errors.Is(err, syscall.Errno(1314)) {
			t.Fatalf("create media directory link: %v", err)
		}
		if !filepath.IsAbs(target) || !filepath.IsAbs(link) || strings.ContainsAny(target+link, "\"&|<>%^\r\n") {
			t.Fatal("junction fixture requires explicit safe absolute paths")
		}
		if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
			t.Fatalf("create media directory junction: %v: %s", err, output)
		}
	}
	t.Cleanup(func() { _ = os.Remove(link) })
}

func TestMediaPathNativeSeparatorsAndContainment(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "media")
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "nested", "image.png")
	if err := os.WriteFile(inside, []byte("media-data"), 0600); err != nil {
		t.Fatal(err)
	}
	valid := []string{inside, filepath.ToSlash(inside)}
	if runtime.GOOS == "windows" {
		valid = append(valid, strings.ToUpper(inside))
		currentRoot, err := filepath.Abs(string(filepath.Separator))
		if err != nil {
			t.Fatal(err)
		}
		if strings.EqualFold(filepath.VolumeName(currentRoot), filepath.VolumeName(inside)) {
			valid = append(valid, strings.TrimPrefix(inside, filepath.VolumeName(inside)))
		}
	}
	for _, name := range valid {
		data, clean, err := readConfinedNativeMedia(root, name, 64)
		if err != nil || string(data) != "media-data" || !filepath.IsAbs(clean) {
			t.Fatalf("valid native media %q: %q, %q, %v", name, data, clean, err)
		}
	}
	outside := filepath.Join(parent, "outside.png")
	if err := os.WriteFile(outside, []byte("outside-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(parent, "media-evil")
	if err := os.Mkdir(sibling, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "image.png"), []byte("sibling-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{outside, root + string(filepath.Separator) + ".." + string(filepath.Separator) + "outside.png", filepath.Join(sibling, "image.png"), root, "image.png", filepath.Join(root, "nested")} {
		if data, _, err := readConfinedNativeMedia(root, name, 64); err == nil || len(data) != 0 {
			t.Fatalf("outside or non-file path accepted: %q, %q, %v", name, data, err)
		}
	}
	if runtime.GOOS == "windows" {
		for _, name := range []string{filepath.VolumeName(inside) + "relative.png", filepath.Join(root, "NUL"), filepath.Join(root, "image.png:private")} {
			if data, _, err := readConfinedNativeMedia(root, name, 64); err == nil || len(data) != 0 {
				t.Fatalf("drive-relative/device/stream path accepted: %q", name)
			}
		}
	}
}

func TestMediaPathRejectsRealSymlinkOrJunctionEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private.png")
	if err := os.WriteFile(outside, []byte("outside-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := createEscapingMediaLink(t, root, outside)
	if data, err := os.ReadFile(link); err != nil || string(data) != "outside-secret" {
		t.Fatalf("escape fixture not readable without confinement: %q, %v", data, err)
	}
	if data, _, err := readConfinedNativeMedia(root, link, 64); err == nil || len(data) != 0 {
		t.Fatalf("real filesystem link escaped confinement: %q, %v", data, err)
	}
}

func TestMediaPathAllowsInternalRelativeLinkAndEnforcesSize(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "image.png")
	if err := os.WriteFile(inside, []byte("inside-data"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "inside-link.png")
	if err := os.Symlink("image.png", link); err != nil {
		if runtime.GOOS != "windows" || !errors.Is(err, syscall.Errno(1314)) {
			t.Fatal(err)
		}
		if err := os.Link(inside, link); err != nil {
			t.Fatalf("create an in-root hard-link alias without symlink privilege: %v", err)
		}
		t.Log("symlink privilege unavailable; checking an in-root hard-link alias")
	}
	if data, _, err := readConfinedNativeMedia(root, link, 64); err != nil || string(data) != "inside-data" {
		t.Fatalf("in-root relative link rejected: %q, %v", data, err)
	}
	if _, _, err := readConfinedNativeMedia(root, inside, 4); !errors.Is(err, errNativeMediaSize) {
		t.Fatalf("oversize file accepted: %v", err)
	}
	empty := filepath.Join(root, "empty.png")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readConfinedNativeMedia(root, empty, 64); !errors.Is(err, errNativeMediaSize) {
		t.Fatalf("empty file accepted: %v", err)
	}
}

func TestMediaPathConfiguredRootMayBeDirectoryLink(t *testing.T) {
	parent := t.TempDir()
	actual := filepath.Join(parent, "actual")
	if err := os.Mkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(actual, "image.png"), []byte("configured-root-data"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, []byte("outside-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	// Create child links before the root alias so Windows need not create a junction through another junction.
	actualEscape := createEscapingMediaLink(t, actual, outside)
	relativeEscape, err := filepath.Rel(actual, actualEscape)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "media-root")
	createMediaDirectoryLink(t, actual, alias)
	if data, _, err := readConfinedNativeMedia(alias, filepath.Join(alias, "image.png"), 64); err != nil || string(data) != "configured-root-data" {
		t.Fatalf("configured media root link failed: %q, %v", data, err)
	}
	escape := filepath.Join(alias, relativeEscape)
	if data, err := os.ReadFile(escape); err != nil || string(data) != "outside-secret" {
		t.Fatalf("escape through configured root link not readable without confinement: %q, %v", data, err)
	}
	if data, _, err := readConfinedNativeMedia(alias, escape, 64); err == nil || len(data) != 0 {
		t.Fatalf("link inside configured root escaped confinement: %q, %v", data, err)
	}
}
