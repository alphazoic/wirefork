package wireproxy

// wirefork: chooses and adjusts the upload limit (netstack/pacer.go).
//
//   --upload-limit off      no limit (default)
//   --upload-limit auto     measure at start (upload test through the tunnel), then adjust
//   --upload-limit auto:N   start at N MB/s without measuring, then adjust
//   --upload-limit N        fixed N MB/s, no adjustment
//
// Adjustment, once per second: a burst loss (resends > max(500, 5% of packets
// sent); normal background loss on the test line was under ~60 per second,
// burst losses 2,000-5,000) lowers the limit by 20%. While the limit is what holds speed down
// (senders waited > 20% of the second) and there was no loss for 5 s, it rises
// 3% per second, or 0.5% per second near the last limit that caused a loss.
// Each change of 10% or more, and each loss, is logged as
//   wirefork: upload limit 13.2 MB/s (reason)
// so a caller can keep the last value and pass it back with auto:N.

import (
	"context"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultProbeURL = "https://speed.cloudflare.com/__up"
	limitFloor      = 0.25e6 // bytes/s
	fallbackStart   = 8e6    // used when the measurement fails
)

type UploadLimitConfig struct {
	Mode     string  // "auto", "fixed" or "off"
	Start    float64 // bytes/s; auto: 0 = measure first
	ProbeURL string
}

func ParseUploadLimit(s string) (UploadLimitConfig, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	mbps := func(v string) (float64, error) {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 || math.IsInf(f, 0) {
			return 0, fmt.Errorf("upload limit: %q is not a positive number of MB/s", v)
		}
		return f * 1e6, nil
	}
	switch {
	case s == "auto":
		return UploadLimitConfig{Mode: "auto"}, nil
	case s == "" || s == "off":
		return UploadLimitConfig{Mode: "off"}, nil
	case strings.HasPrefix(s, "auto:"):
		v, err := mbps(strings.TrimPrefix(s, "auto:"))
		return UploadLimitConfig{Mode: "auto", Start: v}, err
	default:
		v, err := mbps(s)
		return UploadLimitConfig{Mode: "fixed", Start: v}, err
	}
}

func mb(rate float64) string { return fmt.Sprintf("%.1f MB/s", rate/1e6) }

// StartUploadLimit applies cfg to the tunnel and, in auto mode, starts the
// measurement and adjustment in the background.
func (vt *VirtualTun) StartUploadLimit(ctx context.Context, cfg UploadLimitConfig) {
	p := vt.Tnet.Pacer()
	switch cfg.Mode {
	case "off":
		p.SetRate(0)
		log.Printf("wirefork: upload limit off")
		return
	case "fixed":
		p.SetRate(cfg.Start)
		log.Printf("wirefork: upload limit %s (fixed)", mb(cfg.Start))
		return
	}
	go func() {
		start := cfg.Start
		reason := "start value"
		if start == 0 {
			url := cfg.ProbeURL
			if url == "" {
				url = DefaultProbeURL
			}
			est, err := vt.probeUpload(ctx, url)
			switch {
			case err != nil:
				start, reason = fallbackStart, "measurement failed: "+err.Error()
			case est < limitFloor:
				start, reason = fallbackStart, fmt.Sprintf("measurement too low (%s)", mb(est))
			default:
				start, reason = est, fmt.Sprintf("measured %s", mb(est))
			}
		}
		if start < limitFloor {
			start = limitFloor
		}
		p.SetRate(start)
		log.Printf("wirefork: upload limit %s (%s)", mb(start), reason)
		vt.adjustUploadLimit(ctx, start)
	}()
}

type limitSample struct {
	sent, waitNs     int64
	segs, retransmit uint64
}

func (vt *VirtualTun) sample() limitSample {
	p, c := vt.Tnet.Pacer(), vt.Tnet.TCPCounters()
	return limitSample{sent: p.Sent(), waitNs: p.WaitNs(), segs: c.SegmentsSent, retransmit: c.Retransmits}
}

