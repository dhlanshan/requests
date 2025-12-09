package requests

import (
	"bytes"
	"fmt"
	"github.com/dhlanshan/requests/internal/utils"
	"github.com/vmihailenco/msgpack/v5"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
)

type FormFile struct {
	Filename    string
	ContentType string
	Content     any
}

// ContentByXWFormUrlencoded x-www-form-urlencoded
func ContentByXWFormUrlencoded(bodyData map[string]any) (io.Reader, string, error) {
	data := url.Values{}
	for k, v := range bodyData {
		if val, flag := utils.ToStrings(v, true); flag {
			data.Set(k, val[0])
		}
	}
	if len(data) == 0 {
		return nil, "", fmt.Errorf("没有有效的字段")
	}

	return bytes.NewBufferString(data.Encode()), "application/x-www-form-urlencoded", nil
}

// ContentByFormData form-data
func ContentByFormData(bodyData map[string]any) (io.Reader, string, error) {
	fields := make(map[string][]string)
	files := make(map[string][]FormFile)
	for k, v := range bodyData {
		switch val := v.(type) {
		case FormFile:
			files[k] = []FormFile{val}
		case []FormFile:
			files[k] = val
		default:
			if va, flag := utils.ToStrings(v, false); flag {
				fields[k] = va
			}
		}
	}
	if len(fields) == 0 && len(files) == 0 {
		return nil, "", fmt.Errorf("没有有效的字段")
	}

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)

	go func() {
		defer pw.Close()
		defer writer.Close()

		for key, values := range fields {
			for _, val := range values {
				if err := writer.WriteField(key, val); err != nil {
					_ = pw.CloseWithError(err)
					return
				}
			}
		}

		// 写文件(流式)
		for key, fileList := range files {
			for _, fileObj := range fileList {
				filename := utils.TernaryOperator(fileObj.Filename == "", key, fileObj.Filename)

				contentType := utils.TernaryOperator(fileObj.ContentType == "", mime.TypeByExtension(filepath.Ext(filename)), fileObj.ContentType)
				contentType = utils.TernaryOperator(contentType == "", "application/octet-stream", contentType)

				h := make(textproto.MIMEHeader)
				h.Set("Content-Disposition",
					fmt.Sprintf(`form-data; name="%s"; filename="%s"`, key, filename))
				h.Set("Content-Type", contentType)
				part, err := writer.CreatePart(h)
				if err != nil {
					_ = pw.CloseWithError(err)
					return
				}

				switch c := fileObj.Content.(type) {
				case string:
					f, err := os.Open(c)
					if err != nil {
						_ = pw.CloseWithError(err)
						return
					}
					_, err = io.Copy(part, f)
					_ = f.Close()
					if err != nil {
						_ = pw.CloseWithError(err)
						return
					}
				case []byte:
					_, err = io.Copy(part, bytes.NewReader(c))
					if err != nil {
						_ = pw.CloseWithError(err)
						return
					}
				case io.Reader:
					_, err = io.Copy(part, c)
					if err != nil {
						_ = pw.CloseWithError(err)
						return
					}
				default:
					_ = pw.CloseWithError(fmt.Errorf("不支持的文件类型: %T", c))
					return
				}
			}
		}
	}()

	return pr, writer.FormDataContentType(), nil
}

// ContentByJson json
func ContentByJson(bodyData []byte) (io.Reader, string, error) {
	if len(bodyData) == 0 {
		return nil, "", fmt.Errorf("数据无效")
	}
	return bytes.NewBuffer(bodyData), "application/json", nil
}

// ContentByXml xml
func ContentByXml(bodyData []byte) (io.Reader, string, error) {
	if len(bodyData) == 0 {
		return nil, "", fmt.Errorf("数据无效")
	}
	return bytes.NewBuffer(bodyData), "application/xml", nil
}

// ContentByRaw raw text/plain
func ContentByRaw(bodyData string) (io.Reader, string, error) {
	if len(bodyData) == 0 {
		return nil, "", fmt.Errorf("数据无效")
	}
	rawData := []byte(bodyData)
	return bytes.NewBuffer(rawData), "text/plain", nil
}

// ContentByBinary binary octet-stream
func ContentByBinary(bodyData any) (io.Reader, int64, string, error) {
	contentType := "application/octet-stream"

	switch v := bodyData.(type) {
	case *os.File:
		stat, err := v.Stat()
		if err != nil {
			return nil, 0, "", err
		}
		return v, stat.Size(), contentType, nil
	case []byte:
		return bytes.NewReader(v), int64(len(v)), contentType, nil
	case io.Reader:
		return v, -1, contentType, nil
	default:
		return nil, 0, "", fmt.Errorf("unsupported binary type: %T", bodyData)
	}
}

// ContentByMsgpack x-msgpack
func ContentByMsgpack(bodyData map[string]any) (io.Reader, string, error) {
	// 使用 msgpack 编码数据
	msgpackData, err := msgpack.Marshal(bodyData)
	if err != nil {
		return nil, "", fmt.Errorf("error encoding MsgPack: %s", err.Error())
	}

	return bytes.NewBuffer(msgpackData), "application/x-msgpack", nil
}
