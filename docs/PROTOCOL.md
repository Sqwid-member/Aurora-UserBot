> **Language / Мова / Язык:** **English** • [Українська](PROTOCOL.uk.md) • [Русский](PROTOCOL.ru.md)

# Aurora Plugin Protocol Specification

Protocol Version: **1**

An Aurora plugin is an independent executable. It communicates with the Aurora core
via newline-delimited JSON-RPC 2.0 over standard I/O (`stdin`/`stdout`).
No shared libraries, no dynamic linking (`dlopen`), and no strict ABI constraints.

Plugins can be authored in Go, Python, Node.js, Lua, Rust, C, or any language capable of reading and writing JSON over stdio.

---

## 1. Transport Architecture

```
Host Core  --write-->  Plugin stdin      (requests & notifications)
Host Core  <--read---  Plugin stdout     (responses, requests & notifications)
Host Core  <--read---  Plugin stderr     (unstructured logs, captured & routed by host)
```

* **One JSON object per line**, terminated by `\n`. No internal newlines or pretty-printing.
* Maximum line length is 8 MB.
* **Never write arbitrary logs to `stdout`**: `stdout` is reserved exclusively for the JSON-RPC wire protocol. Log messages must go to `stderr`.
* Closure or EOF on `stdout`/`stdin` indicates that the host is shutting down: plugins must terminate promptly.

---

## 2. Message Format

### Request
```json
{"jsonrpc":"2.0","method":"tg.send","params":{"peer":"me","text":"Hello"},"id":1}
```

### Success Response
```json
{"jsonrpc":"2.0","id":1,"result":{"id":42,"peer_id":1234}}
```

### Error Response
```json
{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"plugin lacks permission"}}
```

---

## 3. Lifecycle Handshake

1. **`plugin.hello`**:
   The host launches the process and sends `plugin.hello`. The plugin responds with its capabilities:
   ```json
   {"jsonrpc":"2.0","id":1,"result":{"ok":true}}
   ```
2. **`plugin.load`**:
   The host informs the plugin that dependencies and configurations are ready:
   ```json
   {"jsonrpc":"2.0","id":2,"result":{"ok":true}}
   ```
3. **`plugin.unload`**:
   Before process shutdown, the host sends `plugin.unload` for graceful cleanup.

---

## 4. Resource Isolation & Limits

* **Memory Limits (`RLIMIT_DATA`)**:
  Aurora applies POSIX `RLIMIT_DATA` / `RLIMIT_RSS` constraints per plugin process.
* **Idle Timeout Watchdog**:
  Processes that remain idle without responding to heartbeats or event streams are automatically recycled.
