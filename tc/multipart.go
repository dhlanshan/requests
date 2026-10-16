package tc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/dhlanshan/requests/internal/lz"
	"github.com/dhlanshan/requests/internal/utils"
	"github.com/dhlanshan/requests/tn"
)

type streamFactory func(context.Context) (io.ReadCloser, error)

// multipartSource 仅存储边界、字段和打开文件的方法，内存不随文件大小增长。
type multipartSource struct {
	parts      []streamFactory
	closers    []io.Closer
	replayable bool
	used       atomic.Bool
	mu         sync.Mutex
	direct     io.ReadCloser
	closed     bool
}

func literalPart(data []byte) streamFactory {
	return func(context.Context) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
}

func newMultipart(values map[string]any) (io.Reader, string, error) {
	source := &multipartSource{replayable: true}
	var frame bytes.Buffer
	writer := multipart.NewWriter(&frame)
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	add := func(header textproto.MIMEHeader, content streamFactory) error {
		if _, err := writer.CreatePart(header); err != nil {
			return err
		}
		source.parts = append(source.parts, literalPart(bytes.Clone(frame.Bytes())), content)
		frame.Reset()
		return nil
	}
	for _, key := range keys {
		if strings.ContainsAny(key, "\r\n\x00") {
			return nil, "", fmt.Errorf("invalid multipart field name")
		}
		var files []FormFile
		switch v := values[key].(type) {
		case FormFile:
			files = []FormFile{v}
		case []FormFile:
			files = v
		default:
			fields, ok := utils.ToStrings(v, false)
			if !ok {
				return nil, "", fmt.Errorf("unsupported form field %q (%T)", key, v)
			}
			for _, value := range fields {
				header := textproto.MIMEHeader{"Content-Disposition": {mime.FormatMediaType("form-data", map[string]string{"name": key})}}
				if err := add(header, literalPart([]byte(value))); err != nil {
					return nil, "", err
				}
			}
			continue
		}
		for _, file := range files {
			filename := file.Filename
			if filename == "" {
				filename = key
			}
			if strings.ContainsAny(filename, "\r\n\x00") {
				return nil, "", fmt.Errorf("invalid multipart filename")
			}
			// 上传只包含文件名，避免泄漏本地目录；兼容 Windows 路径分隔符。
			filename = filepath.Base(strings.ReplaceAll(filename, "\\", "/"))
			ct := file.ContentType
			if ct == "" {
				ct = mime.TypeByExtension(filepath.Ext(filename))
			}
			if ct == "" {
				ct = "application/octet-stream"
			}
			if strings.ContainsAny(ct, "\r\n\x00") {
				return nil, "", fmt.Errorf("invalid multipart content type")
			}
			if mediaType, _, err := mime.ParseMediaType(ct); err != nil || !strings.Contains(mediaType, "/") {
				return nil, "", fmt.Errorf("invalid multipart content type")
			}
			var open streamFactory
			switch content := file.Content.(type) {
			case string:
				open = func(ctx context.Context) (io.ReadCloser, error) {
					body, _, err := FileBodyFactory(content)(ctx)
					return body, err
				}
			case []byte:
				open = literalPart(content)
			case io.Reader:
				if utils.IsNil(content) {
					return nil, "", fmt.Errorf("nil multipart reader")
				}
				closer, ok := content.(io.ReadCloser)
				if !ok {
					closer = io.NopCloser(content)
				}
				owned := lz.Guard(closer)
				source.closers = append(source.closers, owned)
				source.replayable = false
				open = func(context.Context) (io.ReadCloser, error) { return owned, nil }
			default:
				return nil, "", fmt.Errorf("unsupported multipart file type: %T", content)
			}
			header := textproto.MIMEHeader{
				"Content-Disposition": {mime.FormatMediaType("form-data", map[string]string{"name": key, "filename": filename})},
				"Content-Type":        {ct},
			}
			if err := add(header, open); err != nil {
				return nil, "", err
			}
		}
	}
	if len(source.parts) == 0 {
		return nil, "", fmt.Errorf("没有有效的字段")
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	source.parts = append(source.parts, literalPart(bytes.Clone(frame.Bytes())))
	return source, writer.FormDataContentType(), nil
}

func (s *multipartSource) Replayable() bool { return s.replayable }
func (s *multipartSource) NewBody(ctx context.Context) (io.ReadCloser, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if !s.replayable && !s.used.CompareAndSwap(false, true) {
		return nil, 0, tn.ErrBodyNotReplayable
	}
	return &sequenceBody{ctx: ctx, parts: s.parts, cleanup: s.closeInputs}, -1, nil
}
func (s *multipartSource) Read(p []byte) (int, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, os.ErrClosed
	}
	if s.direct == nil {
		body, _, err := s.NewBody(context.Background())
		if err != nil {
			s.mu.Unlock()
			return 0, err
		}
		s.direct = body
	}
	body := s.direct
	s.mu.Unlock()
	return body.Read(p)
}
func (s *multipartSource) closeInputs() {
	for _, closer := range s.closers {
		_ = closer.Close()
	}
}
func (s *multipartSource) Close() error {
	s.mu.Lock()
	s.closed = true
	body := s.direct
	s.mu.Unlock()
	s.closeInputs()
	if body != nil {
		return body.Close()
	}
	return nil
}

// sequenceBody 按序输出小块 MIME 头及文件流。没有生产者 goroutine，
// 即使调用方创建后放弃发送，也不会遗留阻塞的管道写入。
type sequenceBody struct {
	ctx     context.Context
	parts   []streamFactory
	cleanup func()
	mu      sync.Mutex
	index   int
	current io.ReadCloser
	closed  bool
}

func (s *sequenceBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if err := s.ctx.Err(); err != nil {
			return 0, err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return 0, os.ErrClosed
		}
		if s.index == len(s.parts) {
			s.mu.Unlock()
			return 0, io.EOF
		}
		body, open := s.current, s.parts[s.index]
		s.mu.Unlock()
		if body == nil {
			var err error
			body, err = open(s.ctx)
			if err != nil {
				return 0, err
			}
			body = lz.Guard(body)
			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				_ = body.Close()
				return 0, os.ErrClosed
			}
			s.current = body
			s.mu.Unlock()
		}
		n, err := body.Read(p)
		if err == io.EOF {
			_ = body.Close()
			s.mu.Lock()
			s.current = nil
			s.index++
			s.mu.Unlock()
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}
func (s *sequenceBody) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	body := s.current
	s.mu.Unlock()
	if s.cleanup != nil {
		s.cleanup()
	}
	if body != nil {
		return body.Close()
	}
	return nil
}
