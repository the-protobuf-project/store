{{.Header}}

package {{.Package}}

// client.go is the Redis implementation of Cache. It is the only file in this
// package that knows Redis exists; everything else is written against the Cache
// interface, so a different provider target emits a different client and nothing
// else changes.

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client adapts a go-redis client to Cache. Values are stored as JSON, so a key
// written here is readable by any other language reading the same Redis — which
// is the point of a shared cache rather than a per-process one.
type Client struct {
	// RDB is the underlying client. Any go-redis client works: a plain client, a
	// cluster client, or one wrapped with your own hooks.
	RDB redis.UniversalClient

	// ScanCount tunes how many keys each SCAN step requests during a prefix
	// delete. Zero uses a sensible default.
	ScanCount int64
}

// New returns a Client over rdb.
func New(rdb redis.UniversalClient) *Client { return &Client{RDB: rdb} }

// Get implements Cache. A missing key is (false, nil), not an error.
func (c *Client) Get(ctx context.Context, key string, dest any) (bool, error) {
	b, err := c.RDB.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, dest); err != nil {
		// A value we cannot decode is worse than no value: drop it so the next
		// read repopulates rather than failing forever on a stale encoding.
		_ = c.RDB.Del(ctx, key).Err()
		return false, err
	}
	return true, nil
}

// Set implements Cache.
func (c *Client) Set(ctx context.Context, key string, val any, ttl time.Duration) error {
	b, err := json.Marshal(val)
	if err != nil {
		return err
	}
	return c.RDB.Set(ctx, key, b, ttl).Err()
}

// Delete implements Cache.
func (c *Client) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return c.RDB.Del(ctx, keys...).Err()
}

// DeletePrefix implements Cache by scanning for matching keys and deleting them
// in batches.
//
// SCAN rather than KEYS: KEYS blocks the server for the length of the keyspace,
// which is exactly the wrong thing to do on the write path of a busy service.
// SCAN is incremental and may return a key more than once, which is harmless
// here — deleting an already-deleted key is a no-op.
func (c *Client) DeletePrefix(ctx context.Context, prefix string) error {
	count := c.ScanCount
	if count <= 0 {
		count = 256
	}
	var cursor uint64
	for {
		keys, next, err := c.RDB.Scan(ctx, cursor, escapeGlob(prefix)+"*", count).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := c.RDB.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}

// escapeGlob quotes the characters Redis treats as glob metacharacters, so a
// prefix containing one matches literally instead of as a pattern.
func escapeGlob(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '*', '?', '[', ']', '^', '\\':
			b = append(b, '\\')
		}
		b = append(b, s[i])
	}
	return string(b)
}
