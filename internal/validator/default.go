package validator

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// DefaultValidator 保留 code == 200 且 msg == "success" 的业务约定。
func DefaultValidator(body []byte, _ http.Header) (bool, error) {
	var result struct {
		Code float64 `json:"code"`
		Msg  string  `json:"msg"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return false, fmt.Errorf("decode validation response: %w", err)
	}
	if result.Code != 200 || result.Msg != "success" {
		return false, fmt.Errorf("expected code=200 and msg=success")
	}
	return true, nil
}
