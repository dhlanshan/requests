package lz

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/dhlanshan/requests/tn"
)

func TestRestoreEmptyRequestPreservesNoBody(t *testing.T) {
	for _, original := range []io.ReadCloser{http.NoBody, io.NopCloser(strings.NewReader(""))} {
		req, _ := http.NewRequest("POST", "http://example.test", original)
		data, err := ReadRequestLimit(req, 16)
		if err != nil || len(data) != 0 || req.Body != http.NoBody || req.ContentLength != 0 || req.GetBody == nil {
			t.Fatalf("body=%T data=%q length=%d err=%v", req.Body, data, req.ContentLength, err)
		}
		reopened, err := req.GetBody()
		if err != nil || reopened != http.NoBody {
			t.Fatalf("replay=%T err=%v", reopened, err)
		}
		var wire bytes.Buffer
		if err := req.Write(&wire); err != nil || strings.Contains(wire.String(), "Transfer-Encoding: chunked") {
			t.Fatalf("empty request changed: %q %v", wire.String(), err)
		}
	}
	req, _ := http.NewRequest("POST", "http://example.test", http.NoBody)
	req.TransferEncoding = []string{"chunked"}
	_, _ = ReadRequestLimit(req, 16)
	if !slices.Equal(req.TransferEncoding, []string{"chunked"}) {
		t.Fatal("explicit transfer encoding was rewritten")
	}
}

func TestRestoreEmptyReadFailureIsNotNoBody(t *testing.T) {
	req, _ := http.NewRequest("POST", "http://example.test", io.NopCloser(&errorReader{err: io.ErrUnexpectedEOF}))
	_, err := ReadRequestLimit(req, 16)
	if !errors.Is(err, io.ErrUnexpectedEOF) || req.Body == http.NoBody || req.GetBody != nil {
		t.Fatalf("read failure became replayable success: %v", err)
	}
	_, err = io.ReadAll(req.Body)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("restored error lost: %v", err)
	}
}

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
