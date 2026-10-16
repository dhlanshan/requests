// Package tc 构造常见 HTTP Body。返回流在 Api/ApiContext 调用期间不得修改。
package tc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/dhlanshan/requests/internal/utils"
	"github.com/dhlanshan/requests/tn"
	"github.com/vmihailenco/msgpack/v5"
)

// FormFile 的 string Content 表示可信本地文件路径；[]byte 表示内存数据。
// 外部输入不可直接用作路径。任意 io.Reader 为一次性流，需支持 Close 取消阻塞读取。
type FormFile struct {
	Filename    string
	ContentType string
	Content     any
}

func ContentByXWFormUrlencoded(bodyData map[string]any) (io.Reader, string, error) {
	data := url.Values{}
	for k, v := range bodyData {
		values, ok := utils.ToStrings(v, false)
		if !ok {
			return nil, "", fmt.Errorf("unsupported form field %q (%T)", k, v)
		}
		for _, value := range values {
			data.Add(k, value)
		}
	}
	if len(data) == 0 {
		return nil, "", fmt.Errorf("没有有效的字段")
	}
	return strings.NewReader(data.Encode()), "application/x-www-form-urlencoded", nil
}

// ContentByFormData 惰性生成 multipart，不启动后台 goroutine，不预读文件。
// 路径和 []byte 可重新打开，因此重试大文件无需整包缓存。
func ContentByFormData(bodyData map[string]any) (io.Reader, string, error) {
	return newMultipart(bodyData)
}

func ContentByJson(bodyData []byte) (io.Reader, string, error) {
	if len(bodyData) == 0 {
		return nil, "", fmt.Errorf("数据无效")
	}
	return bytes.NewReader(bodyData), "application/json", nil
}
func ContentByXml(bodyData []byte) (io.Reader, string, error) {
	if len(bodyData) == 0 {
		return nil, "", fmt.Errorf("数据无效")
	}
	return bytes.NewReader(bodyData), "application/xml", nil
}
func ContentByRaw(bodyData string) (io.Reader, string, error) {
	if bodyData == "" {
		return nil, "", fmt.Errorf("数据无效")
	}
	return strings.NewReader(bodyData), "text/plain", nil
}

func ContentByBinary(bodyData any) (io.Reader, int64, string, error) {
	const contentType = "application/octet-stream"
	switch v := bodyData.(type) {
	case *os.File:
		if v == nil {
			return nil, 0, "", fmt.Errorf("nil file")
		}
		stat, err := v.Stat()
		if err != nil {
			return nil, 0, "", err
		}
		if !stat.Mode().IsRegular() {
			return v, -1, contentType, nil
		}
		offset, err := v.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, 0, "", err
		}
		size := stat.Size() - offset
		if size < 0 {
			size = 0
		}
		return v, size, contentType, nil
	case []byte:
		return bytes.NewReader(v), int64(len(v)), contentType, nil
	case io.Reader:
		if utils.IsNil(v) {
			return nil, 0, "", fmt.Errorf("nil reader")
		}
		return v, -1, contentType, nil
	default:
		return nil, 0, "", fmt.Errorf("unsupported binary type: %T", bodyData)
	}
}

// FileBodyFactory 用于大型二进制上传，每次尝试重新打开文件，不共享文件偏移。
// 调用期间文件内容须保持稳定；路径只能来自可信配置或经过业务层授权。
// Unix 使用非阻塞打开后检查普通文件，避免 FIFO 阻塞；文件系统调用本身无法被 Context 强制终止。
func FileBodyFactory(path string) tn.BodyFactory {
	return func(ctx context.Context) (io.ReadCloser, int64, error) {
		return openRegularFile(ctx, func(flag int) (*os.File, error) {
			return os.OpenFile(path, flag, 0)
		})
	}
}

func ContentByMsgpack(bodyData map[string]any) (io.Reader, string, error) {
	data, err := msgpack.Marshal(bodyData)
	if err != nil {
		return nil, "", fmt.Errorf("encode MsgPack: %w", err)
	}
	return bytes.NewReader(data), "application/x-msgpack", nil
}
