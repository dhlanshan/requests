package validator

import (
	"encoding/json"
	"net/http"
)

func DefaultValidator(respBody []byte, respHeader http.Header) bool {
	checkFiled := []map[string]any{{"key": "code", "value": float64(200)}, {"key": "msg", "value": "success"}}
	busStatus := true

	var rp map[string]any
	err := json.Unmarshal(respBody, &rp)
	if err != nil {
		return false
	}
	for _, pc := range checkFiled {
		if rp[pc["key"].(string)] != pc["value"] {
			busStatus = false
			break
		}
	}

	return busStatus
}
