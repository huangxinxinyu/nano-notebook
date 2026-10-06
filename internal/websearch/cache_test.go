package websearch

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type countingProvider struct {
	calls   atomic.Int32
	release chan struct{}
	err     error
}

func (p *countingProvider) Search(_ context.Context, input Request) ([]Candidate, error) {
	p.calls.Add(1)
	if p.release != nil {
		<-p.release
	}
	if p.err != nil {
		return nil, p.err
	}
	return []Candidate{{Title: input.Query, URL: "https://example.com/a", DisplayURL: "example.com/a", Rank: 1}}, nil
}

type memoryCache struct {
	mu      sync.Mutex
	entries map[string][]byte
	ttls    map[string]time.Duration
	failing bool
}

func newMemoryCache() *memoryCache {
	return &memoryCache{entries: map[string][]byte{}, ttls: map[string]time.Duration{}}
}

func (c *memoryCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failing {
		return nil, false, errors.New("cache down")
	}
	body, ok := c.entries[key]
	return body, ok, nil
}

func (c *memoryCache) Set(_ context.Context, key string, body []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failing {
		return errors.New("cache down")
	}
	c.entries[key], c.ttls[key] = body, ttl
	return nil
}

func TestCachingProviderServesRepeatedSearchFromCache(t *testing.T) {
	t.Parallel()

	upstream := &countingProvider{}
	cache := newMemoryCache()
	provider, err := NewCachingProvider(upstream, cache, "brave", 6*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := provider.Search(ctx, Request{Query: "Film  Production", Count: 10})
	if err != nil {
		t.Fatal(err)
	}
	second, err := provider.Search(ctx, Request{Query: " film production ", Count: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := upstream.calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
	if len(second) != 1 || second[0] != first[0] {
		t.Fatalf("cached result = %#v, want %#v", second, first)
	}
	for _, ttl := range cache.ttls {
		if ttl != 6*time.Hour {
			t.Fatalf("ttl = %s, want 6h", ttl)
		}
	}

	if _, err := provider.Search(ctx, Request{Query: "film production", Count: 5}); err != nil {
		t.Fatal(err)
	}
	if got := upstream.calls.Load(); got != 2 {
		t.Fatalf("upstream calls after different count = %d, want 2", got)
	}
}

func TestCachingProviderDoesNotCacheErrors(t *testing.T) {
	t.Parallel()

	upstream := &countingProvider{err: ErrRateLimited}
	provider, err := NewCachingProvider(upstream, newMemoryCache(), "brave", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := provider.Search(context.Background(), Request{Query: "q", Count: 10}); !errors.Is(err, ErrRateLimited) {
			t.Fatalf("error = %v, want ErrRateLimited", err)
		}
	}
	if got := upstream.calls.Load(); got != 2 {
		t.Fatalf("upstream calls = %d, want 2", got)
	}
}

func TestCachingProviderFallsThroughWhenCacheFails(t *testing.T) {
	t.Parallel()

	upstream := &countingProvider{}
	cache := newMemoryCache()
	cache.failing = true
	provider, err := NewCachingProvider(upstream, cache, "brave", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	results, err := provider.Search(context.Background(), Request{Query: "q", Count: 10})
	if err != nil || len(results) != 1 {
		t.Fatalf("results = %#v, err = %v", results, err)
	}
}

func TestCachingProviderCollapsesConcurrentIdenticalSearches(t *testing.T) {
	t.Parallel()

	upstream := &countingProvider{release: make(chan struct{})}
	provider, err := NewCachingProvider(upstream, newMemoryCache(), "brave", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := provider.Search(context.Background(), Request{Query: "same", Count: 10})
			errs <- err
		}()
	}
	for upstream.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(upstream.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := upstream.calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
}

func TestCachingProviderReturnsWhenCallerContextEnds(t *testing.T) {
	t.Parallel()

	upstream := &countingProvider{release: make(chan struct{})}
	defer close(upstream.release)
	provider, err := NewCachingProvider(upstream, newMemoryCache(), "brave", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := provider.Search(ctx, Request{Query: "slow", Count: 10}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want DeadlineExceeded", err)
	}
}

func TestRedisResultCacheRoundTrip(t *testing.T) {
	redisURL := strings.TrimSpace(os.Getenv("NANO_TEST_REDIS_URL"))
	if redisURL == "" {
		t.Skip("NANO_TEST_REDIS_URL is required")
	}
	cache, err := NewRedisResultCache(RedisResultCacheConfig{
		URL: redisURL, KeyPrefix: "nano:test-web-search:", OperationTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	ctx := context.Background()
	key := "round-trip-" + time.Now().Format(time.RFC3339Nano)
	if _, ok, err := cache.Get(ctx, key); err != nil || ok {
		t.Fatalf("Get before Set: ok = %v, err = %v", ok, err)
	}
	if err := cache.Set(ctx, key, []byte(`[]`), time.Minute); err != nil {
		t.Fatal(err)
	}
	body, ok, err := cache.Get(ctx, key)
	if err != nil || !ok || string(body) != `[]` {
		t.Fatalf("Get after Set: body = %q, ok = %v, err = %v", body, ok, err)
	}
	ttl, err := cache.client.TTL(ctx, "nano:test-web-search:"+key).Result()
	if err != nil || ttl <= 0 || ttl > time.Minute {
		t.Fatalf("TTL = %s, err = %v", ttl, err)
	}
}
