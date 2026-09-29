package collect

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// logInterval is how often a collector that keeps failing may log again.
const logInterval = 5 * time.Minute

// probe runs one category of collection under a timeout.
//
// A collection that times out may be stuck in a system call that cannot be
// cancelled, such as statfs on an unreachable NFS mount, so its goroutine
// can outlive the timeout. At most one collection per probe runs at a time:
// while an earlier one is still going the probe reports no value instead of
// starting another, which bounds the stranded goroutines at one per probe.
type probe[T any] struct {
	name    string
	fn      func(context.Context) (T, error)
	timeout time.Duration
	log     *slog.Logger
	now     func() time.Time

	inFlight atomic.Bool

	mu      sync.Mutex
	failing bool
	lastLog time.Time
}

type probeResult[T any] struct {
	value T
	err   error
}

// get collects one value. ok is false when the collection timed out, failed
// or was skipped, in which case the caller leaves the field out of the
// sample: no data this cycle is not the same as zero.
func (p *probe[T]) get(ctx context.Context) (value T, ok bool) {
	var zero T
	if !p.inFlight.CompareAndSwap(false, true) {
		p.failed("previous collection has not returned")
		return zero, false
	}
	// Buffered so that the goroutine can always finish, even long after the
	// caller stopped waiting for it.
	done := make(chan probeResult[T], 1)
	go func() {
		ctx, cancel := context.WithTimeout(ctx, p.timeout)
		defer cancel()
		v, err := p.fn(ctx)
		done <- probeResult[T]{v, err}
		p.inFlight.Store(false)
	}()

	timer := time.NewTimer(p.timeout)
	defer timer.Stop()
	select {
	case r := <-done:
		if r.err != nil {
			p.failed(r.err.Error())
			return zero, false
		}
		p.succeeded()
		return r.value, true
	case <-timer.C:
		p.failed("timed out")
		return zero, false
	case <-ctx.Done():
		return zero, false
	}
}

// failed logs the first failure of a run, then at most one message per
// logInterval for as long as the failure lasts.
func (p *probe[T]) failed(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	switch {
	case !p.failing:
		p.failing = true
		p.lastLog = now
		p.log.Warn("collection failed", "collector", p.name, "reason", reason)
	case now.Sub(p.lastLog) >= logInterval:
		p.lastLog = now
		p.log.Warn("collection still failing", "collector", p.name, "reason", reason)
	}
}

func (p *probe[T]) succeeded() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failing {
		p.failing = false
		p.log.Info("collection recovered", "collector", p.name)
	}
}
