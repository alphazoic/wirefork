package netstack

import (
	"testing"
	"time"
)

// 2 MB through a 10 MB/s limit (burst 200 KB) takes about 0.18 s; no limit takes ~0.
func TestPacerRate(t *testing.T) {
	p := &Pacer{}
	p.SetRate(10e6)
	begin := time.Now()
	for i := 0; i < 2000; i++ {
		p.Wait(1000)
	}
	if d := time.Since(begin); d < 150*time.Millisecond || d > 400*time.Millisecond {
		t.Fatalf("2 MB at 10 MB/s took %v", d)
	}
	if p.Sent() != 2e6 || p.WaitNs() == 0 {
		t.Fatalf("sent %d, waited %d ns", p.Sent(), p.WaitNs())
	}
	p.SetRate(0)
	begin = time.Now()
	for i := 0; i < 2000; i++ {
		p.Wait(1000)
	}
	if d := time.Since(begin); d > 50*time.Millisecond {
		t.Fatalf("no limit took %v", d)
	}
}
