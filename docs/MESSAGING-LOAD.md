# Constrained-host messaging transport check

`TestMessagingTransportLoad` is an opt-in running-HTTP/WebSocket acceptance check.
It uses disposable SQLite storage and synthetic suite accounts/sessions. Enrollment,
key-possession checks, approval, invitations, room acceptance, cookie/CSRF requests,
live device authentication and cursor fetches run through the real server.

## Workload and measurement

- One room with 50 accounts, two approved devices each, and 100 live WebSockets.
- 60 seconds at 10 messages/second, then two seconds at 50 messages/second.
- 700 application events with 1,024-byte opaque payloads; every device verifies
  ordered, complete fetches and exact payload bytes (70,000 deliveries).
- Each account owns a serial sender and its observations. A paced dispatcher
  supplies independent senders; it does not wait for every global acknowledgement.
  Each receiver owns its cursor and observations; results merge after workers finish.
- Receivers follow wakeups with `/api/auth/me` and durable HTTP cursor reads,
  including pagination. No database/query shortcuts, warm-result cache, or forced
  limiter reset occurs during measurement.
- Both acceptance and receipt latency start at the scheduled send time, including
  scheduling backlog. Concurrent append order is matched using server receipts.
  p95 is reported separately for sustained traffic and bursts. Both must meet
  acceptance <250 ms and receipt <1 second. Fail on any HTTP/socket error, duplicate
  or missing sequence, payload mismatch, incomplete workload or exceeded target.

This measures **transport of synthetic opaque bytes**. It does not generate MLS
messages, measure browser encryption/decryption, exercise TLS/proxy/real-network
latency, validate a deployed issuer, or establish long-duration capacity. Database
and test clients share the same constrained container, including its CPU quota.
The short burst and one-minute sustained run are recorded precisely; they are not
a soak test or a universal 50-person production guarantee.

## Reproduction

Build from the repository root, then run only the opt-in test in an owned container:

```sh
CGO_ENABLED=0 go test -c -o /tmp/kymessages-load.test ./internal/api
docker run --rm --network none --cpus=2 --memory=2g --memory-swap=2g \
  -e KY_MESSAGING_LOAD=1 \
  --mount type=bind,src=/tmp/kymessages-load.test,dst=/load.test,readonly \
  mcr.microsoft.com/playwright@sha256:eff16c30e6f3f4af0a03fa4b706120d5e9b0891c344a27d64559aff5900a4a27 \
  /load.test -test.run '^TestMessagingTransportLoad$' -test.v -test.timeout 3m
```

This image is reused from browser verification; the test does not invoke browsers
or install dependencies. Nothing is published or exposed on host ports. SQLite
lives in the container's writable layer and leaves with it. The test skips in
ordinary suites unless explicitly enabled and rejects a PostgreSQL profile.

## Findings

The original combined 120-request/minute account limit returned HTTP 429 after
64 application messages (about seven seconds). Two receiving devices in a busy room
naturally exceed that shared budget. Reads now have their own 2,400-request/minute
account budget; writes retain 120/minute and enrollment retains 10/5 minutes.
This preserves write abuse protection without letting receipt traffic consume it.
A regression verifies both separation and continued write limiting.

An early aggregate-latency run passed, but a later phase-specific run with a global
serial sender exposed burst backlog (p95 acceptance 1.94 s / receipt 2.02 s). The
final driver uses independent account senders and reports each phase separately;
retain this distinction when comparing measurements.

## Recorded final run (2026-09-27)

Host: AMD Ryzen AI 9 365, Linux `7.2.6-1-cachyos`, Go
`go1.27.1-X:nodwarf5 linux/amd64`. Docker applies a **2-CPU quota, 2-GiB memory
limit and no extra swap**; this is a quota on the named host, not a dedicated VM or
an ARM homelab measurement. The database uses the disposable container layer.

| Phase | p95 scheduled-send to acceptance | p95 scheduled-send to HTTP receipt |
| --- | ---: | ---: |
| 60 seconds at 10/s | 67.24 ms | 220.91 ms |
| Two seconds at 50/s | 52.73 ms | 158.43 ms |

All 700 appends and 70,000 deliveries completed without gaps, corruption or HTTP
errors. The run passed both phase targets and exited successfully in 62.38 seconds.
It did not measure memory high-water use, run with the race detector, or measure
end-to-end foreground rendering. Those limits remain separate release evidence.
