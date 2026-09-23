package egress

import (
	"container/list"
	"sync"
)

const maxClientCacheEntries = 256

type cacheEntry struct {
	key    CacheKey
	client *Client
}

// ClientCache bounds idle tunnel pools and isolates them by proxy connection
// revision. It never performs network I/O while holding its mutex.
type ClientCache struct {
	mu      sync.Mutex
	max     int
	deps    dependencies
	entries map[CacheKey]*list.Element
	recent  *list.List
	closed  bool
}

// NewClientCache constructs a production cache. max must be between 1 and 256.
func NewClientCache(max int) (*ClientCache, error) {
	return newClientCache(max, productionDependencies())
}

func newClientCache(max int, deps dependencies) (*ClientCache, error) {
	if max < 1 || max > maxClientCacheEntries {
		return nil, ErrInvalidConfig
	}
	return &ClientCache{max: max, deps: normalizeDependencies(deps), entries: make(map[CacheKey]*list.Element), recent: list.New()}, nil
}

// Client returns the existing revision-specific pool or constructs one. A
// reused key with different connection material is rejected conservatively.
func (cache *ClientCache) Client(config ProxyConfig) (*Client, error) {
	if cache == nil {
		return nil, ErrInvalidConfig
	}
	key := CacheKey{ProxyID: config.ProxyID, ConnectionRevision: config.ConnectionRevision}
	fingerprint := configFingerprint(config)
	cache.mu.Lock()
	if cache.closed {
		cache.mu.Unlock()
		return nil, ErrCacheClosed
	}
	if element := cache.entries[key]; element != nil {
		entry := element.Value.(*cacheEntry)
		if entry.client.fingerprint != fingerprint {
			cache.mu.Unlock()
			return nil, ErrConfigConflict
		}
		cache.recent.MoveToFront(element)
		client := entry.client
		cache.mu.Unlock()
		return client, nil
	}
	cache.mu.Unlock()

	client, err := newHTTPSConnectClient(config, cache.deps)
	if err != nil {
		return nil, err
	}
	var closeClients []*Client
	cache.mu.Lock()
	if cache.closed {
		cache.mu.Unlock()
		client.CloseIdleConnections()
		return nil, ErrCacheClosed
	}
	if element := cache.entries[key]; element != nil {
		entry := element.Value.(*cacheEntry)
		if entry.client.fingerprint != fingerprint {
			cache.mu.Unlock()
			client.CloseIdleConnections()
			return nil, ErrConfigConflict
		}
		cache.recent.MoveToFront(element)
		winner := entry.client
		cache.mu.Unlock()
		client.CloseIdleConnections()
		return winner, nil
	}
	element := cache.recent.PushFront(&cacheEntry{key: key, client: client})
	cache.entries[key] = element
	for cache.recent.Len() > cache.max {
		oldest := cache.recent.Back()
		entry := oldest.Value.(*cacheEntry)
		delete(cache.entries, entry.key)
		cache.recent.Remove(oldest)
		closeClients = append(closeClients, entry.client)
	}
	cache.mu.Unlock()
	for _, old := range closeClients {
		old.CloseIdleConnections()
	}
	return client, nil
}

// Invalidate removes all cached revisions for one proxy. In-flight requests
// continue; only idle connections are closed.
func (cache *ClientCache) Invalidate(proxyID string) {
	if cache == nil {
		return
	}
	var clients []*Client
	cache.mu.Lock()
	for key, element := range cache.entries {
		if key.ProxyID == proxyID {
			clients = append(clients, element.Value.(*cacheEntry).client)
			delete(cache.entries, key)
			cache.recent.Remove(element)
		}
	}
	cache.mu.Unlock()
	for _, client := range clients {
		client.CloseIdleConnections()
	}
}

func (cache *ClientCache) Close() {
	if cache == nil {
		return
	}
	var clients []*Client
	cache.mu.Lock()
	if !cache.closed {
		cache.closed = true
		for _, element := range cache.entries {
			clients = append(clients, element.Value.(*cacheEntry).client)
		}
		cache.entries = make(map[CacheKey]*list.Element)
		cache.recent.Init()
	}
	cache.mu.Unlock()
	for _, client := range clients {
		client.CloseIdleConnections()
	}
}
