//go:build unix

package tc

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/dhlanshan/requests/tn"
)

func TestFileFactoriesRejectFIFOWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	pipe := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(pipe, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("pipe", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"pipe", "link"} {
		for name, factory := range map[string]tn.BodyFactory{
			"plain": FileBodyFactory(filepath.Join(root, path)),
			"under": FileBodyFactoryUnder(root, path),
		} {
			t.Run(name+"/"+path, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				defer cancel()
				type result struct {
					body io.ReadCloser
					err  error
				}
				done := make(chan result, 1)
				go func() {
					body, _, err := factory(ctx)
					done <- result{body, err}
				}()
				select {
				case got := <-done:
					if got.body != nil {
						_ = got.body.Close()
					}
					if got.err == nil || got.body != nil {
						t.Fatalf("special file accepted: body=%v err=%v", got.body, got.err)
					}
				case <-time.After(time.Second):
					// Unblock a regressed implementation so the test never leaves a reader behind.
					unblock, err := os.OpenFile(pipe, os.O_RDWR|syscall.O_NONBLOCK, 0)
					if err != nil {
						t.Fatal(err)
					}
					got := <-done
					if got.body != nil {
						_ = got.body.Close()
					}
					_ = unblock.Close()
					t.Fatal("file factory blocked opening a FIFO beyond its context deadline")
				}
			})
		}
	}
}
