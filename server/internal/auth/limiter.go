package auth

import (
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

const (
	loginFailureLimit = 5
	loginWindow       = 10 * time.Minute
)

type loginBucket struct {
	failures int
	expires  time.Time
}

type LoginLimiter struct {
	mu         sync.Mutex
	buckets    map[string]loginBucket
	maxEntries int
	now        func() time.Time
}

func NewLoginLimiter(maxEntries int, now func() time.Time) *LoginLimiter {
	if maxEntries <= 0 {
		maxEntries = 1
	}
	if now == nil {
		now = time.Now
	}
	return &LoginLimiter{
		buckets:    make(map[string]loginBucket),
		maxEntries: maxEntries,
		now:        now,
	}
}

func (limiter *LoginLimiter) Allow(username, remoteAddress string) (bool, time.Duration) {
	now := limiter.now()
	key := loginBucketKey(username, remoteAddress)
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	limiter.removeExpired(now)
	if bucket, exists := limiter.buckets[key]; exists {
		if bucket.failures >= loginFailureLimit {
			return false, bucket.expires.Sub(now)
		}
		return true, 0
	}
	if len(limiter.buckets) >= limiter.maxEntries {
		return false, loginWindow
	}
	return true, 0
}

func (limiter *LoginLimiter) RecordFailure(username, remoteAddress string) {
	now := limiter.now()
	key := loginBucketKey(username, remoteAddress)
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	limiter.removeExpired(now)
	bucket, exists := limiter.buckets[key]
	if !exists {
		if len(limiter.buckets) >= limiter.maxEntries {
			return
		}
		bucket.expires = now.Add(loginWindow)
	}
	bucket.failures++
	limiter.buckets[key] = bucket
}

func (limiter *LoginLimiter) RecordSuccess(username, remoteAddress string) {
	key := loginBucketKey(username, remoteAddress)
	limiter.mu.Lock()
	delete(limiter.buckets, key)
	limiter.mu.Unlock()
}

func (limiter *LoginLimiter) removeExpired(now time.Time) {
	for key, bucket := range limiter.buckets {
		if !now.Before(bucket.expires) {
			delete(limiter.buckets, key)
		}
	}
}

func loginBucketKey(username, remoteAddress string) string {
	return strings.ToLower(strings.TrimSpace(username)) + "\x00" + canonicalRemoteIP(remoteAddress)
}

func canonicalRemoteIP(remoteAddress string) string {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		host = remoteAddress
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return "<invalid>"
	}
	return address.Unmap().String()
}
