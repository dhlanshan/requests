package runner

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dhlanshan/requests/tn"
)

func canRetry(req *http.Request, p tn.ApiParam) bool {
	return req.Method == http.MethodGet || req.Method == http.MethodHead || req.Method == http.MethodOptions ||
		p.AllowUnsafeRetry || strings.TrimSpace(req.Header.Get("Idempotency-Key")) != ""
}

func retryStatus(code int, p tn.ApiParam) bool {
	codes := p.RetryStatusCodes
	if codes == nil {
		codes = []int{408, 429, 500, 502, 503, 504}
	}
	for _, c := range codes {
		if code == c {
			return true
		}
	}
	return false
}

func retryForbidden(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, tn.ErrTargetBlocked) ||
		errors.Is(err, tn.ErrResponseTooLarge) || errors.Is(err, tn.ErrRequestBodyTooLarge) || errors.Is(err, tn.ErrBodyNotReplayable) ||
		errors.Is(err, tn.ErrPermanent) || errors.Is(err, tn.ErrAdmissionTooLarge) || errors.Is(err, tn.ErrResponseWrite) || errors.Is(err, tn.ErrClientConfig) {
		return true
	}
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	if errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid) {
		return true
	}
	return false
}

func retryError(err error) bool {
	if retryForbidden(err) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var network net.Error
	return errors.As(err, &network) && (network.Timeout() || network.Temporary())
}

func retryDelay(p tn.ApiParam, retry int, header http.Header) time.Duration {
	var delay time.Duration
	switch {
	case p.Backoff != nil:
		delay = p.Backoff(retry)
	case p.RetryInterval > 0:
		delay = p.RetryInterval
	default:
		shift := retry - 1
		if shift > 5 {
			shift = 5
		}
		ceiling := 100 * time.Millisecond * time.Duration(1<<shift)
		if ceiling > 2*time.Second {
			ceiling = 2 * time.Second
		}
		delay = ceiling/2 + time.Duration(rand.Int64N(int64(ceiling/2)+1))
	}
	if delay < 0 {
		delay = 0
	}
	if value := header.Get("Retry-After"); value != "" {
		var serverDelay time.Duration
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
			if seconds > int64(p.MaxRetryDelay/time.Second) {
				serverDelay = p.MaxRetryDelay
			} else {
				serverDelay = time.Duration(seconds) * time.Second
			}
		} else if when, err := http.ParseTime(value); err == nil {
			serverDelay = time.Until(when)
		}
		if serverDelay > delay {
			delay = serverDelay
		}
	}
	if delay > p.MaxRetryDelay {
		delay = p.MaxRetryDelay
	}
	return delay
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
