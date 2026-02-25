# nitrous

A terminal chat client for [Nostr](https://nostr.com), built with
[Bubbletea](https://github.com/charmbracelet/bubbletea). Supports NIP-28
public channels, NIP-29 relay-based groups, and NIP-17 encrypted direct
messages.

## Setup

Set your private key as an environment variable:

```sh
export NOSTR_PRIVATE_KEY="nsec1..."

# Or for debugging throw-away key
NOSTR_PRIVATE_KEY=$(openssl rand -hex 32) go run . -config ./config.example.toml
```

A default config will be created at `~/.config/nitrous/config.toml` on first
run if it doesn't exist.

## CLI flags

| Flag             | Description                                                    |
|------------------|----------------------------------------------------------------|
| `-config <path>` | Path to config file (default: `~/.config/nitrous/config.toml`) |
| `-debug`         | Enable debug logging to `debug.log` in the current directory   |

The config path can also be set via the `NITROUS_CONFIG` environment variable.
See ./config.example.toml for an example documentation.

## Keybinds and commands

| Key         | Action                    |
|-------------|---------------------------|
| `Enter`     | Send message              |
| `Ctrl+Up`   | Previous channel/group/DM |
| `Ctrl+Down` | Next channel/group/DM     |
| `PgUp`      | Scroll up                 |
| `PgDn`      | Scroll down               |
| `Ctrl+C`    | Quit                      |


| Command              | Description                                  |
|----------------------|----------------------------------------------|
| `/create #name`      | Create a new channel                         |
| `/join #name`        | Join a channel from your rooms file          |
| `/join <event-id>`   | Join a channel by event ID                   |
| `/join naddr1...`    | Join a NIP-29 relay-based group              |
| `/join host'groupid` | Join a NIP-29 group by address               |
| `/dm <npub>`         | Open a DM conversation                       |
| `/leave`             | Leave the current channel, group, or DM      |
| `/me`                | Show QR code of your npub                    |
| `/room`              | Show QR code of the current channel or group |
| `/help`              | Show command help                            |

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
| NIP-65 | Relay List Metadata |
