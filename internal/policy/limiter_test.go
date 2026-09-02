package policy

import (
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	now := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	limiter := NewRateLimiterWithClock(func() time.Time { return now })
	if !limiter.Allow("team-a", 2) {
		t.Fatal("first request denied")
	}
	if !limiter.Allow("team-a", 2) {
		t.Fatal("second request denied")
	}
	if limiter.Allow("team-a", 2) {
		t.Fatal("third request allowed")
	}
	now = now.Add(time.Minute)
	if !limiter.Allow("team-a", 2) {
		t.Fatal("request after window denied")
	}
}

func TestRateLimiterStatusIsReadOnlyAndExpires(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	limiter := NewRateLimiterWithClock(func() time.Time { return now })
	blocked, retryAfter := limiter.Status("key", 1)
	if blocked || retryAfter != 0 {
		t.Fatal("empty bucket should not be blocked")
	}
	if !limiter.Allow("key", 1) {
		t.Fatal("first request denied")
	}
	blocked, retryAfter = limiter.Status("key", 1)
	if !blocked || retryAfter != 60 {
		t.Fatalf("status = blocked=%v retry=%d, want true/60", blocked, retryAfter)
	}
	if snapshot := limiter.Snapshot()["key"]; snapshot != 1 {
		t.Fatalf("status must not consume requests, snapshot=%d want 1", snapshot)
	}
	now = now.Add(time.Minute)
	blocked, retryAfter = limiter.Status("key", 1)
	if blocked || retryAfter != 0 {
		t.Fatalf("expired status = blocked=%v retry=%d, want false/0", blocked, retryAfter)
	}
}
