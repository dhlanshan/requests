package lz

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dhlanshan/requests/tn"
)

func TestCanceledReadStillClosesBody(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := &brokenBody{}
	_, err := ReadBounded(ctx, body, 16)
	if !errors.Is(err, context.Canceled) || body.closes != 1 {
		t.Fatalf("err=%v closes=%d", err, body.closes)
	}
}

type brokenBody struct {
	closes int
	sent   bool
}

func (b *brokenBody) Read(p []byte) (int, error) {
	if b.sent {
		return 0, io.ErrUnexpectedEOF
	}
	b.sent = true
	return copy(p, "partial"), io.ErrUnexpectedEOF
}
func (b *brokenBody) Close() error { b.closes++; return nil }

func TestRestorePreservesPartialReadError(t *testing.T) {
	original := &brokenBody{}
	resp := &http.Response{Body: original}
	data, err := ReadAndRestoreResponseBody(resp)
	if string(data) != "partial" || !errors.Is(err, io.ErrUnexpectedEOF) || original.closes != 1 {
		t.Fatalf("data=%q err=%v closes=%d", data, err, original.closes)
	}
	data, err = io.ReadAll(resp.Body)
	if string(data) != "partial" || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("restored data=%q err=%v", data, err)
	}
}

func TestRestoreOversizeRetainsTail(t *testing.T) {
	req, _ := http.NewRequest("POST", "http://example.test", strings.NewReader("123456789"))
	data, err := ReadRequestLimit(req, 3)
	if string(data) != "123" || !errors.Is(err, tn.ErrResponseTooLarge) {
		t.Fatalf("data=%q err=%v", data, err)
	}
	data, err = io.ReadAll(req.Body)
	if string(data) != "123456789" || err != nil {
		t.Fatalf("truncated restored body: %q %v", data, err)
	}
	req.Body.Close()
}
