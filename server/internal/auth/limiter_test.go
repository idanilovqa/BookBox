package auth

import (
	"sync"
	"testing"
	"time"
)

func TestLoginLimiterBlocksAfterFiveFailuresAndResetsOnSuccess(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	limiter := NewLoginLimiter(10000, func() time.Time { return now })
	for attempt := 0; attempt < 5; attempt++ {
		if allowed, _ := limiter.Allow("owner", "192.0.2.1:1000"); !allowed {
			t.Fatalf("attempt %d unexpectedly blocked", attempt+1)
		}
		limiter.RecordFailure("owner", "192.0.2.1:1000")
	}
	allowed, retry := limiter.Allow("owner", "192.0.2.1:2000")
	if allowed || retry != 10*time.Minute {
		t.Fatalf("sixth attempt = %v, %v; want blocked for 10m", allowed, retry)
	}

	limiter.RecordSuccess("owner", "192.0.2.1:3000")
	if allowed, _ := limiter.Allow("owner", "192.0.2.1:4000"); !allowed {
		t.Fatal("successful login did not reset bucket")
	}
}

func TestLoginLimiterCanonicalizesUsernameAndDirectIP(t *testing.T) {
	limiter := NewLoginLimiter(10000, time.Now)
	for attempt := 0; attempt < 5; attempt++ {
		limiter.RecordFailure(" Owner ", "[::ffff:192.0.2.1]:1000")
	}
	if allowed, _ := limiter.Allow("owner", "192.0.2.1:2000"); allowed {
		t.Fatal("equivalent username/IP bypassed the limiter")
	}
}

func TestLoginLimiterExpiresBucketsAndFailsClosedAtCapacity(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	limiter := NewLoginLimiter(2, func() time.Time { return now })
	limiter.RecordFailure("owner", "192.0.2.1:1")
	limiter.RecordFailure("owner", "192.0.2.2:1")
	if allowed, retry := limiter.Allow("owner", "192.0.2.3:1"); allowed || retry != 10*time.Minute {
		t.Fatalf("full limiter = %v, %v; want fail closed for 10m", allowed, retry)
	}

	now = now.Add(10*time.Minute + time.Second)
	if allowed, retry := limiter.Allow("owner", "192.0.2.3:1"); !allowed || retry != 0 {
		t.Fatalf("expired limiter = %v, %v; want allowed", allowed, retry)
	}
}

func TestLoginLimiterIsConcurrencySafe(t *testing.T) {
	limiter := NewLoginLimiter(10000, time.Now)
	var wait sync.WaitGroup
	for index := 0; index < 100; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			limiter.RecordFailure("owner", "192.0.2.1:1")
			_, _ = limiter.Allow("owner", "192.0.2.1:2")
		}()
	}
	wait.Wait()
}
