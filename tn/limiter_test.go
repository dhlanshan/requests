package tn

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestLimiterBudgetCancellationAndRelease(t *testing.T) {
	limiter, err := NewLimiter(2, 10)
	if err != nil {
		t.Fatal(err)
	}
	release, err := limiter.Acquire(context.Background(), 8)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err = limiter.Acquire(ctx, 3); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); release() }()
	}
	wg.Wait()
	second, err := limiter.Acquire(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	second()
	if _, err = limiter.Acquire(context.Background(), 11); !errors.Is(err, ErrAdmissionTooLarge) {
		t.Fatal(err)
	}
}

func TestLimiterWithoutByteBudgetDoesNotOverflowAccounting(t *testing.T) {
	limiter, _ := NewLimiter(2, 0)
	a, err := limiter.Acquire(context.Background(), math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	b, err := limiter.Acquire(context.Background(), math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	a()
	b()
	if limiter.buffered != 0 || limiter.active != 0 {
		t.Fatal("invalid accounting")
	}
}

func FuzzRetryListJSONSemantics(f *testing.F) {
	f.Add([]byte{}, false)
	f.Add([]byte{}, true)
	f.Add([]byte{1, 2, 3}, false)
	f.Fuzz(func(t *testing.T, data []byte, nilList bool) {
		if len(data) > 1024 {
			t.Skip()
		}
		var values []int
		if !nilList {
			values = make([]int, len(data))
			for i, value := range data {
				values[i] = 100 + int(value)
			}
		}
		encoded, err := json.Marshal(ApiParam{RetryStatusCodes: values})
		if err != nil {
			t.Fatal(err)
		}
		var decoded ApiParam
		if err = json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(values, decoded.RetryStatusCodes) {
			t.Fatal("retry list changed during JSON round trip")
		}
	})
}
