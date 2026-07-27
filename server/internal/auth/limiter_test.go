package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoginLimiterBlocksAfterFiveFailuresAndResetsOnSuccess(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	limiter := NewLoginLimiter(10000, func() time.Time { return now })
	for attempt := 0; attempt < 5; attempt++ {
		reservation, allowed, _ := limiter.Reserve("owner", "192.0.2.1:1000")
		if !allowed {
			t.Fatalf("attempt %d unexpectedly blocked", attempt+1)
		}
		reservation.Failure()
	}
	_, allowed, retry := limiter.Reserve("owner", "192.0.2.1:2000")
	if allowed || retry != 10*time.Minute {
		t.Fatalf("sixth attempt = %v, %v; want blocked for 10m", allowed, retry)
	}

	now = now.Add(10*time.Minute + time.Second)
	success, allowed, _ := limiter.Reserve("owner", "192.0.2.1:3000")
	if !allowed {
		t.Fatal("attempt after expiry blocked")
	}
	success.Success()
	if _, allowed, _ := limiter.Reserve("owner", "192.0.2.1:4000"); !allowed {
		t.Fatal("successful login did not reset bucket")
	}
}

func TestLoginLimiterCanonicalizesUsernameAndDirectIP(t *testing.T) {
	limiter := NewLoginLimiter(10000, time.Now)
	for attempt := 0; attempt < 5; attempt++ {
		reservation, allowed, _ := limiter.Reserve(" Owner ", "[::ffff:192.0.2.1]:1000")
		if !allowed {
			t.Fatalf("attempt %d blocked", attempt+1)
		}
		reservation.Failure()
	}
	if _, allowed, _ := limiter.Reserve("owner", "192.0.2.1:2000"); allowed {
		t.Fatal("equivalent username/IP bypassed the limiter")
	}
}

func TestLoginLimiterExpiresBucketsAndFailsClosedAtCapacity(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	limiter := NewLoginLimiter(2, func() time.Time { return now })
	first, _, _ := limiter.Reserve("owner", "192.0.2.1:1")
	first.Failure()
	second, _, _ := limiter.Reserve("owner", "192.0.2.2:1")
	second.Failure()
	if _, allowed, retry := limiter.Reserve("owner", "192.0.2.3:1"); allowed || retry != 10*time.Minute {
		t.Fatalf("full limiter = %v, %v; want fail closed for 10m", allowed, retry)
	}

	now = now.Add(10*time.Minute + time.Second)
	if _, allowed, retry := limiter.Reserve("owner", "192.0.2.3:1"); !allowed || retry != 0 {
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
			if reservation, allowed, _ := limiter.Reserve("owner", "192.0.2.1:1"); allowed {
				reservation.Failure()
			}
		}()
	}
	wait.Wait()
}

func TestLoginLimiterAtomicallyCapsInflightReservations(t *testing.T) {
	limiter := NewLoginLimiter(10000, time.Now)
	reservations := make([]*LoginReservation, 0, loginFailureLimit)
	for attempt := 0; attempt < loginFailureLimit; attempt++ {
		reservation, allowed, retry := limiter.Reserve("owner", "192.0.2.1:1000")
		if !allowed || retry != 0 || reservation == nil {
			t.Fatalf("reservation %d = %#v, %v, %v", attempt+1, reservation, allowed, retry)
		}
		reservations = append(reservations, reservation)
	}
	if reservation, allowed, retry := limiter.Reserve("owner", "192.0.2.1:2000"); allowed ||
		reservation != nil || retry <= 0 {
		t.Fatalf("sixth reservation = %#v, %v, %v; want blocked", reservation, allowed, retry)
	}

	reservations[0].Cancel()
	replacement, allowed, _ := limiter.Reserve("owner", "192.0.2.1:3000")
	if !allowed {
		t.Fatal("cancelled reservation did not release capacity")
	}
	replacement.Failure()
	for _, reservation := range reservations[1:] {
		reservation.Failure()
	}
	if _, allowed, retry := limiter.Reserve("owner", "192.0.2.1:4000"); allowed || retry <= 0 {
		t.Fatalf("post-failure reservation = %v, %v; want blocked", allowed, retry)
	}
}

func TestLoginReservationSuccessResetsAndInvalidatesLateCompletion(t *testing.T) {
	limiter := NewLoginLimiter(10000, time.Now)
	first, allowed, _ := limiter.Reserve(" Owner ", "[::ffff:192.0.2.1]:1000")
	if !allowed {
		t.Fatal("first reservation blocked")
	}
	second, allowed, _ := limiter.Reserve("owner", "192.0.2.1:2000")
	if !allowed {
		t.Fatal("second reservation blocked")
	}
	first.Success()
	second.Failure()

	for attempt := 0; attempt < loginFailureLimit; attempt++ {
		reservation, ok, _ := limiter.Reserve("owner", "192.0.2.1:3000")
		if !ok {
			t.Fatalf("attempt %d blocked after success reset", attempt+1)
		}
		reservation.Cancel()
	}
}

func TestLoginLimiterCancelledReservationFreesBoundedBucket(t *testing.T) {
	limiter := NewLoginLimiter(1, time.Now)
	reservation, allowed, _ := limiter.Reserve("owner", "192.0.2.1:1")
	if !allowed {
		t.Fatal("first key blocked")
	}
	if _, allowed, retry := limiter.Reserve("owner", "192.0.2.2:1"); allowed || retry <= 0 {
		t.Fatalf("full limiter = %v, %v; want fail closed", allowed, retry)
	}
	reservation.Cancel()
	next, allowed, _ := limiter.Reserve("owner", "192.0.2.2:1")
	if !allowed {
		t.Fatal("cancelled empty bucket was not removed")
	}
	next.Cancel()
}

func TestPasswordVerifierCapsConcurrentArgonWork(t *testing.T) {
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	verifier := NewPasswordVerifier(2, func(_, _ string) (bool, error) {
		current := active.Add(1)
		for {
			seen := maximum.Load()
			if current <= seen || maximum.CompareAndSwap(seen, current) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		active.Add(-1)
		return false, nil
	})

	var wait sync.WaitGroup
	var ready sync.WaitGroup
	ready.Add(3)
	start := make(chan struct{})
	for index := 0; index < 3; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			ready.Done()
			<-start
			_, _ = verifier.Verify(context.Background(), "hash", "candidate")
		}()
	}
	ready.Wait()
	close(start)
	<-entered
	<-entered
	select {
	case <-entered:
		t.Fatal("third verifier entered before a slot was released")
	case <-time.After(150 * time.Millisecond):
	}
	release <- struct{}{}
	<-entered
	release <- struct{}{}
	release <- struct{}{}
	wait.Wait()
	if maximum.Load() != 2 {
		t.Fatalf("maximum concurrent verifications = %d, want 2", maximum.Load())
	}
}

func TestPasswordVerifierReturnsContextCancellationAfterVerification(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	verifier := NewPasswordVerifier(1, func(_, _ string) (bool, error) {
		cancel()
		return true, nil
	})
	ok, err := verifier.Verify(ctx, "hash", "candidate")
	if ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("Verify() = %v, %v; want false, context.Canceled", ok, err)
	}
}
