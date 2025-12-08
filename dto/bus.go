package dto

import "sync"

type CtxBSExtend struct{}

type InternalBus struct {
	// RequestId used to record the unique ID for the current request
	RequestId  string   `json:"request_id"`
	MdwDataMap sync.Map `json:"mdw_data_map"`
}
