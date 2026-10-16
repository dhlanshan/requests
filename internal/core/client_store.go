package core

import (
	"container/list"
	"fmt"
	"net/http"
	"sync"

	"github.com/dhlanshan/requests/tn"
)

// ClientStore 只在访问 LRU 时持锁。构造和清理可执行用户代码，必须在锁外运行。
type ClientStore struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	lru     list.List
	limit   int64
}
type clientEntry struct {
	key    string
	client *http.Client
	policy string
}

var clientStore = &ClientStore{limit: 100}

func (s *ClientStore) cached(key, config string) (*http.Client, error) {
	if element, ok := s.entries[key]; ok {
		entry := element.Value.(*clientEntry)
		if entry.policy != config {
			return nil, fmt.Errorf("%w: named client options differ; use Reset, GetClient or NewClientUncached", tn.ErrClientConfig)
		}
		s.lru.MoveToFront(element)
		return entry.client, nil
	}
	return nil, nil
}

func (s *ClientStore) getOrCreate(key, config string, reset bool, build func() (*http.Client, error)) (*http.Client, error) {
	s.mu.Lock()
	if !reset {
		client, err := s.cached(key, config)
		if client != nil || err != nil {
			s.mu.Unlock()
			return client, err
		}
	}
	s.mu.Unlock()

	// 乐观构造：并发首次创建允许多个独立候选，提交时只采用一个。
	// 不等待用户回调，也不持有缓存锁，避免慢回调、重入或 panic 锁住整个缓存。
	candidate, err := build()
	if err != nil {
		return nil, err
	}
	if candidate == nil {
		return nil, fmt.Errorf("%w: constructor returned nil client", tn.ErrClientConfig)
	}

	s.mu.Lock()
	if !reset {
		winner, err := s.cached(key, config)
		if winner != nil || err != nil {
			s.mu.Unlock()
			if winner != candidate {
				candidate.CloseIdleConnections()
			}
			return winner, err
		}
	}
	if s.entries == nil {
		s.entries = make(map[string]*list.Element)
	}
	var old, evicted *http.Client
	if element, ok := s.entries[key]; ok {
		old = element.Value.(*clientEntry).client
		s.lru.Remove(element)
		delete(s.entries, key)
	}
	s.entries[key] = s.lru.PushFront(&clientEntry{key: key, client: candidate, policy: config})
	limit := s.limit
	if limit <= 0 {
		limit = 100
	}
	if int64(s.lru.Len()) > limit {
		tail := s.lru.Back()
		entry := tail.Value.(*clientEntry)
		evicted = entry.client
		delete(s.entries, entry.key)
		s.lru.Remove(tail)
	}
	s.mu.Unlock()
	if old != nil && old != candidate {
		old.CloseIdleConnections()
	}
	if evicted != nil && evicted != candidate {
		evicted.CloseIdleConnections()
	}
	return candidate, nil
}

func (s *ClientStore) Store(key string, val *http.Client) {
	_, _ = s.getOrCreate(key, "", true, func() (*http.Client, error) { return val, nil })
}
func (s *ClientStore) Load(key string) (*http.Client, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	element, ok := s.entries[key]
	if !ok {
		return nil, false
	}
	s.lru.MoveToFront(element)
	return element.Value.(*clientEntry).client, true
}
