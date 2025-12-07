package core

import (
	"net/http"
	"sync"
	"sync/atomic"
)

var clientStore *ClientStore

type ClientStore struct {
	m     sync.Map
	count atomic.Int64
	limit int64
}

func (s *ClientStore) Store(key string, val *http.Client) {
	if s.count.Add(1) > s.limit {
		s.reset()
	}
	s.m.Store(key, val)

}

func (s *ClientStore) Load(key string) (*http.Client, bool) {
	if v, ok := s.m.Load(key); ok {
		return v.(*http.Client), true
	}
	return nil, false
}

func (s *ClientStore) reset() {
	s.m = sync.Map{}
	s.count.Store(0)
}

func init() {
	clientStore = &ClientStore{
		limit: 100,
	}
}
