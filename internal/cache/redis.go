package cache

import (
	"context"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis wraps a minimal get/set string cache with TTL.
type Redis struct {
	cli    *redis.Client
	prefix string
	ttl    time.Duration
}

// NewRedis builds a Redis cache client from addr (host:port or redis:// URL),
// key prefix, and TTL. Caller owns lifecycle; Close when done if needed.
func NewRedis(addr, prefix string, ttl time.Duration) *Redis {
	// Support both "host:port" and redis URL forms.
	// If addr starts with redis://, use ParseURL for richer options.
	var client *redis.Client
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(addr)), "redis://") {
		opt, err := redis.ParseURL(addr)
		if err == nil {
			client = redis.NewClient(opt)
		} else {
			// Fallback to simple Addr if URL parse fails
			client = redis.NewClient(&redis.Options{Addr: addr})
		}
	} else {
		client = redis.NewClient(&redis.Options{Addr: addr})
	}
	if prefix == "" {
		prefix = "wecom-robot"
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &Redis{cli: client, prefix: prefix, ttl: ttl}
}

// Key builds a namespaced key.
func (r *Redis) Key(parts ...string) string {
	// prefix:part1:part2
	var b strings.Builder
	b.WriteString(r.prefix)
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(":")
		b.WriteString(p)
	}
	return b.String()
}

// GetString fetches an UTF-8 string value.
func (r *Redis) GetString(ctx context.Context, key string) (string, bool, error) {
	if r == nil || r.cli == nil || key == "" {
		return "", false, nil
	}
	v, err := r.cli.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetString stores an UTF-8 string value with default TTL.
func (r *Redis) SetString(ctx context.Context, key, value string) error {
	if r == nil || r.cli == nil || key == "" {
		return nil
	}
	return r.cli.Set(ctx, key, value, r.ttl).Err()
}
