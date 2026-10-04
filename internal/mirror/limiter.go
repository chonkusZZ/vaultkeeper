package mirror

import (
	"context"
	"io"
	"sync"
	"time"
)

// Limiter is a simple shared bytes/second throttle.
type Limiter struct {
	mu   sync.Mutex
	rate float64
	next time.Time
}

func NewLimiter(kbPerSec int) *Limiter {
	if kbPerSec <= 0 {
		return nil
	}
	return &Limiter{rate: float64(kbPerSec) * 1024}
}

func (l *Limiter) wait(ctx context.Context, n int) {
	if l == nil || n <= 0 {
		return
	}
	d := time.Duration(float64(n) / l.rate * float64(time.Second))
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	l.next = l.next.Add(d)
	w := l.next.Sub(now) - 100*time.Millisecond
	l.mu.Unlock()
	if w > 0 {
		select {
		case <-time.After(w):
		case <-ctx.Done():
		}
	}
}

type limitedReader struct {
	ctx context.Context
	r   io.Reader
	l   *Limiter
	n   *int64 // bytes counter (atomic add by caller-provided func)
	add func(int64)
}

func (r *limitedReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.l.wait(r.ctx, n)
		if r.add != nil {
			r.add(int64(n))
		}
	}
	return n, err
}
