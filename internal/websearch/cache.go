package websearch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

// ResultCache stores encoded search results by an opaque key. A miss returns
// (nil, false, nil); any error is treated as a miss by CachingProvider.
type ResultCache interface {
	Get(context.Context, string) ([]byte, bool, error)
	Set(context.Context, string, []byte, time.Duration) error
}

// CachingProvider answers repeated identical searches from a shared cache and
// collapses concurrent identical searches in this process into one upstream
// call, so the upstream Provider's rate limit is spent only on new queries.
// Only successful results are cached; cache failures fall through to next.
type CachingProvider struct {
	next      Provider
	cache     ResultCache
	namespace string
	ttl       time.Duration
	flights   singleflight.Group
}

const cacheSchemaVersion = 1

func NewCachingProvider(next Provider, cache ResultCache, namespace string, ttl time.Duration) (*CachingProvider, error) {
	if next == nil || cache == nil || strings.TrimSpace(namespace) == "" || ttl <= 0 {
		return nil, errors.New("invalid Web Search cache configuration")
	}
	return &CachingProvider{next: next, cache: cache, namespace: namespace, ttl: ttl}, nil
}

func (p *CachingProvider) Search(ctx context.Context, input Request) ([]Candidate, error) {
	key := p.cacheKey(input)
	if body, ok, err := p.cache.Get(ctx, key); err != nil {
		slog.WarnContext(ctx, "Web Search cache read failed", "error", err)
	} else if ok {
		var cached []Candidate
		if err := json.Unmarshal(body, &cached); err == nil {
			return cached, nil
		}
	}

	// The shared call must not die with whichever caller started it; the
	// upstream HTTP client timeout bounds it instead.
	flight := p.flights.DoChan(key, func() (any, error) {
		flightCtx := context.WithoutCancel(ctx)
		candidates, err := p.next.Search(flightCtx, input)
		if err != nil {
			return nil, err
		}
		if body, err := json.Marshal(candidates); err == nil {
			if err := p.cache.Set(flightCtx, key, body, p.ttl); err != nil {
				slog.WarnContext(ctx, "Web Search cache write failed", "error", err)
			}
		}
		return candidates, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-flight:
		if result.Err != nil {
			return nil, result.Err
		}
		shared := result.Val.([]Candidate)
		return append([]Candidate(nil), shared...), nil
	}
}

// cacheKey normalizes the parts of a Request that do not change what the
// upstream returns: surrounding/repeated whitespace and letter case.
func (p *CachingProvider) cacheKey(input Request) string {
	normalized := strings.Join([]string{
		strconv.Itoa(cacheSchemaVersion),
		p.namespace,
		strings.ToLower(strings.Join(strings.Fields(input.Query), " ")),
		strconv.Itoa(input.Count),
		strings.ToUpper(strings.TrimSpace(input.Country)),
		strings.ToLower(strings.TrimSpace(input.SearchLang)),
	}, "\x00")
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

type RedisResultCacheConfig struct {
	URL              string
	KeyPrefix        string
	OperationTimeout time.Duration
}

type RedisResultCache struct {
	client           *redis.Client
	keyPrefix        string
	operationTimeout time.Duration
}

func NewRedisResultCache(config RedisResultCacheConfig) (*RedisResultCache, error) {
	if strings.TrimSpace(config.URL) == "" || strings.TrimSpace(config.KeyPrefix) == "" || config.OperationTimeout <= 0 {
		return nil, errors.New("invalid Web Search Redis cache configuration")
	}
	options, err := redis.ParseURL(config.URL)
	if err != nil {
		return nil, fmt.Errorf("parse Web Search Redis cache URL: %w", err)
	}
	options.DialTimeout = config.OperationTimeout
	options.ReadTimeout = config.OperationTimeout
	options.WriteTimeout = config.OperationTimeout
	return &RedisResultCache{
		client: redis.NewClient(options), keyPrefix: config.KeyPrefix, operationTimeout: config.OperationTimeout,
	}, nil
}

func (c *RedisResultCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	opCtx, cancel := context.WithTimeout(ctx, c.operationTimeout)
	defer cancel()
	body, err := c.client.Get(opCtx, c.keyPrefix+key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return body, true, nil
}

func (c *RedisResultCache) Set(ctx context.Context, key string, body []byte, ttl time.Duration) error {
	opCtx, cancel := context.WithTimeout(ctx, c.operationTimeout)
	defer cancel()
	return c.client.Set(opCtx, c.keyPrefix+key, body, ttl).Err()
}

func (c *RedisResultCache) Close() error {
	return c.client.Close()
}
