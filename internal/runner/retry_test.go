package runner

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/dhlanshan/requests/tn"
)

func TestCertificateErrorsCannotBeRetriedByDecider(t *testing.T) {
	cert := &x509.Certificate{DNSNames: []string{"other.test"}}
	unknown := x509.UnknownAuthorityError{Cert: cert}
	hostname := x509.HostnameError{Certificate: cert, Host: "example.test"}
	invalid := x509.CertificateInvalidError{Cert: cert, Reason: x509.Expired}
	roots := x509.SystemRootsError{Err: errors.New("fixture missing roots")}
	for _, test := range []struct {
		name  string
		cause error
	}{
		{"unknown_value", unknown}, {"unknown_pointer", &unknown},
		{"hostname_value", hostname}, {"hostname_pointer", &hostname},
		{"invalid_value", invalid}, {"invalid_pointer", &invalid},
		{"roots_value", roots}, {"roots_pointer", &roots},
		{"tls_roots", &tls.CertificateVerificationError{Err: roots}},
		{"tls_custom_verification", &tls.CertificateVerificationError{Err: errors.New("fixture custom verifier")}},
		{"application_no_retry", tn.NoRetry(errors.New("fixture application certificate failure"))},
	} {
		for _, wrapping := range []string{"direct", "formatted", "joined"} {
			t.Run(test.name+"/"+wrapping, func(t *testing.T) {
				cause := test.cause
				switch wrapping {
				case "formatted":
					cause = fmt.Errorf("fixture wrap: %w", cause)
				case "joined":
					cause = errors.Join(io.ErrUnexpectedEOF, cause)
				}
				calls, decisions := 0, 0
				p := tn.ApiParam{Url: "https://example.test", Method: "GET", Retry: 1, RetryInterval: time.Nanosecond,
					RetryDecider: func(context.Context, tn.RetryInfo) bool { decisions++; return true }}
				_, _, err := Execute(context.Background(), &http.Client{Transport: logTransportFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return nil, cause
				})}, p)
				if err == nil || !errors.Is(err, test.cause) || calls != 1 || decisions != 0 {
					t.Fatalf("calls=%d decisions=%d err=%v", calls, decisions, err)
				}
			})
		}
	}
}

func TestTLSNetworkFailureCanStillRetry(t *testing.T) {
	calls, decisions := 0, 0
	p := tn.ApiParam{Url: "https://example.test", Method: "GET", Retry: 1, RetryInterval: time.Nanosecond,
		RetryDecider: func(_ context.Context, info tn.RetryInfo) bool { decisions++; return info.DefaultRetry }}
	_, _, err := Execute(context.Background(), &http.Client{Transport: logTransportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, &net.OpError{Op: "TLS handshake", Net: "tcp", Err: io.ErrUnexpectedEOF}
		}
		return logResponse(200, "ok"), nil
	})}, p)
	if err != nil || calls != 2 || decisions != 1 {
		t.Fatalf("calls=%d decisions=%d err=%v", calls, decisions, err)
	}
}
