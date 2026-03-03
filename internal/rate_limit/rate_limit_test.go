package rate_limit

import (
	"context"
	"testing"
	"time"
)

type stubStore struct {
	count     int
	latest    time.Time
	hasLatest bool
}

func (s stubStore) CountRecentByIPHashes(context.Context, []string, time.Time) (int, error) {
	return s.count, nil
}

func (s stubStore) LatestPostTimeByIPHashes(context.Context, []string) (time.Time, bool, error) {
	return s.latest, s.hasLatest, nil
}

func TestHashIPIsStable(t *testing.T) {
	t.Parallel()

	limiter := NewLimiter(time.Minute, 15*time.Minute, 5, "salt", time.Hour)
	now := time.Unix(1700000000, 0).UTC()
	first := limiter.CurrentIPHash("127.0.0.1", now)
	second := limiter.CurrentIPHash("127.0.0.1", now)

	if first != second {
		t.Fatalf("expected stable hash, got %q and %q", first, second)
	}
}

func TestCheckReturnsCooldown(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0).UTC()
	limiter := NewLimiter(time.Minute, 15*time.Minute, 5, "salt", time.Hour)
	err := limiter.Check(context.Background(), stubStore{
		latest:    now.Add(-30 * time.Second),
		hasLatest: true,
	}, "8.8.8.8", now)
	if err == nil {
		t.Fatal("expected cooldown error")
	}
}

func TestCheckReturnsRateLimited(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0).UTC()
	limiter := NewLimiter(time.Minute, 15*time.Minute, 2, "salt", time.Hour)
	err := limiter.Check(context.Background(), stubStore{
		count: 2,
	}, "8.8.8.8", now)
	if err != ErrRateLimited {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}
}

func TestCheckSkipsLANIPs(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0).UTC()
	limiter := NewLimiter(time.Minute, 15*time.Minute, 2, "salt", time.Hour)
	err := limiter.Check(context.Background(), stubStore{
		count:     99,
		latest:    now,
		hasLatest: true,
	}, "192.168.1.25", now)
	if err != nil {
		t.Fatalf("expected LAN IP to bypass limiter, got %v", err)
	}
}

func TestCheckSkipsLoopbackIPv6(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0).UTC()
	limiter := NewLimiter(time.Minute, 15*time.Minute, 2, "salt", time.Hour)
	err := limiter.Check(context.Background(), stubStore{
		count:     99,
		latest:    now,
		hasLatest: true,
	}, "::1", now)
	if err != nil {
		t.Fatalf("expected loopback IP to bypass limiter, got %v", err)
	}
}

func TestCurrentIPHashRotatesByBucket(t *testing.T) {
	t.Parallel()

	limiter := NewLimiter(time.Minute, 15*time.Minute, 2, "salt", time.Hour)
	first := limiter.CurrentIPHash("8.8.8.8", time.Unix(1700000000, 0).UTC())
	second := limiter.CurrentIPHash("8.8.8.8", time.Unix(1700003601, 0).UTC())
	if first == second {
		t.Fatal("expected rotating IP hash to change across buckets")
	}
}
