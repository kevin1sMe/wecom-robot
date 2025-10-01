package cache

import (
	"context"
	"net"
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
	// Reasonable defaults per request: 1s timeouts, 3 retries, TCP keepalive.
	// Support both "host:port" and redis URL forms. If parse fails, fall back to Addr.
	makeOpts := func() *redis.Options {
		// Shared dialer with 1s connect timeout and TCP keepalive
		d := &net.Dialer{Timeout: time.Second, KeepAlive: 30 * time.Second}
		return &redis.Options{
			Addr:         strings.TrimSpace(addr),
			DialTimeout:  time.Second,
			ReadTimeout:  time.Second,
			WriteTimeout: time.Second,
			// Retry up to 3 times with small backoff
			MaxRetries:      3,
			MinRetryBackoff: 100 * time.Millisecond,
			MaxRetryBackoff: 500 * time.Millisecond,
			// Use custom dialer to ensure TCP keepalive
			Dialer: func(ctx context.Context, network, address string) (net.Conn, error) {
				return d.DialContext(ctx, network, address)
			},
		}
	}

	var opt *redis.Options
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(addr)), "redis://") {
		if parsed, err := redis.ParseURL(addr); err == nil {
			// Preserve parsed settings (Addr/DB/Username/Password), then apply our timeouts/retries/keepalive
			opt = parsed
			opt.DialTimeout = time.Second
			opt.ReadTimeout = time.Second
			opt.WriteTimeout = time.Second
			opt.MaxRetries = 3
			opt.MinRetryBackoff = 100 * time.Millisecond
			opt.MaxRetryBackoff = 500 * time.Millisecond
			d := &net.Dialer{Timeout: time.Second, KeepAlive: 30 * time.Second}
			opt.Dialer = func(ctx context.Context, network, address string) (net.Conn, error) {
				return d.DialContext(ctx, network, address)
			}
		} else {
			opt = makeOpts()
		}
	} else {
		opt = makeOpts()
	}

	client := redis.NewClient(opt)
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
	// Per-call bound: 1s total to avoid long hangs (parent deadline may be shorter)
	cctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	v, err := r.cli.Get(cctx, key).Result()
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
	// Per-call bound: 1s total with built-in client retries
	cctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return r.cli.Set(cctx, key, value, r.ttl).Err()
}
