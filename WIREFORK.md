# wirefork

wireproxy (github.com/windtf/wireproxy, ISC) with an upload limit, for M3.

## Why

wireproxy's TCP (gVisor) sends a whole congestion window at once. On a long path (Cambodia → Proton JP#208 → MEGA) one connection sent 2.7 MB in 80 ms (~34 MB/s on a ~14 MB/s line). The whole burst was lost, and gVisor then resent it one packet per round trip (~6 KB/s) for a minute or more. 200 MB uploads took 80–118 s instead of the Proton CLI's 18–23 s. A newer gVisor (2026-11-19) didn't change this. Measurements: M3 repo `docs/for-user/wireguard-measurements.md`.

Spacing outgoing packets at about the line's upload capacity prevents the bursts: 200 MB in 18–27 s with a limit of 13–16 MB/s on that line.

## Changes from wireproxy

- **gVisor TCP loss recovery** (`patches/gvisor-tcp-recovery.patch`, applied in the committed `vendor/`; `scripts/vendor.sh` refreshes and re-patches it after a go.mod change). Three changes in `pkg/tcpip/transport/tcp`, each matching what Linux does:
  1. `SetPipe`: segments RACK marked lost don't count as in flight. Before, the tail of a lost burst stayed counted, pipe stayed above cwnd, and the connection only sent on each RTO (one packet per ~300 ms for a minute or more).
  2. No new recovery (and no second ssthresh cut) while in RTO recovery. Before, RACK flagged the old segments right after an RTO and recomputed ssthresh from cwnd 1, i.e. 2, so the connection grew one segment per round trip afterwards.
  3. The duplicate-ACK check no longer overwrites RTO recovery with Disorder, which defeated change 2.
- **Upload limit** (`netstack/pacer.go`, `limit.go`): optional limit on outgoing packets, off by default. `netstack/` is a copy of `golang.zx2c4.com/wireguard/tun/netstack` (MIT, `netstack/LICENSE-wireguard-go`) with the limit added.
- `cmd/wireproxy/main.go`: options below; loads system CA certificates before the Linux sandbox when measuring.
- gVisor updated to 2026-11-19.

## Results (2026-11-19, Proton JP#208 and JP#59 from Cambodia, 200 MB upload with ultra, `ResolveStrategy = ipv4`)

| | JP#208 | JP#59 |
|---|---|---|
| Proton CLI (Linux kernel TCP) | 18–21 s | 22–23 s |
| stock wireproxy | mostly 80–118 s, stalls | mostly >140 s |
| wirefork (recovery fixes, no limit) | 25–30 s | 24–32 s |

No stalls with wirefork in the final runs. The remaining gap is mostly the end of each upload: a connection that hits an RTO at the end of a recovery finishes a few seconds after the others.

**Use `ResolveStrategy = ipv4`** (`[Resolve]` section) for MEGA through Proton: new IPv6 connections to MEGA's API sometimes take many seconds to complete.

## Upload limit: when it helps

Without the recovery fixes, a limit near the line's capacity prevented stalls on JP#208 (19–25 s) but not on JP#59. With the fixes it made no clear difference on either server (JP#59 with a fixed 14 MB/s: 26–33 s, without: 27–30 s), and the automatic measurement read far too low on JP#59 (1–2 MB/s; the test uploads barely moved). So it is off by default and kept for a manual setting.

## Options

    --upload-limit off       no limit (default)
    --upload-limit auto      measure at start, then adjust
    --upload-limit auto:N    start at N MB/s without measuring, then adjust
    --upload-limit N         fixed N MB/s
    --probe-url URL          upload target for the measurement (default https://speed.cloudflare.com/__up)

**Measurement** (auto without N, ≤ 6 s, about 40–60 MB): 6 connections through the tunnel send 2 MB uploads one after another, while the limit rises from 2 MB/s by 25% every 200 ms. It stops raising at a burst of resends, when the limit is 1.5× what arrives, or when the senders stop filling it. The result is the highest rate the server confirmed receiving over 2 s.

**Adjustment** (once per second): a burst loss (more than max(500, 5% of packets sent) resends) lowers the limit 20%. While the limit holds speed down and there was no loss for 5 s, it rises 3% per second (0.5% near the last rate that caused a loss).

**Log lines** (stderr), for the caller to keep the last value:

    wirefork: measurement: line full at 14.9 MB/s; 44 MB confirmed in 6.0 s; best 2 s: 12.6 MB/s
    wirefork: upload limit 12.6 MB/s (measured 12.6 MB/s)
    wirefork: upload limit 15.7 MB/s (burst loss: 2952 resends)
    wirefork: upload limit 14.2 MB/s (raised)
