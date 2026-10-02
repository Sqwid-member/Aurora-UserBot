# Aurora 🌌

> **Language / Мова / Язык:** **English** • [Українська](README.uk.md) • [Русский](README.ru.md)

A modular Telegram **userbot** written in Go. A single static binary that runs
anywhere Go runs — including **directly in Termux on your Android phone**.
Plugins can be written in any programming language. Controlled via a local web panel.

```
┌──────────────────────────────────────────────────────────────┐
│  aurora (single binary, ~12 MB, CGO disabled)                │
│                                                              │
│   ┌──────────┐  JSON-RPC  ┌──────────────┐  stdio  ┌───────┐ │
│   │   MTProto│──(stdio)──▶│plugin-manager│◀────────│ plugin│ │
│   │  (gotd)  │◀──────────│  + limits     │────────▶│  .go  │ │
│   └──────────┘   events   └──────────────┘         │  .py  │ │
│        │                                             │  .lua │ │
│        │                                             │  .mjs │ │
│   ┌────▼─────┐   HTTP/SSE   ┌────────────────────┐  └───────┘ │
│   │   panel  │◀─────────────│ 127.0.0.1:8420     │            │
│   └──────────┘              └────────────────────┘            │
└──────────────────────────────────────────────────────────────┘
```

---

## Key Features

| Requirement | Implementation |
|---|---|
| **Maximum Performance** | Go, `CGO_ENABLED=0`, no heavyweight frameworks. Parsing updates performs no network roundtrips — only name caches and in-memory hash maps. Event queues are bounded; delivery is non-blocking. |
| **Minimal RAM Footprint** | `debug.SetMemoryLimit` defaults to 96 MB, minimal goroutine count, ring-buffered logging, `kv` is in-memory JSON, web panel is a single self-contained HTML page without CDN dependencies. `RLIMIT_DATA` per plugin (not `RLIMIT_AS` — which kills Go/Python/Node on startup; detailed in [docs/PROTOCOL.md](docs/PROTOCOL.md)). |
| **True Modularity** | The core has no hardcoded knowledge of specific plugins. A plugin is an ordinary executable communicating over JSON-RPC, equipped with a manifest, capabilities/permissions, resource limits, and lifecycle events. |
| **Multi-Language Plugins** | Zero-dependency Go SDK, Python, Node.js, Lua — examples are included in the repository. The protocol is newline-delimited JSON, compatible with virtually any language. |
| **Termux Optimization** | Installs in seconds from a prebuilt binary into `$PREFIX/bin`, background service `aurora start` with `termux-wake-lock`, interactive `aurora setup`, and automatic web dashboard launcher. |
| **Simple Installation** | Single curl command or `make install`. Zero external databases, dependencies, or system daemons required. |
| **Local Web Dashboard** | Embedded SPA: real-time status, plugin store and manager, multi-account authentication, terminal chat, log viewer, settings. Token-based authentication, listening exclusively on loopback. |

---

## Installation on Termux

```console
$ pkg install curl
$ curl -fsSL https://raw.githubusercontent.com/Sqwid-member/Aurora-UserBot/main/scripts/install.sh | bash
```

The installer script:
1. Detects your hardware platform (`android/arm64`, `linux/armv7`, `linux/amd64`, `darwin/*`).
2. Checks GitHub Releases for the latest release and **downloads the prebuilt binary** — no need to install Go or compile on your phone.
3. Places the binary in `$PREFIX/bin/aurora` and sets up `a` command aliases.
4. Generates a secure token, sets up default plugins (`echo`, `pulse`, `autoaway`, `hello`), and starts the service.

### Linux / macOS

```console
$ curl -fsSL https://raw.githubusercontent.com/Sqwid-member/Aurora-UserBot/main/scripts/install.sh | bash
```

Installs to `~/bin/aurora`. Add `~/bin` to your `PATH` if it is not already present.

### Prebuilt Binaries

