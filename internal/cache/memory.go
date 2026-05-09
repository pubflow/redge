package cache

import (
	"sync/atomic"
	"time"

	"github.com/dgraph-io/ristretto"
)

type Options struct {
	Enabled    bool
	MaxKeys    int
	MaxBytes   int64
	DefaultTTL time.Duration
}

type Memory struct {
	enabled    bool
	maxKeys    int
	maxBytes   int64
	defaultTTL time.Duration
	cache      *ristretto.Cache
	setOK      atomic.Uint64
	setDropped atomic.Uint64
}

func NewMemory(opts Options) *Memory {
	if opts.MaxKeys <= 0 {
		opts.MaxKeys = 100000
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = 128 * 1024 * 1024
	}
	if opts.DefaultTTL <= 0 {
		opts.DefaultTTL = 5 * time.Second
	}

	m := &Memory{
		enabled:    opts.Enabled,
		maxKeys:    opts.MaxKeys,
		maxBytes:   opts.MaxBytes,
		defaultTTL: opts.DefaultTTL,
	}
	if !opts.Enabled {
		return m
	}

	c, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: int64(opts.MaxKeys) * 10,
		MaxCost:     opts.MaxBytes,
		BufferItems: 64,
		Metrics:     true,
	})
	if err != nil {
		m.enabled = false
		return m
	}
	m.cache = c
	return m
}

func (m *Memory) Get(key string) ([]byte, bool) {
	if !m.enabled || m.cache == nil {
		return nil, false
	}
	value, ok := m.cache.Get(key)
	if !ok {
		return nil, false
	}
	bytes, ok := value.([]byte)
	if !ok {
		m.cache.Del(key)
		return nil, false
	}
	return append([]byte(nil), bytes...), true
}

func (m *Memory) Set(key string, value []byte, ttl time.Duration) {
	if !m.enabled || m.cache == nil {
		return
	}
	if ttl <= 0 || ttl > m.defaultTTL {
		ttl = m.defaultTTL
	}
	if ttl <= 0 {
		return
	}

	copied := append([]byte(nil), value...)
	cost := int64(len(key) + len(copied) + 64)
	if cost <= 0 {
		cost = 1
	}
	if m.cache.SetWithTTL(key, copied, cost, ttl) {
		m.setOK.Add(1)
		return
	}
	m.setDropped.Add(1)
}

func (m *Memory) Del(keys ...string) {
	if !m.enabled || m.cache == nil {
		return
	}
	for _, key := range keys {
		m.cache.Del(key)
	}
}

func (m *Memory) Close() {
	if m.cache != nil {
		m.cache.Close()
	}
}

func (m *Memory) Stats() map[string]any {
	stats := map[string]any{
		"enabled":     m.enabled,
		"max_keys":    m.maxKeys,
		"max_bytes":   m.maxBytes,
		"set_ok":      m.setOK.Load(),
		"set_dropped": m.setDropped.Load(),
		"engine":      "ristretto",
	}
	if m.cache == nil || m.cache.Metrics == nil {
		return stats
	}
	stats["hits"] = m.cache.Metrics.Hits()
	stats["misses"] = m.cache.Metrics.Misses()
	stats["hit_ratio"] = m.cache.Metrics.Ratio()
	stats["keys_added"] = m.cache.Metrics.KeysAdded()
	stats["keys_updated"] = m.cache.Metrics.KeysUpdated()
	stats["keys_evicted"] = m.cache.Metrics.KeysEvicted()
	stats["cost_added"] = m.cache.Metrics.CostAdded()
	stats["cost_evicted"] = m.cache.Metrics.CostEvicted()
	stats["sets_dropped_internal"] = m.cache.Metrics.SetsDropped()
	stats["sets_rejected"] = m.cache.Metrics.SetsRejected()
	stats["gets_dropped"] = m.cache.Metrics.GetsDropped()
	stats["gets_kept"] = m.cache.Metrics.GetsKept()
	return stats
}
