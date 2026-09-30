#!/usr/bin/env sh
# Aurora autostart on boot under Termux: termux-boot.
#
#   pkg install termux-boot
#   mkdir -p ~/.termux/boot
#   ln -sf "$(dirname "$0")/aurora-boot.sh" ~/.termux/boot/aurora
#
# termux-boot runs this only after an actual device reboot, never on app kill.
set -eu

# Give the network a moment to come up; a userbot that starts before the
# radio is ready just burns reconnect cycles.
i=0
while [ $i -lt 30 ]; do
  if command -v getprop >/dev/null 2>&1; then
    STATE="$(getprop sys.boot_completed 2>/dev/null || echo 0)"
    [ "$STATE" = "1" ] && break
  fi
  sleep 2
  i=$((i + 1))
done
sleep 15

export PATH="${PREFIX:-/data/data/com.termux/files/usr}/bin:$HOME/bin:$PATH"
LOG_DIR="$HOME/.local/share/aurora/logs"
mkdir -p "$LOG_DIR"

# Never start a second instance: aurora's own pid file is the arbiter.
if [ -f "$HOME/.local/share/aurora/run/aurora.pid" ]; then
  PID="$(cat "$HOME/.local/share/aurora/run/aurora.pid" 2>/dev/null || true)"
  if [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; then
    echo "aurora вже працює (pid $PID)"
    exit 0
  fi
fi

aurora start
