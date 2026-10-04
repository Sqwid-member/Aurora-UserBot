#!/usr/bin/env python3
"""Aurora calc plugin — safe math evaluator.

/calc 2 + 2 * 2  ->  6
Supports + - * / // % **, parentheses, pi/e/tau and a small set of
math functions. Implemented on ast, never eval(). Precision is tunable
in the plugin settings.
"""

import ast
import json
import math
import operator
import sys
import threading
import time

OPS = {
    ast.Add: operator.add,
    ast.Sub: operator.sub,
    ast.Mult: operator.mul,
    ast.Div: operator.truediv,
    ast.FloorDiv: operator.floordiv,
    ast.Mod: operator.mod,
    ast.Pow: operator.pow,
    ast.UAdd: operator.pos,
    ast.USub: operator.neg,
}

consts = {"pi": math.pi, "e": math.e, "tau": math.tau}

funcs = {
    "sqrt": math.sqrt,
    "sin": math.sin,
    "cos": math.cos,
    "tan": math.tan,
    "log": math.log,
    "ln": math.log,
    "exp": math.exp,
    "abs": abs,
    "round": round,
    "floor": math.floor,
    "ceil": math.ceil,
    "factorial": math.factorial,
}

_name = "calc"
_SEQ = [0]
_pending = {}
SETTINGS = {"precision": 6}


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


def evaluate(node):
    if isinstance(node, ast.Expression):
        return evaluate(node.body)
    if isinstance(node, ast.Constant):
        if isinstance(node.value, bool) or not isinstance(node.value, (int, float)):
            raise ValueError("тільки числа")
        return node.value
    if isinstance(node, ast.BinOp):
        op = OPS.get(type(node.op))
        if op is None:
            raise ValueError("операція не підтримується")
        return op(evaluate(node.left), evaluate(node.right))
    if isinstance(node, ast.UnaryOp):
        op = OPS.get(type(node.op))
        if op is None:
            raise ValueError("операція не підтримується")
        return op(evaluate(node.operand))
    if isinstance(node, ast.Name):
        if node.id in consts:
            return consts[node.id]
        raise ValueError(f"невідома константа: {node.id}")
    if isinstance(node, ast.Call):
        if not isinstance(node.func, ast.Name) or node.func.id not in funcs:
            raise ValueError("такої функції немає (є: sqrt sin cos tan log exp abs round floor ceil factorial)")
        if node.keywords or len(node.args) > 2:
            raise ValueError("не більше двох аргументів")
        return funcs[node.func.id](*[evaluate(a) for a in node.args])
    raise ValueError("такий вираз не вмію")


def calculate(text: str) -> str:
    expr = (text or "").strip()
    if not expr:
        return "🧮 Вкажіть вираз: /calc 2 + 2 * 2"
    try:
        tree = ast.parse(expr, mode="eval")
    except SyntaxError:
        return "🧮 Не зрозумів вираз — приклад: /calc (2 + 3) * 4"
    try:
        value = evaluate(tree)
    except ZeroDivisionError:
        return "🧮 На нуль ділити не можна"
    except (ValueError, OverflowError) as exc:
        return f"🧮 Помилка: {exc}"
    except Exception:
        return "🧮 Не вийшло порахувати"
    if isinstance(value, float):
        if math.isinf(value) or math.isnan(value):
            return "🧮 Нескінченність — не результат"
        prec = SETTINGS.get("precision", 6)
        value = round(value, prec)
        if value == int(value):
            value = int(value)
    return f"🧮 {expr} = **{value}**"


def next_id() -> int:
    _SEQ[0] += 1
    return _SEQ[0]


def call_wait(method: str, params: dict, timeout: float = 10.0):
    rid = next_id()
    box: list = []
    _pending[rid] = box
    try:
        send({"jsonrpc": "2.0", "id": rid, "method": method, "params": params})
        deadline = time.time() + timeout
        while time.time() < deadline:
            if box:
                resp = box.pop(0)
                if resp.get("error"):
                    return None
                return resp.get("result") or {}
            line = sys.stdin.readline()
            if not line:
                return None
            line = line.strip()
            if not line:
                continue
            try:
                dispatch(json.loads(line))
            except json.JSONDecodeError as exc:
                log("error", f"bad json: {exc}")
        return None
    finally:
        _pending.pop(rid, None)


def load_settings() -> None:
    res = call_wait("settings.get", {"key": "precision"})
    if isinstance(res, dict) and res.get("found"):
        try:
            prec = int(res.get("value", 6))
            SETTINGS["precision"] = max(0, min(10, prec))
        except (TypeError, ValueError):
            pass


def dispatch(msg: dict) -> None:
    global _name
    if msg.get("method") is None and msg.get("id") in _pending:
        _pending[msg["id"]].append(msg)
        return
    method = msg.get("method")
    if method == "plugin.hello":
        _name = msg.get("params", {}).get("plugin", "calc")
        respond(msg, {"ok": True})
    elif method == "plugin.load":
        load_settings()
        log("info", "loaded")
        respond(msg, {"ok": True})
    elif method == "plugin.unload":
        log("info", "unloading")
        respond(msg, {"ok": True})
        sys.exit(0)
    elif method == "event":
        ev = msg.get("params", {})
        if ev.get("name") == "settings.changed":
            load_settings()
    elif method == "command":
        params = msg.get("params", {})
        if params.get("name") in ("calc", _name):
            respond(msg, {"text": calculate(params.get("text", ""))})
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
        except Exception as exc:
            log("error", f"dispatch failed: {exc}")


if __name__ == "__main__":
    main()
