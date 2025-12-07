package requests

import (
	"bytes"
	"fmt"
	"github.com/dhlanshan/requests/internal/utils"
	"github.com/vmihailenco/msgpack/v5"
	"io"
	"mime/multipart"
	"net/url"
	"os"
)

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
func ContentByFormData(bodyData map[string]any, fileKeys []string) (io.Reader, string, error) {
	fields := make(map[string][]string)
	files := make(map[string][]string)
	for k, v := range bodyData {
		if val, flag := utils.ToStrings(v, false); flag {
			result := utils.InSlice(fileKeys, k)
			m := utils.TernaryOperator(result, fields, files)
			if _, ok := m[k]; !ok {
				m[k] = make([]string, 0)
			}
			m[k] = val
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

		for k, v := range fields {
			for _, val := range v {
				if err := writer.WriteField(k, val); err != nil {
					_ = pw.CloseWithError(err)
					return
				}
			}
		}

		// 写文件(流式)
		for k, v := range files {
			val := v[0]
			file, err := os.Open(val)
			if err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			part, err := writer.CreateFormFile(k, file.Name())
			if err != nil {
				_ = file.Close()
				_ = pw.CloseWithError(err)
				return
			}
			_, err = io.Copy(part, file) //不吃内存
			_ = file.Close()

			if err != nil {
				_ = pw.CloseWithError(err)
				return
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
func ContentByBinary(path string) (io.ReadCloser, int64, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, "", err
	}

	stat, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, "", err
	}

	return file, stat.Size(), "application/octet-stream", nil
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
