package agentoutbox

import (
	"errors"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PurgeStoreConfig struct {
	ProducerID          string
	MaxCommands         int
	LeaseDuration       time.Duration
	BaseBackoff         time.Duration
	MaxBackoff          time.Duration
	RetryJitter         func() float64
	StagingObjects      objectstore.Store
	StagingObjectPrefix string
}

func (c PurgeStoreConfig) withDefaults() PurgeStoreConfig {
	if c.MaxCommands == 0 {
		c.MaxCommands = 16
	}
	if c.LeaseDuration == 0 {
		c.LeaseDuration = 30 * time.Second
	}
	if c.BaseBackoff == 0 {
		c.BaseBackoff = time.Second
	}
	if c.MaxBackoff == 0 {
		c.MaxBackoff = time.Minute
	}
	if c.RetryJitter == nil {
		c.RetryJitter = rand.Float64
	}
	c.StagingObjectPrefix = strings.Trim(strings.TrimSpace(c.StagingObjectPrefix), "/")
	if c.StagingObjectPrefix == "" {
		c.StagingObjectPrefix = "agent-replay-staging"
	}
	return c
}

func (c PurgeStoreConfig) validate() error {
	if strings.TrimSpace(c.ProducerID) == "" {
		return errors.New("purge producer ID is required")
	}
	if c.MaxCommands < 1 {
		return errors.New("purge command Batch limit must be positive")
	}
	if c.LeaseDuration <= 0 {
		return errors.New("purge lease duration must be positive")
	}
	if c.BaseBackoff <= 0 || c.MaxBackoff < c.BaseBackoff {
		return errors.New("purge retry backoff is invalid")
	}
	return nil
}

type PurgeStore struct {
	pool           *pgxpool.Pool
	config         PurgeStoreConfig
	stagingObjects objectstore.Store
}

func NewPurgeStore(pool *pgxpool.Pool, config PurgeStoreConfig) (*PurgeStore, error) {
	if pool == nil {
		return nil, errors.New("purge PostgreSQL pool is required")
	}
	config = config.withDefaults()
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &PurgeStore{pool: pool, config: config, stagingObjects: config.StagingObjects}, nil
}

func (s *PurgeStore) retryDelay(attemptCount int) time.Duration {
	delay := s.config.BaseBackoff
	for attempt := 1; attempt < attemptCount && delay < s.config.MaxBackoff; attempt++ {
		if delay > s.config.MaxBackoff/2 {
			delay = s.config.MaxBackoff
			break
		}
		delay *= 2
	}
	jitter := s.config.RetryJitter()
	if jitter < 0 {
		jitter = 0
	}
	if jitter > 1 {
		jitter = 1
	}
	delay = time.Duration(float64(delay) * (0.5 + jitter))
	if delay > s.config.MaxBackoff {
		return s.config.MaxBackoff
	}
	return delay
}
