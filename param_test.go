package requests

import (
	"testing"
	"time"
)

func TestParam(t *testing.T) {
	p := ApiParam{
		Url:           "12313",
		Method:        "GET",
		RetryInterval: 5 * time.Second,
	}
	p.Check()
}
