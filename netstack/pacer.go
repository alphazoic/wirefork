package netstack

// wirefork: upload limit on packets leaving the userspace TCP stack.
//
// gVisor's TCP sends a whole congestion window at once. On a long path
// (e.g. Cambodia → Japan VPN server → MEGA) a burst well above the line's
// upload capacity overflows a buffer on the way and is lost as one block, and
// gVisor then resends that block one packet per round trip for a minute or
// more. Spacing outgoing packets at about the line's capacity avoids the
// bursts. Measurements: docs/for-user/wireguard-measurements.md in the M3 repo.
//
// The limiter never drops a packet: the sending goroutine waits instead.

import (
	"sync"
	"sync/atomic"
	"time"
)

type Pacer struct {
	mu     sync.Mutex
	rate   float64 // bytes per second; 0 = no limit
	burst  float64 // bytes that may leave at once
	tokens float64
	last   time.Time

	waitNs atomic.Int64 // total time senders spent waiting (for "is the limit what holds speed down?")
	sent   atomic.Int64 // total bytes that passed through
}

// Burst: 20 ms worth of the rate, between 32 KB and 256 KB (256 KB at 13 MB/s
// is what was tested).
func burstFor(rate float64) float64 {
	b := rate * 0.020
	if b < 32e3 {
		b = 32e3
	}
	if b > 256e3 {
		b = 256e3
	}
	return b
}

// SetRate sets the limit in bytes per second (0 = no limit).
func (p *Pacer) SetRate(rate float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rate = rate
	p.burst = burstFor(rate)
	if p.tokens > p.burst {
		p.tokens = p.burst
	}
}

func (p *Pacer) Rate() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rate
}

// WaitNs and Sent are running totals; callers compare two readings.
func (p *Pacer) WaitNs() int64 { return p.waitNs.Load() }
func (p *Pacer) Sent() int64   { return p.sent.Load() }

// Wait blocks until n bytes may leave.
func (p *Pacer) Wait(n int) {
	p.sent.Add(int64(n))
	p.mu.Lock()
	if p.rate <= 0 {
		p.mu.Unlock()
		return
	}
	now := time.Now()
	if !p.last.IsZero() {
		p.tokens += now.Sub(p.last).Seconds() * p.rate
	}
	p.last = now
	if p.tokens > p.burst {
		p.tokens = p.burst
	}
	p.tokens -= float64(n)
	var wait time.Duration
	if p.tokens < 0 {
		wait = time.Duration(-p.tokens / p.rate * 1e9)
	}
	p.mu.Unlock()
	if wait > 0 {
		time.Sleep(wait)
		p.waitNs.Add(int64(wait))
	}
}
