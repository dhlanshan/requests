//go:build go1.24

package tc

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestFileUnderRejectsTraversalAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "safe"), []byte("allowed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "escape")); err != nil {
		t.Skip(err)
	}
	for _, path := range []string{"../secret", filepath.Join(outside, "secret"), "escape"} {
		body, _, err := FileBodyFactoryUnder(root, path)(context.Background())
		if body != nil {
			body.Close()
		}
		if err == nil {
			t.Fatalf("escaped authorized root via %q", path)
		}
	}
	body, length, err := FileBodyFactoryUnder(root, "safe")(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil || length != 7 || string(data) != "allowed" {
		t.Fatal(length, string(data), err)
	}
}
