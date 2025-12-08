package pf

import (
	"context"
	"github.com/dhlanshan/requests/dto"
)

func GetRequestMeta(ctx context.Context) (*dto.ApiParam, bool) {
	meta, ok := ctx.Value(dto.CtxJKExtend{}).(*dto.ApiParam)
	return meta, ok
}

func GetBusMeta(ctx context.Context) (*dto.InternalBus, bool) {
	meta, ok := ctx.Value(dto.CtxBSExtend{}).(*dto.InternalBus)
	return meta, ok
}
