package core

import (
	"fmt"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dhlanshan/requests/tn"
)

type idleTracker struct{ closed atomic.Int32 }

func (t *idleTracker) RoundTrip(*http.Request) (*http.Response, error) { panic("not used") }
func (t *idleTracker) CloseIdleConnections()                           { t.closed.Add(1) }

func TestCacheConcurrentCreationAndEviction(t *testing.T) {
	store := &ClientStore{limit: 8}
	var builds atomic.Int32
	var wg sync.WaitGroup
	clients := make(chan *http.Client, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client, err := store.getOrCreate("same", "", false, func() (*http.Client, error) { builds.Add(1); return &http.Client{}, nil })
			if err != nil {
				t.Error(err)
			}
			clients <- client
		}()
	}
	wg.Wait()
	close(clients)
	var first *http.Client
	for client := range clients {
		if first == nil {
			first = client
		}
		if client != first {
			t.Error("same key created multiple clients")
		}
	}
	if builds.Load() < 1 || builds.Load() > 64 {
		t.Fatalf("builds=%d", builds.Load())
	}
	for i := 0; i < 512; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", i%32)
			_, err := store.getOrCreate(key, "", i%7 == 0, func() (*http.Client, error) { return &http.Client{}, nil })
			if err != nil {
				t.Error(err)
			}
			store.Load(key)
		}(i)
	}
	wg.Wait()
	if len(store.entries) > 8 || store.lru.Len() != len(store.entries) {
		t.Fatal("LRU bound was broken")
	}
}

func TestCacheClosesEvictedIdleConnections(t *testing.T) {
	store := &ClientStore{limit: 2}
	a, b := &idleTracker{}, &idleTracker{}
	store.Store("a", &http.Client{Transport: a})
	store.Store("b", &http.Client{Transport: b})
	store.Load("a")
	store.Store("c", &http.Client{})
	if _, ok := store.Load("b"); ok || b.closed.Load() != 1 || a.closed.Load() != 0 {
		t.Fatal("wrong LRU eviction")
	}
	store.Store("a", &http.Client{})
	if a.closed.Load() != 1 {
		t.Fatal("Reset leaked idle connections")
	}
}

func TestPublicAddressClassification(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "0.1.2.3", "224.0.0.1", "240.0.0.1", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "2001:db8::1", "64:ff9b::7f00:1"} {
		if publicAddress(netip.MustParseAddr(address)) {
			t.Errorf("allowed reserved address %s", address)
		}
	}
	for _, address := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !publicAddress(netip.MustParseAddr(address)) {
			t.Errorf("blocked public address %s", address)
		}
	}
}

func TestHostAllowlistExactMatch(t *testing.T) {
	policy, _, err := normalizePolicy(&tn.NetworkPolicy{AllowedHosts: []string{"API.Example.com."}})
	if err != nil {
		t.Fatal(err)
	}
	if checkHost(policy, "api.example.com") != nil {
		t.Fatal("normalization failed")
	}
	for _, host := range []string{"evil.example.com", "api.example.com.evil.test", "example.com"} {
		if checkHost(policy, host) == nil {
			t.Fatalf("host accepted: %s", host)
		}
	}
}
