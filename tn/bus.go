package tn

import "sync"

type CtxBSExtend struct{}

// InternalBus 每次 Api 调用独立创建，可通过 pf 获取。
type InternalBus struct {
	RequestId  string   `json:"request_id"`
	MdwDataMap sync.Map `json:"-"`
}
type RetryData struct {
	TryCnt int `json:"try_cnt"`
}
