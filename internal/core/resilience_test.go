package core

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheConstructorPanicDoesNotPoisonLock(t *testing.T) {
	store := &ClientStore{limit: 2}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected panic")
			}
		}()
		_, _ = store.getOrCreate("panic", "", false, func() (*http.Client, error) { panic("extension failure") })
	}()
	if !store.mu.TryLock() {
		t.Fatal("cache lock leaked after panic")
	}
	store.mu.Unlock()
	if _, err := store.getOrCreate("healthy", "", false, func() (*http.Client, error) { return &http.Client{}, nil }); err != nil {
		t.Fatal(err)
	}
}

func TestCacheConstructorCanCreateAnotherClient(t *testing.T) {
	store := &ClientStore{limit: 2}
	_, err := store.getOrCreate("outer", "", false, func() (*http.Client, error) {
		if !store.mu.TryLock() {
			return nil, errors.New("constructor runs under cache lock")
		}
		store.mu.Unlock()
		_, err := store.getOrCreate("inner", "", false, func() (*http.Client, error) { return &http.Client{}, nil })
		return &http.Client{}, err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestVerifiedDialFallsBackWithoutResolvingAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	primaryStopped := make(chan struct{})
	conn, err := dialVerified(ctx, "tcp", "443", []netip.Addr{netip.MustParseAddr("2606:4700::1111"), netip.MustParseAddr("1.1.1.1")}, 5*time.Millisecond, func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" {
			t.Error("invalid endpoint", address)
		}
		if _, err := netip.ParseAddr(host); err != nil {
			t.Error("dial received a hostname instead of a verified IP")
		}
		if host == "2606:4700::1111" {
			<-ctx.Done()
			close(primaryStopped)
			return nil, ctx.Err()
		}
		a, b := net.Pipe()
		b.Close()
		return a, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	select {
	case <-primaryStopped:
	case <-time.After(time.Second):
		t.Fatal("losing dial was not canceled")
	}
}

type countedConn struct {
	net.Conn
	closed chan struct{}
	count  atomic.Int32
}

func (c *countedConn) Close() error {
	if c.count.Add(1) == 1 {
		close(c.closed)
	}
	return c.Conn.Close()
}

func TestVerifiedDialClosesLateSuccessfulConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a, b := net.Pipe()
	defer b.Close()
	late := &countedConn{Conn: a, closed: make(chan struct{})}
	conn, err := dialVerified(ctx, "tcp", "443", []netip.Addr{netip.MustParseAddr("2606:4700::1111"), netip.MustParseAddr("1.1.1.1")}, 5*time.Millisecond, func(ctx context.Context, _ string, address string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(address)
		if host == "2606:4700::1111" {
			<-ctx.Done()
			return late, nil
		}
		x, y := net.Pipe()
		y.Close()
		return x, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	select {
	case <-late.closed:
	case <-time.After(time.Second):
		t.Fatal("late connection leaked")
	}
}
