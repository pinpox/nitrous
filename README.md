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


| Command              | Description                                  |
|----------------------|----------------------------------------------|
| `/create #name`      | Create a new channel                         |
| `/join #name`        | Join a channel from your rooms file          |
| `/join <event-id>`   | Join a channel by event ID                   |
| `/join naddr1...`    | Join a NIP-29 relay-based group              |
| `/join host'groupid` | Join a NIP-29 group by address               |
| `/dm <npub\|user@domain>` | Open a DM conversation (supports NIP-05) |
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
| NIP-05 | DNS-based internet identifiers (user lookup) |
| NIP-65 | Relay List Metadata |
