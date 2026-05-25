package auth

import (
	"sync"
	"time"
)

// LoginRateLimit is a per-email bucket. Five failures in 15 minutes
// triggers a 60s back-off (the bucket starts refusing). Lives in
// memory; restart resets it. Scale-out concerns are out of scope.
type LoginRateLimit struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	failures   int
	firstFail  time.Time
	blockUntil time.Time
}

const (
	maxFailures   = 5
	failureWindow = 15 * time.Minute
	blockFor      = 60 * time.Second
)

func NewLoginRateLimit() *LoginRateLimit {
	return &LoginRateLimit{buckets: map[string]*bucket{}}
}

// Allow returns true if the email may attempt to login right now.
func (l *LoginRateLimit) Allow(email string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[email]
	if b == nil {
		return true
	}
	now := time.Now()
	if !b.blockUntil.IsZero() && now.Before(b.blockUntil) {
		return false
	}
	return true
}

// Failed registers a login failure for the email.
func (l *LoginRateLimit) Failed(email string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b := l.buckets[email]
	if b == nil || now.Sub(b.firstFail) > failureWindow {
		l.buckets[email] = &bucket{failures: 1, firstFail: now}
		return
	}
	b.failures++
	if b.failures >= maxFailures {
		b.blockUntil = now.Add(blockFor)
		b.failures = 0
		b.firstFail = now
	}
}

// Reset clears the bucket on a successful login.
func (l *LoginRateLimit) Reset(email string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, email)
}
