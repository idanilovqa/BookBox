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
	inflight int
	expires  time.Time
	id       uint64
}

type LoginLimiter struct {
	mu         sync.Mutex
	buckets    map[string]loginBucket
	maxEntries int
	now        func() time.Time
	nextID     uint64
}

type LoginReservation struct {
	limiter *LoginLimiter
	key     string
	bucket  uint64
	once    sync.Once
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

func (limiter *LoginLimiter) Reserve(
	username string,
	remoteAddress string,
) (*LoginReservation, bool, time.Duration) {
	now := limiter.now()
	key := loginBucketKey(username, remoteAddress)
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	limiter.removeExpired(now)
	bucket, exists := limiter.buckets[key]
	if exists && bucket.failures+bucket.inflight >= loginFailureLimit {
		retry := bucket.expires.Sub(now)
		if retry <= 0 {
			retry = loginWindow
		}
		return nil, false, retry
	}
	if !exists {
		if len(limiter.buckets) >= limiter.maxEntries {
			return nil, false, loginWindow
		}
		limiter.nextID++
		bucket = loginBucket{expires: now.Add(loginWindow), id: limiter.nextID}
	}
	bucket.inflight++
	limiter.buckets[key] = bucket
	return &LoginReservation{limiter: limiter, key: key, bucket: bucket.id}, true, 0
}

func (reservation *LoginReservation) Failure() {
	reservation.finish(reservationFailure)
}

func (reservation *LoginReservation) Success() {
	reservation.finish(reservationSuccess)
}

func (reservation *LoginReservation) Cancel() {
	reservation.finish(reservationCancel)
}

type reservationResult uint8

const (
	reservationFailure reservationResult = iota
	reservationSuccess
	reservationCancel
)

func (reservation *LoginReservation) finish(result reservationResult) {
	if reservation == nil || reservation.limiter == nil {
		return
	}
	reservation.once.Do(func() {
		limiter := reservation.limiter
		now := limiter.now()
		limiter.mu.Lock()
		defer limiter.mu.Unlock()
		bucket, exists := limiter.buckets[reservation.key]
		if !exists || bucket.id != reservation.bucket {
			return
		}
		if result == reservationSuccess {
			delete(limiter.buckets, reservation.key)
			return
		}
		if bucket.inflight > 0 {
			bucket.inflight--
		}
		if result == reservationFailure {
			if !now.Before(bucket.expires) {
				bucket.expires = now.Add(loginWindow)
			}
			bucket.failures++
		}
		if bucket.failures == 0 && bucket.inflight == 0 {
			delete(limiter.buckets, reservation.key)
			return
		}
		limiter.buckets[reservation.key] = bucket
	})
}

func (limiter *LoginLimiter) removeExpired(now time.Time) {
	for key, bucket := range limiter.buckets {
		if !now.Before(bucket.expires) && bucket.inflight == 0 {
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
