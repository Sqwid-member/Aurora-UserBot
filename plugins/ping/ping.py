#!/usr/bin/env python3
"""Aurora ping plugin — liveness check with plugin uptime.

Answers /ping in any chat (via the host command router) and in the panel.
Dependency-free, Python standard library only.
"""

import json
import sys
import time

_started = time.time()
_name = "ping"


def log(level: str, message: str) -> None:
    print(f"[{level.upper()}] {message}", file=sys.stderr, flush=True)


def send(obj: dict) -> None:
    sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def respond(req: dict, result=None, error: str | None = None) -> None:
    frame = {"jsonrpc": "2.0", "id": req.get("id")}
    if error:
        frame["error"] = {"code": -32000, "message": error}
    else:
        frame["result"] = result
    send(frame)


def uptime() -> str:
    sec = int(time.time() - _started)
    d, sec = divmod(sec, 86400)
    h, sec = divmod(sec, 3600)
    m, sec = divmod(sec, 60)
    if d:
        return f"{d} д {h} год"
    if h:
        return f"{h} год {m} хв"
    if m:
        return f"{m} хв {sec} с"
    return f"{sec} с"


def dispatch(msg: dict) -> None:
    global _name
    method = msg.get("method")

    if method == "plugin.hello":
        _name = msg.get("params", {}).get("plugin", "ping")
        respond(msg, {"ok": True})
    elif method == "plugin.load":
        log("info", "loaded")
        respond(msg, {"ok": True})
    elif method == "plugin.unload":
        log("info", "unloading")
        respond(msg, {"ok": True})
        sys.exit(0)
    elif method == "event":
        pass  # nothing to subscribe to
    elif method == "command":
        params = msg.get("params", {})
        if params.get("name") in ("ping", _name):
            respond(msg, {"text": f"🏓 Понг! Аптайм плагіна: {uptime()}"})
        else:
            respond(msg, error=f"unknown command {params.get('name')}")


def main() -> None:
    while True:
        line = sys.stdin.readline()
        if not line:
            break
        line = line.strip()
        if not line:
            continue
        try:
            dispatch(json.loads(line))
        except json.JSONDecodeError as exc:
            log("error", f"bad json: {exc}")
        except Exception as exc:  # never let one bad message kill the plugin
            log("error", f"dispatch failed: {exc}")


if __name__ == "__main__":
    main()