// probeUpload uploads through the tunnel for up to 6 s (6 connections, each
// sending 2 MB requests one after another) while raising the limit from
// 2 MB/s by 25% every 200 ms, until a burst of resends, until the limit is
// 1.5x what arrives, or until the senders no longer fill the limit. It returns the highest rate the server confirmed
// receiving over 2 s (bytes of completed requests). Counting bytes leaving
// wireproxy instead reads too high: buffers on the way take more than the
// line delivers for a while (20 MB/s measured that way on a ~14 MB/s line).
func (vt *VirtualTun) probeUpload(ctx context.Context, url string) (float64, error) {
	const (
		workers = 6
		reqSize = 2 << 20
		step    = 200 * time.Millisecond
	)
	p := vt.Tnet.Pacer()
	ctx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{DialContext: vt.Tnet.DialContext, MaxIdleConnsPerHost: workers}}

	var delivered atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, io.LimitReader(zeroReader{}, reqSize))
				if err != nil {
					errs <- err
					return
				}
				req.ContentLength = reqSize
				resp, err := client.Do(req)
				if err != nil {
					if ctx.Err() == nil {
						errs <- err
					}
					return
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode/100 != 2 {
					errs <- fmt.Errorf("%s answered %s", url, resp.Status)
					return
				}
				delivered.Add(reqSize)
			}
		}()
	}

	rate := 2e6
	p.SetRate(rate)
	prev := vt.sample()
	var history []int64 // delivered bytes at each step
	best, unfilled, over, ramping, why := 0.0, 0, 0, true, "6 s passed"
	tick := time.NewTicker(step)
	defer tick.Stop()
	begin := time.Now()
loop:
	for time.Since(begin) < 6*time.Second {
		select {
		case <-ctx.Done():
			break loop
		case <-tick.C:
		}
		cur := vt.sample()
		waited := time.Duration(cur.waitNs - prev.waitNs)
		segs, resent := cur.segs-prev.segs, cur.retransmit-prev.retransmit
		prev = cur
		history = append(history, delivered.Load())
		if n := len(history); n > 10 { // 2 s window
			if r := float64(history[n-1]-history[n-11]) / 2; r > best {
				best = r
			}
		}
		if !ramping {
			continue
		}
		if n := len(history); n > 5 && time.Since(begin) > 2*time.Second {
			if arrived := float64(history[n-1] - history[n-6]); rate > 1.5*arrived {
				if over++; over >= 3 {
					ramping, why = false, fmt.Sprintf("line full at %s", mb(rate))
					continue
				}
			} else {
				over = 0
			}
		}
		if float64(resent) > math.Max(100, 0.05*float64(segs)) { // same as adjustUploadLimit, per 200 ms
			ramping, why = false, fmt.Sprintf("burst loss at %s", mb(rate)) // keep measuring what arrives, stop raising
			continue
		}
		if waited > step*3/10 {
			rate *= 1.25
			p.SetRate(rate)
			unfilled = 0
		} else if time.Since(begin) > time.Second {
			if unfilled++; unfilled >= 5 {
				ramping, why = false, fmt.Sprintf("senders stopped filling %s", mb(rate))
			}
		}
	}
	cancel()
	wg.Wait()
	log.Printf("wirefork: measurement: %s; %d MB confirmed in %.1f s; best 2 s: %s", why, delivered.Load()>>20, time.Since(begin).Seconds(), mb(best))
	if best == 0 {
		select {
		case err := <-errs:
			return 0, err
		default:
			return 0, fmt.Errorf("no upload completed within 6 s")
		}
	}
	return best, nil
}

type zeroReader struct{}

func (zeroReader) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = 0
	}
	return len(b), nil
}

func (vt *VirtualTun) adjustUploadLimit(ctx context.Context, rate float64) {
	p := vt.Tnet.Pacer()
	logged, lossRate := rate, 0.0
	quiet := 5 // seconds since the last loss
	prev := vt.sample()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		cur := vt.sample()
		waited := time.Duration(cur.waitNs - prev.waitNs)
		segs, resent := cur.segs-prev.segs, cur.retransmit-prev.retransmit
		prev = cur
		if float64(resent) > math.Max(500, 0.05*float64(segs)) {
			lossRate = rate
			rate = math.Max(limitFloor, rate*0.8)
			p.SetRate(rate)
			quiet = 0
			logged = rate
			log.Printf("wirefork: upload limit %s (burst loss: %d resends)", mb(rate), resent)
			continue
		}
		if quiet++; quiet < 5 || waited < time.Second/5 {
			continue
		}
		if lossRate > 0 && rate >= 0.9*lossRate {
			rate *= 1.005
		} else {
			rate *= 1.03
		}
		p.SetRate(rate)
		if rate >= logged*1.1 {
			logged = rate
			log.Printf("wirefork: upload limit %s (raised)", mb(rate))
		}
	}
}
