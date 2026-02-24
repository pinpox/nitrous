# nitrous

A terminal chat client for [Nostr](https://nostr.com), built with
[Bubbletea](https://github.com/charmbracelet/bubbletea). Supports NIP-28
public channels, NIP-29 relay-based groups, and NIP-17 encrypted direct
messages.

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

## Setup

Set your private key as an environment variable:

```sh
export NOSTR_PRIVATE_KEY="nsec1..."
```

Then run:

```sh
nitrous
```

To quickly test with a throwaway identity and the example config:

```sh
NOSTR_PRIVATE_KEY=$(openssl rand -hex 32) go run . -config ./config.example.toml
```

A default config will be created at `~/.config/nitrous/config.toml` on first
run if it doesn't exist.

## CLI flags

| Flag | Description |
|------|-------------|
| `-config <path>` | Path to config file (default: `~/.config/nitrous/config.toml`) |
| `-debug` | Enable debug logging to `debug.log` in the current directory |

The config path can also be set via the `NITROUS_CONFIG` environment variable.

## Config

The config file is TOML. See `config.example.toml` for a full example.

| Field | Description |
|-------|-------------|
| `relays` | List of relay WebSocket URLs |
| `max_messages` | Max messages kept in memory per conversation (default: 500) |
| `profile.name` | Your username/handle |
| `profile.display_name` | Your human-readable display name |
| `profile.about` | Profile bio |
| `profile.picture` | URL to profile picture |

## Data files

All data files live alongside the config file (default: `~/.config/nitrous/`).

| File | Format | Description |
|------|--------|-------------|
| `config.toml` | TOML | Main configuration |
| `rooms` | `name event_id` per line | Saved channels |
| `groups` | `name relay_url group_id` per line | Saved NIP-29 groups |
| `contacts` | `name hex_pubkey` per line | Saved DM contacts |

## Key bindings

| Key | Action |
|-----|--------|
| `Enter` | Send message |
| `Ctrl+Up` | Previous channel/group/DM |
| `Ctrl+Down` | Next channel/group/DM |
| `PgUp` | Scroll up |
| `PgDn` | Scroll down |
| `Ctrl+C` | Quit |

## Commands

| Command | Description |
|---------|-------------|
| `/create #name` | Create a new channel |
| `/join #name` | Join a channel from your rooms file |
| `/join <event-id>` | Join a channel by event ID |
| `/join naddr1...` | Join a NIP-29 relay-based group |
| `/join host'groupid` | Join a NIP-29 group by address |
| `/dm <npub>` | Open a DM conversation |
| `/leave` | Leave the current channel, group, or DM |
| `/me` | Show QR code of your npub |
| `/room` | Show QR code of the current channel or group |
| `/help` | Show command help |
