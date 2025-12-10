package pf

import (
	"context"
	"github.com/dhlanshan/requests/tn"
)

func GetRequestMeta(ctx context.Context) (*tn.ApiParam, bool) {
	meta, ok := ctx.Value(tn.CtxJKExtend{}).(*tn.ApiParam)
	return meta, ok
}

func GetBusMeta(ctx context.Context) (*tn.InternalBus, bool) {
	meta, ok := ctx.Value(tn.CtxBSExtend{}).(*tn.InternalBus)
	return meta, ok
}
