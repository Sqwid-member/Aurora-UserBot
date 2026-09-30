#!/usr/bin/env sh
# Remove Aurora. Pass --purge to delete the data directory as well.
set -eu

BIN_DIR="${AURORA_BIN_DIR:-$HOME/bin}"
DATA_DIR="${AURORA_HOME:-$HOME/.local/share/aurora}"
PURGE=0
[ "${1:-}" = "--purge" ] && PURGE=1

if [ -f "$DATA_DIR/run/aurora.pid" ]; then
  PID="$(cat "$DATA_DIR/run/aurora.pid" 2>/dev/null || true)"
  if [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; then
    kill "$PID" 2>/dev/null || true
    sleep 2
    kill -9 "$PID" 2>/dev/null || true
    echo "зупинено процес $PID"
  fi
fi

if [ -f "$HOME/.termux/boot/aurora" ]; then
  rm -f "$HOME/.termux/boot/aurora"
  echo "видалено автозапуск"
fi

rm -f "$BIN_DIR/aurora"
echo "видалено $BIN_DIR/aurora"

if [ "$PURGE" = "1" ]; then
  rm -rf "$DATA_DIR"
  echo "видалено дані $DATA_DIR (сесія Telegram теж видалена)"
  echo "Якщо хочете повернути авторизацію без SMS — імпортуйте StringSession знову."
else
  echo "дані $DATA_DIR збережено (видаліть вручну або запустіть з --purge)"
fi