You can also download standalone binaries directly from [GitHub Releases](https://github.com/Sqwid-member/Aurora-UserBot/releases/latest):

* `aurora-arm64` — Termux / Android aarch64 & Linux ARM64
* `aurora-amd64` — Linux x86_64
* `aurora-linux-arm.tar.gz` — Termux 32-bit ARM (armv7)
* `aurora-darwin-arm64` — Apple Silicon (M1/M2/M3/M4)
* `aurora-darwin-amd64` — Intel macOS

---

## First Run and Authentication

Launch the terminal control center:

```console
$ aurora
# or using the shortcut:
$ a
```

### Authentication Methods:

1. **One-tap QR on the same phone** (Recommended for Termux):
   * Select `Вхід по QR` (QR Login).
   * Tap `Відкрити в Telegram` (Open in Telegram) on the same phone.
   * Official Telegram will prompt you to confirm the login. No phone number or SMS required.
2. **Phone Number + Login Code**:
   * Select `Вхід у Telegram` (Phone Login).
   * Enter your international phone number (`+380...`).
   * Enter the code received in your Telegram application or via SMS, followed by your 2FA password (if enabled).
3. **Session String Import**:
   * Import existing Telethon / Pyrogram / WDesktop session strings directly in the Web Dashboard.

### Background Service Management

```console
$ a start    # Start background daemon
$ a stop     # Stop background daemon
$ a restart  # Restart daemon
$ a status   # Check daemon & Telegram status
$ a logs -f  # Follow live logs
$ a update   # Seamlessly update to the latest release
```

---

## Web Dashboard

The local web control center is accessible at:
```
http://127.0.0.1:8420/?token=<your-token>
```
To open it automatically in your default browser:
```console
$ a web
```

---

## Plugins

### Structure

Each plugin lives in its own subdirectory inside `~/.local/share/aurora/plugins/<name>/` and contains an `aurora.plugin.json` manifest:

```json
{
  "name": "echo",
  "version": "1.0.0",
  "description": "Echo bot replying to /echo",
  "language": "python",
  "runtime": {
    "command": "python3",
    "args": ["echo.py"]
  },
  "events": ["message.new"],
  "permissions": {
    "tg": ["send"]
  },
  "limits": {
    "memory_mb": 64,
    "idle_timeout_sec": 900
  }
}
```

### Installation

Install plugins directly from Git repositories:
```console
$ aurora plugin install https://github.com/user/aurora-plugin-name
```
Or manage them visually via the Web Dashboard.

---

## CLI Reference

```console
$ aurora run                          # core: Telegram + plugins + panel
$ aurora login                        # interactive login (phone → code → 2FA)
$ aurora login qr                     # one-tap login on the same phone
$ aurora login web                    # open the web panel for login
$ aurora panel                        # print the panel URL with token
$ aurora send [--silent] [--no-preview] @user "hi"
$ aurora commands                     # list plugin commands
$ aurora command <name> [text]        # run a plugin command
$ aurora accounts                     # list Telegram accounts
$ aurora accounts switch <id>         # switch the active account
$ aurora sessions                     # list active Telegram sessions
$ aurora sessions kill <hash>         # terminate a session
$ aurora profile                      # show profile (name, username, bio)
$ aurora gc                           # garbage-collect the core
$ aurora plugins                      # list plugins
$ aurora plugin start|stop|restart <name>
$ aurora plugin install <git-url> [name]
$ aurora plugin remove <name>
$ aurora plugin settings <name> [k=v ...]   # empty value resets key to default
$ aurora plugin settings <name> reset       # reset all settings to defaults
$ aurora session                      # local session info
$ aurora session export               # StringSession (Telethon/Pyrogram)
$ aurora session import "1BVts…"      # import without SMS
$ aurora session import-web <JSON|@file> [--dc N]  # Telegram Web export
$ aurora backup [file]                # config+sessions bundle for moving
$ aurora restore <file>               # restore (core must be stopped)
$ aurora device [--save]              # device fingerprint snapshot
$ aurora config                       # config (secrets masked) + validation
$ aurora doctor                       # environment check
$ aurora status                       # live daemon/auth/RAM status
$ aurora logs                         # live log tail
$ aurora update                       # self-update to the latest release
$ aurora logout                       # close the Telegram session
```

---

## Documentation

* [Plugin API Guide](docs/PLUGIN_API.md) — How to write plugins in Python, Go, Node.js, and Lua.
* [Protocol Specification](docs/PROTOCOL.md) — Complete JSON-RPC 2.0 protocol specifications and limits.

---

## License

This project is licensed under the [MIT License](LICENSE).
