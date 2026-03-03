package rate_limit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

var ErrRateLimited = errors.New("rate limited")

type CooldownError struct {
	Remaining time.Duration
}

func (e CooldownError) Error() string {
	return fmt.Sprintf("cooldown active for %s", e.Remaining.Round(time.Second))
}

type Store interface {
	CountRecentByIPHashes(ctx context.Context, ipHashes []string, since time.Time) (int, error)
	LatestPostTimeByIPHashes(ctx context.Context, ipHashes []string) (time.Time, bool, error)
}

type Limiter struct {
	cooldown time.Duration
	window   time.Duration
	maxPosts int
	rotation time.Duration
	hmacKey  []byte
}

func NewLimiter(cooldown, window time.Duration, maxPosts int, salt string, rotation time.Duration) *Limiter {
	return &Limiter{
		cooldown: cooldown,
		window:   window,
		maxPosts: maxPosts,
		rotation: rotation,
		hmacKey:  []byte(salt),
	}
}

func (l *Limiter) CurrentIPHash(ip string, now time.Time) string {
	return l.hashForBucket(ip, bucketStart(now, l.rotation))
}

func (l *Limiter) hashForBucket(ip string, bucket time.Time) string {
	mac := hmac.New(sha256.New, l.hmacKey)
	mac.Write([]byte(bucket.UTC().Format(time.RFC3339)))
	mac.Write([]byte{0})
	mac.Write([]byte(ip))
	return hex.EncodeToString(mac.Sum(nil))
}

func (l *Limiter) Check(ctx context.Context, store Store, ip string, now time.Time) error {
	if isLANIP(ip) {
		return nil
	}

	cooldownHashes := l.hashesForRange(ip, now.Add(-l.cooldown), now)
	windowHashes := l.hashesForRange(ip, now.Add(-l.window), now)

	if latest, ok, err := store.LatestPostTimeByIPHashes(ctx, cooldownHashes); err != nil {
		return err
	} else if ok {
		remaining := l.cooldown - now.Sub(latest)
		if remaining > 0 {
			return CooldownError{Remaining: remaining}
		}
	}

	count, err := store.CountRecentByIPHashes(ctx, windowHashes, now.Add(-l.window))
	if err != nil {
		return err
	}
	if count >= l.maxPosts {
		return ErrRateLimited
	}

	return nil
}

func (l *Limiter) hashesForRange(ip string, start, end time.Time) []string {
	if l.rotation <= 0 {
		return nil
	}

	start = start.UTC()
	end = end.UTC()
	if end.Before(start) {
		start, end = end, start
	}

	hashes := make([]string, 0, 4)
	seen := map[string]struct{}{}
	for bucket := bucketStart(start, l.rotation); !bucket.After(end); bucket = bucket.Add(l.rotation) {
		hash := l.hashForBucket(ip, bucket)
		if _, ok := seen[hash]; ok {
			continue
		}
		seen[hash] = struct{}{}
		hashes = append(hashes, hash)
	}
	return hashes
}

func bucketStart(now time.Time, rotation time.Duration) time.Time {
	unix := now.UTC().Unix()
	span := int64(rotation / time.Second)
	if span <= 0 {
		return now.UTC()
	}
	return time.Unix((unix/span)*span, 0).UTC()
}

func isLANIP(ip string) bool {
	if ip == "localhost" {
		return true
	}

	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}

	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast()
}
