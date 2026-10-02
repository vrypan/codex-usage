# codex-usage

Read Codex quota windows, reset times, credit balances, earned resets, and token activity from your local Codex daemon. Output is plain text, ANSI-colored text, or JSON. The tool does not start a model request or change account settings.

```text
codex  5h ━━━─────── 32% used ↻2h14m  7d 61% used ↻3d4h  credits 120  resets 2  tokens lifetime 7.7M
```

Example numbers only. The service decides which windows and quota buckets are available.

## Requirements

- macOS or Linux; Go 1.23+ to build. The binary uses only the Go standard library.
- Codex CLI with `app-server proxy`, a running local daemon, and an existing login. Developed against Codex CLI/daemon 0.160.0. The app-server interface is experimental.
- Some account usage fields require Codex service support. API-key-only authentication may not provide ChatGPT quota or token activity.

## Install

With Homebrew (builds from the tagged source):

```sh
brew install vrypan/tap/codex-usage
```

Or download a prebuilt binary (macOS arm64, Linux amd64/arm64) from the
[releases page](https://github.com/vrypan/codex-usage/releases), extract it, and put
`codex-usage` on your `PATH`. Verify the download against `checksums.txt`.

Or install with Go:

```sh
go install github.com/vrypan/codex-usage@latest
```

`codex-usage version` prints the version, commit, and build date.

## Build and run

```sh
make build          # optimized build with version info, written to bin/
./bin/codex-usage snapshot
./bin/codex-usage snapshot --json
./bin/codex-usage watch
```

`snapshot` writes one reading and exits. `watch` keeps the connection open, polls periodically, and prints a new line when the displayed reading changes. Its initial line may say `disconnected` while it connects. Stop it with Ctrl-C. Both modes work in any shell; neither requires statusbar.

If the Codex daemon is not running, start Codex normally or run `codex app-server daemon start`. This tool does not start a daemon or log in.

### One thread's estimated usage

```sh
./bin/codex-usage snapshot --thread THREAD_ID --json
./bin/codex-usage watch --thread THREAD_ID
```

Use an explicit thread ID. `account/usage/read` with `threadId` returns estimated thread token and credit usage when the thread's billing route is available. If every returned model group reports `totalTokens`, the tool sums those values; it does not add input and output components again. The live test account returned no estimate for the test thread, so unavailable values are shown as `—`. These figures are separate from context-window usage and account quota.

### Options

```sh
./bin/codex-usage watch --interval 60s --usage-interval 5m
./bin/codex-usage snapshot --color always --width 100
./bin/codex-usage snapshot --bucket codex --bars=false
./bin/codex-usage snapshot --socket /absolute/path/to/app-server.sock
./bin/codex-usage snapshot --codex /path/to/codex
./bin/codex-usage snapshot --help
```

By default, quota and thread usage refresh every minute in `watch`; account token activity refreshes every five minutes. Intervals must be at least five seconds. Each RPC call has a ten-second timeout (`--timeout`). `--color auto` adds ANSI colors on a terminal; use `--color always` for a consumer that supports ANSI. `--width` removes meters before clipping at a given width. Without a width, output is not clipped.

Text selects the `codex` quota bucket if present, otherwise the first bucket in sorted order. `--bucket` selects another. JSON includes every returned bucket, independent timestamps, and any endpoint errors. A missing field appears as `—`, not zero.

## Example: statusbar theme

A statusbar `[command]` can run the snapshot on its own schedule. No watcher or statusbar-specific integration is needed:

```ini
[line.codex]
text = " #(command:codex)#(fill: )"

[command.codex]
run = /absolute/path/to/codex-usage/bin/codex-usage snapshot --color always
interval = 60
```

`statusbar` displays the first output line. The snapshot already includes the `codex` label. Adjust the path and interval in your theme. Other prompt or status tools can also consume the single-line or JSON output.

## Reading the values

| Label | Meaning |
| --- | --- |
| `5h`, `7d`, etc. | Server-reported quota window duration |
| `% used` | Usage within that quota window; separate from token totals |
| `↻` | Time until the reported reset timestamp |
| `credits` | Reported balance, `unlimited`, `available` with unknown balance, or `—` |
| `resets` | Available earned resets, separate from spendable credits |
| `tokens lifetime` | Account cumulative token activity reported by Codex |
| `thread est. tokens` | Estimated activity for the selected thread |
| `est. credits` | Estimated thread credit usage decoded from microcredits |

Credit balances stay in their reported units. Daily account buckets and timestamps remain in JSON using the dates supplied by the service. The tool does not assume a timezone for those dates. Meters use the terminal palette: green below 70%, yellow from 70%, and red from 90%.

## Behavior

- Connects through `codex app-server proxy`. The proxy tunnels WebSocket bytes, so the tool handles the WebSocket handshake and masking before JSON-RPC initialization.
- Merges sparse quota notifications into the last full snapshot without making unrelated credit fields appear fresh. Requests a full refresh after notifications, with a minimum five-second spacing.
- Recalculates reset countdowns locally. An expired deadline reads `refreshing` until the server supplies another value.
- Keeps old values after a failed refresh and marks them stale. Reconnects back off from one to 30 seconds. Account changes and reconnects clear cached values.
- Never spends credits, redeems resets, sends messages, or starts inference.

## Development

```sh
go test -race ./...
go vet ./...
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/codex-usage-linux .
```

Tests cover WebSocket framing, JSON-RPC routing, timeouts, sparse updates, account switches, unavailable fields, estimates, formatting, and reconnects. No third-party Go packages are required.

Protocol reference: [Codex app-server](https://learn.chatgpt.com/docs/app-server). The local schema can be regenerated with `codex app-server generate-ts --out /tmp/codex-protocol`.

## License

MIT. See [LICENSE](LICENSE).
