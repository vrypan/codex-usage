# statusbar-codex

Show Codex quota, credits, reset countdowns, and token usage in
[statusbar](https://github.com/vrypan/statusbar).

The value of the pushed line looks like this; your theme displays its `codex`
name through `#(name)`:

```text
5h ━━━─────── 32% used ↻2h14m  7d 61% used ↻3d4h  credits 120  resets 2  tokens lifetime 7.7M
```

Example numbers only. Window durations come from Codex; an account may have
one window, two windows, or several quota buckets.

## Requirements

- macOS or Linux; Go 1.23+ to build. The resulting binary has no runtime dependencies besides the CLIs below.
- Codex CLI with `app-server proxy`, a running local daemon, and an existing login.
  Developed against Codex CLI/daemon 0.160.0. The app-server interface is experimental.
- statusbar with `push --fifo`, `set`, `list --short --json`, and `pop` for watch mode.
- Account usage depends on Codex service support. API-key-only authentication may not expose ChatGPT quota or token activity.

## Build and try

```sh
go build -trimpath -o bin/statusbar-codex .
./bin/statusbar-codex snapshot
./bin/statusbar-codex snapshot --json
```

If Codex is not already running, launch Codex normally or explicitly start its
daemon with `codex app-server daemon start`. The helper connects to an existing
daemon; it does not start one, log in, or change Codex configuration.

Inside a statusbar session:

```sh
/path/to/statusbar-codex/bin/statusbar-codex watch &
helper_pid=$!

# Stop the helper and remove its line:
kill "$helper_pid"
```

The helper creates a pushed line named `codex`. It looks up and keeps the line's
numeric ID for updates and cleanup, so it cannot remove a later line that
reuses the name. The FIFO path confirms the name without relying on terminal
stdin; the FIFO is removed with the line. The line uses status `normal`, so the helper
itself does not animate as a running job. Your theme's `[push]` template still
applies. `--keep` leaves the last displayed line after shutdown. Removing the
line manually causes the helper to exit rather than recreate it.

`snapshot` and `watch --stdout` include a leading `codex` label because they
have no pushed line name to display it.

`watch --stdout` emits a new line only when its rendered content changes. This
mode works outside statusbar and creates no statusbar lines.

### Follow a particular thread

```sh
./bin/statusbar-codex snapshot --thread THREAD_ID --json
./bin/statusbar-codex watch --thread THREAD_ID
```

Use the explicit Codex thread ID. The helper never guesses the active thread or
resumes a conversation. It calls `account/usage/read` with `threadId` and labels
the returned token and credit totals **estimated**. This endpoint depends on the
server's support and the thread's billing route; unavailable data is shown as
`—`. Thread mode is tested with protocol fixtures. Live account readings were
verified; the live thread endpoint returned no estimate for the test thread.
An eligible thread/account is required to receive estimates.

Thread totals sum the reported per-group `totalTokens` only when every group
has that field. Cached input and output components are available in JSON; they
are not added again to the total. These figures are not context-window usage.

### Useful options

```sh
./bin/statusbar-codex watch --interval 60s --usage-interval 5m
./bin/statusbar-codex watch --color never --bars=false --width 100
./bin/statusbar-codex snapshot --bucket codex --json
./bin/statusbar-codex snapshot --socket /absolute/path/to/app-server.sock
./bin/statusbar-codex watch --codex /path/to/codex --statusbar /path/to/statusbar
./bin/statusbar-codex watch --help
```

Default intervals: quota and thread usage every 60 seconds, account token
activity every five minutes. Intervals must be at least five seconds. Each RPC
has a ten-second timeout, configurable with `--timeout`.

Text selects the `codex` quota bucket if present, otherwise the first bucket in
sorted order. `--bucket` selects another. JSON includes every returned bucket.
`--width` removes meters before clipping to an explicit width; without it,
statusbar handles clipping. Labels/metrics normally use single-cell Unicode;
font choices and unusual bucket names can affect cell width. Increase width
or disable meters to see more fields.

## Meaning of the readings

| Label | Meaning |
| --- | --- |
| `5h`, `7d`, etc. | Server-reported quota window duration |
| `% used` | Usage of that quota window, not a percentage of token totals |
| `↻` | Time until the server's reported reset timestamp |
| `credits` | Reported balance, `unlimited`, `available` (balance unknown), or `—` (unavailable) |
| `resets` | Available earned resets; independent of spendable credits |
| `tokens lifetime` | Account-wide cumulative activity reported by Codex |
| `thread est. tokens` | Estimated token activity for the selected thread |
| `est. credits` | Estimated thread usage in credits, decoded from microcredits |

Credit balances are displayed as supplied, without conversion to currency.
Zero is shown only when the balance or credit flags establish it. Optional
fields remain unavailable instead of becoming zero. Token activity may lag.
Daily account buckets and timestamps are retained in JSON using the dates
supplied by the service; the helper does not assume a timezone for those dates.

Thin meters use the terminal palette: green under 70%, yellow at 70%, red at
90%. Percentages remain visible without color. The helper does not spend
credits, redeem resets, send messages, or start model requests.

## Reliability and ownership

- WebSocket handshake and masked frames travel through `codex app-server proxy`.
  The proxy forwards raw bytes; it is not a JSONL-to-WebSocket adapter.
- JSON-RPC begins with `initialize` and `initialized`.
- Sparse quota notifications merge into the last full snapshot. They do not
  renew the freshness timestamp for unrelated credit fields.
- Notifications request a full refresh with a minimum five-second spacing.
- Countdown text is recalculated locally; identical output is not republished.
- Failed refreshes retain the last values and mark them stale. Expired reset
  times say `refreshing`; usage is never assumed to have returned to zero.
- Reconnects back off from one to 30 seconds. Account changes and reconnects
  discard cached data before using the new connection.
- One helper per statusbar session is enforced by a file lock because each
  helper uses the `codex` line name.
  `--stdout` has no lock. Small empty lock files remain beside statusbar's state
  after shutdown to avoid inode races; they contain no credentials or usage.
- Ctrl-C and SIGTERM close the proxy and remove only the helper's own line.
  A force kill cannot clean up; remove the leftover line with `statusbar pop ID`.
- Unsupported usage endpoints are shown as unavailable while supported fields
  continue updating. JSON includes endpoint errors and independent timestamps.

## Development

```sh
go test -race ./...
go vet ./...
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/statusbar-codex-linux .
```

No third-party Go packages. Tests cover WebSocket framing and ping/pong,
RPC response routing and timeouts, sparse updates, account changes, missing
fields, estimated thread totals, ANSI formatting, reconnects, duplicate-helper
locks, and statusbar line ownership/cleanup.

`rpc.go` and `websocket.go` handle transport, `model.go` holds snapshots,
`render.go` formats text, `sink.go` owns the statusbar line, and `main.go`
coordinates polling and reconnects.

Protocol reference: [Codex app-server](https://learn.chatgpt.com/docs/app-server).
The local schema can be regenerated for compatibility checks with:

```sh
codex app-server generate-ts --out /tmp/codex-protocol
```
