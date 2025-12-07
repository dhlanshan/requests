package validator

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func DefaultValidator(respBody []byte, respHeader http.Header) (bool, error) {
	checkFiled := []map[string]any{{"key": "code", "value": float64(200)}, {"key": "msg", "value": "success"}}
	busStatus := true

	var rp map[string]any
	err := json.Unmarshal(respBody, &rp)
	if err != nil {
		return false, fmt.Errorf("error unmarshalling response body: %w", err)
	}
	for _, pc := range checkFiled {
		if rp[pc["key"].(string)] != pc["value"] {
			busStatus = false
			err = fmt.Errorf("key: %s is error", pc["key"])
			break
		}
	}

	return busStatus, err
}
