package auth

import (
	"sync"
	"time"
)

type attempt struct {
	count int
	until time.Time
}

type Limiter struct {
	mu      sync.Mutex
	entries map[string]attempt
}

func NewLimiter() *Limiter { return &Limiter{entries: map[string]attempt{}} }

func (l *Limiter) Allowed(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.entries[key]
	if !ok {
		return true
	}
	return time.Now().After(a.until)
}

func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.entries[key]
	a.count++
	if a.count >= 5 {
		a.until = time.Now().Add(15 * time.Minute)
		a.count = 0
	}
	l.entries[key] = a
}

func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.entries, key)
	l.mu.Unlock()
}
