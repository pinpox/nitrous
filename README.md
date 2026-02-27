# nitrous

A terminal chat client for [Nostr](https://nostr.com), built with
[Bubbletea](https://github.com/charmbracelet/bubbletea). Supports NIP-28
public channels, NIP-29 relay-based groups, and NIP-17 encrypted direct
messages.

## Setup

On first run, nitrous generates a keypair and config at
`~/.config/nitrous/config.toml` automatically, then starts the TUI.

To generate a new keypair into the configured `private_key_file`:

```sh
nitrous keygen
```

You can also set a key via the `NOSTR_PRIVATE_KEY` environment variable
(falls back to this if `private_key_file` is not set).

## CLI flags

| Flag             | Description                                                    |
|------------------|----------------------------------------------------------------|
| `-config <path>` | Path to config file (default: `~/.config/nitrous/config.toml`) |
| `-debug`         | Enable debug logging to `debug.log` in the current directory   |

The config path can also be set via the `NITROUS_CONFIG` environment variable.
See ./config.example.toml for example documentation.

## Keybinds and commands

| Key         | Action                    |
|-------------|---------------------------|
| `Enter`     | Send message              |
| `Ctrl+Up`   | Previous channel/group/DM |
| `Ctrl+Down` | Next channel/group/DM     |
| `PgUp`      | Scroll up                 |
| `PgDn`      | Scroll down               |
| `Ctrl+C`    | Quit                      |


| Command                        | Description                                  |
|--------------------------------|----------------------------------------------|
| `/channel create #name`        | Create a new NIP-28 channel                  |
| `/join #name`                  | Join a channel from your rooms file          |
| `/join <event-id>`             | Join a channel by event ID                   |
| `/join naddr1...`              | Join a NIP-29 relay-based group              |
| `/join host'groupid`           | Join a NIP-29 group by address               |
| `/group create <name> [relay]` | Create a NIP-29 group                        |
| `/group name <new-name>`       | Rename the current group                     |
| `/group about <text>`          | Set group description                        |
| `/group picture <url>`         | Set group picture                            |
| `/group set open\|closed`      | Set group open/closed                        |
| `/group user add <pubkey>`     | Add a user to the current group              |
| `/dm <npub\|hex\|user@domain>` | Open a DM conversation (supports NIP-05)     |
| `/delete`                      | Delete your last message in a group          |
| `/leave`                       | Leave the current channel, group, or DM      |
| `/me`                          | Show QR code of your npub                    |
| `/room`                        | Show QR code of the current channel or group |
| `/help`                        | Show command help                            |

## Supported NIPs

| NIP | Description |
|-----|-------------|
| NIP-01 | Profile metadata (kind 0) |
| NIP-17 | Private Direct Messages (gift wrap) |
| NIP-19 | bech32 entities (npub, nsec, nevent, naddr) |
| NIP-28 | Public Channels (kind 40/42) |
| NIP-29 | Relay-based Groups (kind 9, join/leave) |
| NIP-42 | Client authentication |
| NIP-44 | Versioned encryption |
| NIP-59 | Gift Wrap |
| NIP-05 | DNS-based internet identifiers (user lookup) |
| NIP-51 | Lists (contacts, public chats, simple groups) |
| NIP-65 | Relay List Metadata |

## Message Logging

Nitrous logs all chat messages to plain-text files at
`~/.config/nitrous/logs/` (one file per room). Format:

```
2024-01-15 10:30:45	e48be560	d9656344	alice	hey everyone
```

Fields: `timestamp \t eventID \t pubkey \t displayname \t content`

Logs are human-readable — use `grep`, `less`, or `cat` to browse them.
On startup, recent history is loaded from log files so messages persist
across restarts.

Disable with `logging = false` in `config.toml`. Customize the directory
with `log_dir`.

## Testing

```sh
# Unit tests only (fast)
go test -short ./...

# All tests including integration (~55s)
go test -timeout 180s ./...

# Integration test with verbose output
go test -v -run TestIntegration -timeout 180s
```

The integration test starts an embedded khatru29 NIP-29 relay in-process
(no external services needed) and uses
[teatest](https://github.com/charmbracelet/x/exp/teatest) to drive two
TUI clients (alice & bob) through channels, groups, DMs, and commands.
