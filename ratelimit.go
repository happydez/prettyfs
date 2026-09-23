package pretty

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter reports whether key may access item now.
// item is "" for plain requests. Must be safe for concurrent use.
type Limiter interface {
	Allow(key, item string) (ok bool, retryAfter time.Duration)
}

// defaultMaxKeys caps tracked keys, so a botnet can't exhaust memory.
const defaultMaxKeys = 100_000

// windowLimiter allows at most limit items per key within any span of per.
// Repeated access to the same item is free.
type windowLimiter struct {
	mu        sync.Mutex
	limit     int
	per       time.Duration
	maxKeys   int
	hits      map[string][]hit // sorted by time
	lastSweep time.Time
	now       func() time.Time
}

type hit struct {
	at   time.Time
	item string
}

// newWindowLimiter returns nil if the limit is disabled.
func newWindowLimiter(requests int, per time.Duration) *windowLimiter {
	if requests <= 0 || per <= 0 {
		return nil
	}
	return &windowLimiter{
		limit:   requests,
		per:     per,
		maxKeys: defaultMaxKeys,
		hits:    make(map[string][]hit),
		now:     time.Now,
	}
}

func (l *windowLimiter) Allow(key, item string) (bool, time.Duration) {
	now := l.now()
	cutoff := now.Add(-l.per)

	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastSweep) > l.per {
		l.sweep(now, cutoff)
	}

	hs, known := l.hits[key]
	if !known && len(l.hits) >= l.maxKeys {
		if now.Sub(l.lastSweep) > time.Second {
			l.sweep(now, cutoff)
		}
		if len(l.hits) >= l.maxKeys {
			return false, time.Second
		}
	}

	i := 0
	for i < len(hs) && !hs[i].at.After(cutoff) {
		i++
	}
	hs = hs[i:]
	if item != "" {
		for j := range hs {
			if hs[j].item == item {
				l.hits[key] = hs
				return true, 0
			}
		}
	}

	if len(hs) >= l.limit {
		l.hits[key] = hs
		return false, hs[0].at.Sub(cutoff)
	}
	l.hits[key] = append(hs, hit{at: now, item: item})

	return true, 0
}

// sweep drops keys with no hits in the window.
func (l *windowLimiter) sweep(now, cutoff time.Time) {
	for k, hs := range l.hits {
		if len(hs) == 0 || !hs[len(hs)-1].at.After(cutoff) {
			delete(l.hits, k)
		}
	}
	l.lastSweep = now
}

// tokenBucket is a single shared bucket; key and item are ignored.
type tokenBucket struct {
	l *rate.Limiter
}

// newTokenBucket returns nil if the limit is disabled.
func newTokenBucket(requests int, per time.Duration) *tokenBucket {
	if requests <= 0 || per <= 0 {
		return nil
	}
	return &tokenBucket{rate.NewLimiter(rate.Every(per/time.Duration(requests)), requests)}
}

func (t *tokenBucket) Allow(_, _ string) (bool, time.Duration) {
	res := t.l.Reserve()
	if !res.OK() {
		return false, time.Second
	}
	if d := res.Delay(); d > 0 {
		res.Cancel()
		return false, d
	}
	return true, 0
}
