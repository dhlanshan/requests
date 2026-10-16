package tc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dhlanshan/requests/tn"
	"github.com/vmihailenco/msgpack/v5"
)

func TestFormTypesAndRepeatedValues(t *testing.T) {
	type UserID int64
	body, _, err := ContentByXWFormUrlencoded(map[string]any{"id": UserID(12), "tag": []string{"one", "two"}, "enabled": true})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(body)
	values, _ := url.ParseQuery(string(data))
	if values.Get("id") != "12" || values.Get("enabled") != "true" || len(values["tag"]) != 2 {
		t.Fatalf("values=%v", values)
	}
	if _, _, err := ContentByXWFormUrlencoded(map[string]any{"bad": map[string]int{"x": 1}}); err == nil {
		t.Fatal("unsupported field silently dropped")
	}
	if _, _, err := ContentByFormData(map[string]any{"good": "ok", "bad": make(chan int)}); err == nil {
		t.Fatal("unsupported multipart field silently dropped")
	}
}

func TestMultipartStreamingAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.bin")
	if err := os.WriteFile(path, []byte("file-data"), 0600); err != nil {
		t.Fatal(err)
	}
	reader, ct, err := ContentByFormData(map[string]any{
		"tag":    []string{"one", "two"},
		"upload": FormFile{Filename: "folder/中文\"file.bin", Content: path},
		"memory": FormFile{Filename: "bytes.txt", Content: []byte("memory-data")},
	})
	if err != nil {
		t.Fatal(err)
	}
	source, ok := reader.(tn.BodySource)
	if !ok || !source.Replayable() {
		t.Fatal("file multipart should be replayable")
	}
	_, options, _ := mime.ParseMediaType(ct)
	var previous []byte
	for attempt := 0; attempt < 2; attempt++ {
		body, length, err := source.NewBody(context.Background())
		if err != nil || length != -1 {
			t.Fatalf("length=%d err=%v", length, err)
		}
		data, err := io.ReadAll(body)
		body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if attempt > 0 && !bytes.Equal(previous, data) {
			t.Fatal("multipart boundary or content changed on replay")
		}
		previous = data
		parser := multipart.NewReader(bytes.NewReader(data), options["boundary"])
		form, err := parser.ReadForm(1024)
		if err != nil {
			t.Fatal(err)
		}
		if len(form.Value["tag"]) != 2 || form.File["upload"][0].Filename != "中文\"file.bin" {
			t.Fatalf("bad form: %#v", form)
		}
		file, err := form.File["upload"][0].Open()
		if err != nil {
			t.Fatal(err)
		}
		payload, err := io.ReadAll(file)
		file.Close()
		form.RemoveAll()
		if err != nil || string(payload) != "file-data" {
			t.Fatalf("data=%q err=%v", payload, err)
		}
	}
	reader.(io.Closer).Close()
}

func TestMultipartRejectsHeaderInjection(t *testing.T) {
	for _, values := range []map[string]any{
		{"bad\r\nInjected": "value"},
		{"file": FormFile{Filename: "bad\r\nX:y", Content: []byte("x")}},
		{"file": FormFile{Filename: "file", ContentType: "text/plain\r\nX:y", Content: []byte("x")}},
		{"file": FormFile{Filename: "file", ContentType: "invalid", Content: []byte("x")}},
	} {
		if _, _, err := ContentByFormData(values); err == nil {
			t.Fatal("header injection accepted")
		}
	}
}

func TestMultipartDoesNotOpenFilesUntilRead(t *testing.T) {
	reader, _, err := ContentByFormData(map[string]any{"file": FormFile{Content: filepath.Join(t.TempDir(), "missing")}})
	if err != nil {
		t.Fatalf("opened file eagerly: %v", err)
	}
	defer reader.(io.Closer).Close()
	if _, err = io.ReadAll(reader); err == nil {
		t.Fatal("missing file error swallowed")
	}
}

type closeCounter struct {
	io.Reader
	closes atomic.Int32
}

func (c *closeCounter) Close() error { c.closes.Add(1); return nil }

func TestMultipartOwnsOneShotReaders(t *testing.T) {
	input := &closeCounter{Reader: strings.NewReader("one-shot")}
	reader, _, err := ContentByFormData(map[string]any{"file": FormFile{Content: input}})
	if err != nil {
		t.Fatal(err)
	}
	source := reader.(tn.BodySource)
	if source.Replayable() {
		t.Fatal("arbitrary reader marked replayable")
	}
	body, _, err := source.NewBody(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(body); err != nil {
		t.Fatal(err)
	}
	body.Close()
	reader.(io.Closer).Close()
	if input.closes.Load() != 1 {
		t.Fatalf("closes=%d", input.closes.Load())
	}
	if _, _, err = source.NewBody(context.Background()); !errors.Is(err, tn.ErrBodyNotReplayable) {
		t.Fatalf("reopened one-shot body: %v", err)
	}
}

func TestBinaryLengthUsesCurrentOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(path, []byte("123456"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err = file.Seek(2, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	body, length, _, err := ContentByBinary(file)
	if err != nil || length != 4 {
		t.Fatalf("length=%d err=%v", length, err)
	}
	data, err := io.ReadAll(body)
	if err != nil || string(data) != "3456" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	factory := FileBodyFactory(path)
	for i := 0; i < 2; i++ {
		stream, length, err := factory(context.Background())
		if err != nil || length != 6 {
			t.Fatal(length, err)
		}
		data, err := io.ReadAll(stream)
		stream.Close()
		if err != nil || string(data) != "123456" {
			t.Fatal(string(data), err)
		}
	}
}

func TestContentEncodings(t *testing.T) {
	for _, constructor := range []func([]byte) (io.Reader, string, error){ContentByJson, ContentByXml} {
		body, ct, err := constructor([]byte("content"))
		if err != nil || ct == "" {
			t.Fatal(ct, err)
		}
		data, _ := io.ReadAll(body)
		if string(data) != "content" {
			t.Fatal(string(data))
		}
		if _, _, err = constructor(nil); err == nil {
			t.Fatal("empty body accepted")
		}
	}
	body, _, err := ContentByMsgpack(map[string]any{"key": "value"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(body)
	var value map[string]string
	if err = msgpack.Unmarshal(data, &value); err != nil || value["key"] != "value" {
		t.Fatal(value, err)
	}
}

func BenchmarkMultipartFileStream(b *testing.B) {
	path := filepath.Join(b.TempDir(), "large.bin")
	file, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	const size = 8 << 20
	if err = file.Truncate(size); err != nil {
		b.Fatal(err)
	}
	file.Close()
	b.SetBytes(size)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		body, _, err := ContentByFormData(map[string]any{"file": FormFile{Content: path}})
		if err != nil {
			b.Fatal(err)
		}
		_, err = io.Copy(io.Discard, body)
		body.(io.Closer).Close()
		if err != nil {
			b.Fatal(err)
		}
	}
}
