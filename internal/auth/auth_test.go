package auth

import (
	"errors"
	"testing"
	"time"
)

func TestHashAndVerify(t *testing.T) {
	pwd := "correcthorsebatterystaple"
	h, err := HashPassword(pwd)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := VerifyPassword(h, pwd); err != nil {
		t.Fatalf("verify good: %v", err)
	}
	if err := VerifyPassword(h, pwd+"x"); !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("verify bad: got %v want ErrPasswordMismatch", err)
	}
}

func TestPasswordPolicy(t *testing.T) {
	if _, err := HashPassword("short"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("short: got %v", err)
	}
}

func TestRateLimitBlocksAfterFiveFailures(t *testing.T) {
	rl := NewLoginRateLimit()
	const email = "x@y"
	for i := 0; i < 5; i++ {
		if !rl.Allow(email) {
			t.Fatalf("attempt %d unexpectedly blocked", i+1)
		}
		rl.Failed(email)
	}
	if rl.Allow(email) {
		t.Fatalf("expected to be blocked after 5 failures")
	}
}

func TestRateLimitResetsOnSuccess(t *testing.T) {
	rl := NewLoginRateLimit()
	const email = "x@y"
	rl.Failed(email)
	rl.Failed(email)
	rl.Reset(email)
	for i := 0; i < 5; i++ {
		if !rl.Allow(email) {
			t.Fatalf("attempt %d after reset unexpectedly blocked", i+1)
		}
		rl.Failed(email)
	}
	// after the fifth failure we should be blocked again
	if rl.Allow(email) {
		t.Fatalf("expected to be blocked after 5 fresh failures")
	}
}

// Time-sensitive check: a long-past first failure should not count
// toward the window. (Cheap because failureWindow uses wall clock,
// and we manipulate the bucket directly.)
func TestRateLimitWindowExpires(t *testing.T) {
	rl := NewLoginRateLimit()
	const email = "x@y"
	rl.Failed(email)
	rl.buckets[email].firstFail = time.Now().Add(-failureWindow - time.Minute)
	rl.Failed(email)
	if rl.buckets[email].failures != 1 {
		t.Fatalf("expected bucket reset after window expiry; got failures=%d", rl.buckets[email].failures)
	}
}
