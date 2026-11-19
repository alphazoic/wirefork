# wirefork

wireproxy (github.com/windtf/wireproxy, ISC) with an upload limit, for M3.

## Why

wireproxy's TCP (gVisor) sends a whole congestion window at once. On a long path (Cambodia → Proton JP#208 → MEGA) one connection sent 2.7 MB in 80 ms (~34 MB/s on a ~14 MB/s line). The whole burst was lost, and gVisor then resent it one packet per round trip (~6 KB/s) for a minute or more. 200 MB uploads took 80–118 s instead of the Proton CLI's 18–23 s. A newer gVisor (2026-11-19) didn't change this. Measurements: M3 repo `docs/for-user/wireguard-measurements.md`.

Spacing outgoing packets at about the line's upload capacity prevents the bursts: 200 MB in 18–27 s with a limit of 13–16 MB/s on that line.

## Changes from wireproxy

- `netstack/`: copy of `golang.zx2c4.com/wireguard/tun/netstack` (MIT, `netstack/LICENSE-wireguard-go`) with a packet-rate limit (`pacer.go`) on outgoing packets. Senders wait; nothing is dropped.
- `limit.go`: chooses and adjusts the limit.
- `cmd/wireproxy/main.go`: options below; loads system CA certificates before the Linux sandbox when measuring.
- gVisor updated to 2026-11-19.

## Limits of this fix (tested 2026-11-19)

On Proton JP#208 the automatic limit gave 200 MB uploads in 19–25 s (Proton CLI 18–23 s). On JP#59 the same day, uploads still stalled (>140 s in 3 of 5 runs with `auto:14`, 2 of 3 with a fixed 8 MB/s) while the Proton CLI took 20–28 s. There, losses happen even at moderate rates, and gVisor's slow recovery after any larger loss still causes the stall. The limit prevents losses caused by bursts. It doesn't fix the recovery.

## Options

    --upload-limit auto      measure at start, then adjust (default)
    --upload-limit auto:N    start at N MB/s without measuring, then adjust
    --upload-limit N         fixed N MB/s
    --upload-limit off       no limit (stock wireproxy)
    --probe-url URL          upload target for the measurement (default https://speed.cloudflare.com/__up)

**Measurement** (auto without N, ≤ 6 s, about 40–60 MB): 6 connections through the tunnel send 2 MB uploads one after another, while the limit rises from 2 MB/s by 25% every 200 ms. It stops raising at a burst of resends, when the limit is 1.5× what arrives, or when the senders stop filling it. The result is the highest rate the server confirmed receiving over 2 s.

**Adjustment** (once per second): a burst loss (more than max(500, 5% of packets sent) resends) lowers the limit 20%. While the limit holds speed down and there was no loss for 5 s, it rises 3% per second (0.5% near the last rate that caused a loss).

**Log lines** (stderr), for the caller to keep the last value:

    wirefork: measurement: line full at 14.9 MB/s; 44 MB confirmed in 6.0 s; best 2 s: 12.6 MB/s
    wirefork: upload limit 12.6 MB/s (measured 12.6 MB/s)
    wirefork: upload limit 15.7 MB/s (burst loss: 2952 resends)
    wirefork: upload limit 14.2 MB/s (raised)
