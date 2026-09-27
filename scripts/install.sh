#!/data/data/com.termux/files/usr/bin/bash
# Aurora installer for Termux (and any other POSIX shell).
#
#   curl -fsSL https://raw.githubusercontent.com/aurora/aurora/main/scripts/install.sh | bash
#
# What it does:
#   1. checks the environment and installs only what is genuinely missing;
#   2. prefers a prebuilt binary, falls back to compiling from source;
#   3. creates ~/bin/aurora and prints the first-login steps.
set -euo pipefail

REPO="${AURORA_REPO:-https://github.com/aurora/aurora}"
VERSION="${AURORA_VERSION:-latest}"
BIN_DIR="${AURORA_BIN_DIR:-$HOME/bin}"
DATA_DIR="${AURORA_HOME:-$HOME/.local/share/aurora}"

say()  { printf '\033[35m▚▚▚\033[0m %s\n' "$*"; }
warn() { printf '\033[33m⚠\033[0m  %s\n' "$*" >&2; }
die()  { printf '\033[31m✖\033[0m  %s\n' "$*" >&2; exit 1; }

# --- 0. sanity -----------------------------------------------------------------
command -v curl >/dev/null 2>&1 || die "потрібен curl: pkg install curl"

if [ -n "${PREFIX:-}" ] && [ -d "/data/data/com.termux/files/usr" ] && [ "${PREFIX}" = "/data/data/com.termux/files/usr" ]; then
  IS_TERMUX=1
else
  IS_TERMUX=0
fi

OS="$(uname -s)"
ARCH="$(uname -m)"
case "$ARCH" in
  aarch64|arm64) GOARCH=arm64 ;;
  armv7l|armv7)  GOARCH=armv7 ;;
  armv8l)        GOARCH=arm64 ;;
  x86_64)        GOARCH=amd64 ;;
  i686|i386)     GOARCH=386 ;;
  *) die "непідтримувана архітектура: $ARCH" ;;
esac

say "Aurora installer"
printf '     система: %s/%s → GOARCH=%s\n' "$OS" "$ARCH" "$GOARCH"
[ "$IS_TERMUX" = "1" ] && printf '     оточення: Termux\n'

# --- 1. dependencies -----------------------------------------------------------
need_pkg() { command -v "$1" >/dev/null 2>&1; }

install_termux_pkg() {
  if need_pkg pkg; then
    say "встановлюю: $*"
    pkg install -y "$@" >/dev/null
  else
    warn "встановіть вручну: pkg install $*"
  fi
}

if [ "$IS_TERMUX" = "1" ]; then
  need_pkg git || install_termux_pkg git
  need_pkg termux-api || install_termux_pkg termux-api   # termux-open-url
fi

mkdir -p "$BIN_DIR" "$DATA_DIR"
mkdir -p "$DATA_DIR"/{etc,data,plugins,logs,cache,run}
chmod 700 "$DATA_DIR" "$DATA_DIR"/{etc,data,plugins,logs,cache,run} 2>/dev/null || true

# --- 2. build or download ------------------------------------------------------
built=0

if [ "$VERSION" != "latest" ] && need_pkg curl; then
  url="https://github.com/aurora/aurora/releases/download/${VERSION}/aurora-${OS,,}-${GOARCH}"
  say "пробую завантажити готовий бінарник: $url"
  if curl -fsSL --retry 2 -o "$BIN_DIR/aurora.tmp" "$url"; then
    mv "$BIN_DIR/aurora.tmp" "$BIN_DIR/aurora"
    chmod 755 "$BIN_DIR/aurora"
    built=1
  else
    warn "release не знайдено — збираю з вихідного коду"
  fi
fi

if [ "$built" = "0" ]; then
  need_pkg git || die "потрібен git: pkg install git"
  if need_pkg go; then
    GOVER="$(go env GOVERSION 2>/dev/null | head -1)"
    say "використовую встановлений Go ($GOVER)"
  else
    if [ "$IS_TERMUX" = "1" ]; then
      install_termux_pkg golang
    else
      say "встановлюю Go 1.23 локально"
      GO_ROOT="$HOME/.local/go"
      if [ ! -x "$GO_ROOT/bin/go" ]; then
        curl -fsSL --retry 2 -o /tmp/go.tgz "https://go.dev/dl/go1.23.4.linux-${GOARCH}.tar.gz" || die "не вдалося завантажити Go"
        tar -C "$HOME/.local" -xzf /tmp/go.tgz
        rm -f /tmp/go.tgz
      fi
      export PATH="$GO_ROOT/bin:$PATH"
    fi
  fi
  command -v go >/dev/null 2>&1 || die "Go не знайдено в PATH"

  SRC="${AURORA_SRC:-$HOME/.local/src/aurora}"
  say "клоную репозиторій у $SRC"
  if [ -d "$SRC/.git" ]; then
    git -C "$SRC" pull --ff-only
  else
    mkdir -p "$(dirname "$SRC")"
    git clone --depth 1 "$REPO" "$SRC"
  fi

  say "збираю (CGO вимкнено — це важливо для Termux)"
  ( cd "$SRC" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" \
      go build -trimpath -ldflags "-s -w" -o "$BIN_DIR/aurora" ./cmd/aurora )
  chmod 755 "$BIN_DIR/aurora"
  built=1
fi

# --- 3. PATH -------------------------------------------------------------------
export PATH="$BIN_DIR:$PATH"
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) warn "додайте рядок у ~/.bashrc:"; printf '\n    export PATH="%s:$PATH"\n' "$BIN_DIR" ;;
esac

# --- 4. examples ---------------------------------------------------------------
if [ -d "$SRC/plugins" ]; then
  for d in echo pulse autoaway; do
    if [ -d "$SRC/plugins/$d" ] && [ ! -d "$DATA_DIR/plugins/$d" ]; then
      cp -r "$SRC/plugins/$d" "$DATA_DIR/plugins/$d" 2>/dev/null || true
    fi
  done
  say "приклади плагінів скопійовано у $DATA_DIR/plugins"
fi

# --- 5. first run --------------------------------------------------------------
echo
say "Готово."
cat <<EOF

  Бінарник:   $BIN_DIR/aurora
  Дані:       $DATA_DIR

  Далі:

    1) Отримайте app_id і app_hash на https://my.telegram.org
       (API development tools). Вони обов'язкові — Telegram не приймає
       реєстрації з пустими ключами.

    2) Заповніть їх одразу:

         aurora config > $DATA_DIR/etc/config.json

       або відкрийте $DATA_DIR/etc/config.json у редакторі.

    3) Запустіть ядро:

         aurora run

  Панель керування підніметься на http://127.0.0.1:8420 — токен буде
  надруковано в термінал. Відкриється автоматично, якщо встановлено
  Termux:API.

  Перевірити оточення:  aurora doctor
EOF
