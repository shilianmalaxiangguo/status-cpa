# CPA Network Status

`status-cpa` is a small, self-contained status service for a Cloudflare Tunnel
connector. It reports cloudflared's configured transport mode and active
protocol while independently checking both HTTP/2 and QUIC paths to the edge.

The UI opens directly on HTTP/2 and QUIC health, followed by upstream model
health for AI INPUT, PIPIO, and KRILL, in that order. AI INPUT shows the authenticated
monitor channels `CodeX 余额-3`, `CodeX 余额-2`, and `CodeX 余额-1` independently.
PIPIO and KRILL each have rows for `gpt-6-astra`, `gpt-5.6-sol`, and `gpt-5.6-terra`. Each row shows its current state,
available latency signal, and one-minute history for the latest 60 minutes. Hover,
tap, or use the keyboard on a minute to inspect its status and probe detail. The
page defaults to a dark theme and retains an explicit light/dark choice. Service
paths and recent incidents follow below. It does not require Node.js, Python, a
database server, or a package manager on the target host.

## What it measures

- Production connector health from the local `cloudflared` Prometheus endpoint.
- Configured transport mode and active connector protocol from cloudflared's
  local diagnostic endpoints.
- HTTP/2 path health using TCP + TLS to Cloudflare Tunnel port `7844`.
- QUIC path health using a real TLS/QUIC handshake with Cloudflare's dedicated
  probe SNI (`probe.cftunnel.com`) and ALPN (`argotunnel`). The connection is
  closed immediately; no stream is opened and no Tunnel connector is registered.
- Local CPA (`8317`) and CPA Manager Plus (`18317`) endpoints.
- Public API and panel routes without following redirects.
- AI INPUT channel status from `https://ai.input.im/monitor`, using authenticated
  `/api/v1/channel-monitors` data for channel IDs 3, 2, and 1. These channels
  currently probe `gpt-5.6-sol`; their results are never copied to other models.
  `operational` maps to healthy, `degraded` to degraded, and `error` to critical.
  Latency is the channel's conversation latency, not endpoint PING.
  The current status must match the latest timeline record and be at most three
  minutes old. Missing channels, mismatched models, invalid data or unavailable
  authentication produce unknown. There is no fallback to the public status API.
- Exact `gpt-6-astra`, `gpt-5.6-sol`, and `gpt-5.6-terra` status published by
  PIPIO and KRILL. KRILL uses `https://www.krill-code.com/status`.
  Missing, stale, ambiguous, or unreadable model data is reported as unknown.
  PIPIO does not publish latency or probe timestamps; its row uses the reported
  current state. KRILL latency is the source's 24-hour TTFT P99.
- All model timelines use our own one-minute observations for the latest 60 minutes,
  not the source website's historical buckets. PIPIO/KRILL history IDs are
  preserved. AI INPUT channels use new IDs so old public model history is not
  mixed with account-specific channel history. Retired providers and INPUT model IDs
  are excluded from the API and incidents, without deleting stored history.

The QUIC check follows the probe behavior added to cloudflared 2026.7.x. A UDP
socket or `nc -u` alone is not treated as success.

## Local development

```bash
go test ./...
go run ./cmd/status-cpa \
  -listen 127.0.0.1:19090 \
  -data ./data/history.jsonl \
  -interval 30s
```

Open <http://127.0.0.1:19090>.

To exercise deterministic UI states without touching the collector:

```text
http://127.0.0.1:19090/?demo=healthy
http://127.0.0.1:19090/?demo=degraded
http://127.0.0.1:19090/?demo=critical
```

## Build for openEuler 22.03 SP3 x86_64

```bash
mkdir -p bin
VERSION="${VERSION:?set VERSION, for example v0.4.0}"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o bin/status-cpa-linux-amd64 ./cmd/status-cpa
strings bin/status-cpa-linux-amd64 | grep -F -- "${VERSION}"
sha256sum bin/status-cpa-linux-amd64
```

The binary is statically linked and does not require `dnf`, `rpm`, Go, Node.js,
or Python on the server.

## Target layout

```text
/opt/status-cpa/status-cpa
/var/lib/status-cpa/history.jsonl
/etc/systemd/system/status-cpa.service
```

See [`deploy/status-cpa.service`](deploy/status-cpa.service). The default unit
listens on port `19090` on all server interfaces. This keeps a LAN fallback at
`http://192.168.31.5:19090` when the Tunnel itself is unavailable, while the
existing Cloudflare Tunnel publishes the same service as:

```text
status.longxiachaogu.com -> http://127.0.0.1:19090
```

Do not add interactive Cloudflare Access to this public status page if it must
remain visible during incident diagnosis. The page exposes no credentials,
request bodies, API keys, or management actions.

## Environment / flags

General settings have flags and matching environment variables:

| Flag | Environment | Default |
| --- | --- | --- |
| `-listen` | `STATUS_CPA_LISTEN` | `127.0.0.1:19090` |
| `-data` | `STATUS_CPA_DATA` | `./data/history.jsonl` |
| `-interval` | `STATUS_CPA_INTERVAL` | `60s` |
| `-retention` | `STATUS_CPA_RETENTION` | `168h` |
| `-metrics-url` | `STATUS_CPA_METRICS_URL` | `http://127.0.0.1:20241/metrics` |

AI INPUT credentials are environment-only: `STATUS_CPA_AI_INPUT_EMAIL` and
`STATUS_CPA_AI_INPUT_PASSWORD`. Do not commit their values or put them in command-line flags.
For the supplied systemd unit, put them in `/etc/status-cpa/ai-input.env`, owned
by root with mode `0600`:

```text
STATUS_CPA_AI_INPUT_EMAIL=your-login-email
STATUS_CPA_AI_INPUT_PASSWORD=your-login-password
```

The unit reads this file at startup. After installing the updated unit, run
`systemctl daemon-reload` and restart `status-cpa` to load it.
For local development, export the same variables in the shell before running Go.
Passwords are used only to log in; the access token stays in memory and is reused
until expiry. HTTP 401 triggers at most one re-login per collection. All three
INPUT checks share one channel response (or failure) per collection, avoiding
three simultaneous logins. No credentials or raw account responses enter the
public status API, JSONL history, or logs.

## Health semantics

- **Healthy**: the check passed.
- **Degraded**: production remains available, but an optional path or one of
  multiple connector signals is unhealthy.
- **Critical**: the production connector or both public service paths fail.
- **Unknown**: the check has not completed or its source cannot be read.

The global state treats an independent edge-path failure as degraded while the
production connector remains online. That distinction prevents the status page
from calling the whole API down merely because an unused fallback path is
unavailable. External model-provider rows are tracked independently and do not
change the Tunnel's global network state.
