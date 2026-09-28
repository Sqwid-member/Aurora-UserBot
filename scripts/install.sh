#!/data/data/com.termux/files/usr/bin/bash
# Aurora installer for Termux (and any other POSIX shell).
#
#   curl -fsSL https://raw.githubusercontent.com/<owner>/Aurora-UserBot/main/scripts/install.sh | bash
#
# What it does:
#   1. detects the platform and asks GitHub for a matching prebuilt binary;
#   2. falls back to compiling from source if no release matches;
#   3. installs ~/bin/aurora, creates the data directory, prints next steps.
#
# Environment:
#   AURORA_REPO      owner/name, defaults to the official repository
#   AURORA_VERSION   a release tag, e.g. v0.1.0. Default: latest
#   AURORA_FORCE_SRC =1 to always build from source
#   AURORA_BIN_DIR   where to install (default ~/bin)
#   AURORA_HOME      data directory (default ~/.local/share/aurora)
set -euo pipefail

REPO="${AURORA_REPO:-Sqwid-member/Aurora-UserBot}"
VERSION="${AURORA_VERSION:-latest}"
DATA_DIR="${AURORA_HOME:-$HOME/.local/share/aurora}"

say()  { printf '\033[35m▚▚▚\033[0m %s\n' "$*"; }
warn() { printf '\033[33m⚠\033[0m  %s\n' "$*" >&2; }
die()  { printf '\033[31m✖\033[0m  %s\n' "$*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || die "потрібен curl: pkg install curl"

# --- 0. platform --------------------------------------------------------------
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  aarch64|arm64) GOARCH=arm64 ;;
  armv7l|armv7)  GOARCH=arm ;;
  armv8l)        GOARCH=arm64 ;;
  x86_64)        GOARCH=amd64 ;;
  i686|i386)     GOARCH=386 ;;
  *) die "непідтримувана архітектура: $ARCH" ;;
esac
case "$OS" in
  linux|android) ;;
  darwin) ;;
  *) warn "OS $OS не перевірена; спробую зібрати з вихідного коду" ;;
esac

if [ -n "${PREFIX:-}" ] && [ "${PREFIX}" = "/data/data/com.termux/files/usr" ]; then
  IS_TERMUX=1
  DEFAULT_BIN_DIR="${PREFIX}/bin"
else
  IS_TERMUX=0
  DEFAULT_BIN_DIR="$HOME/bin"
fi
BIN_DIR="${AURORA_BIN_DIR:-$DEFAULT_BIN_DIR}" 

say "Aurora installer"
printf '     репозиторій: %s\n' "$REPO"
printf '     платформа:   %s/%s → GOOS=%s GOARCH=%s\n' "$OS" "$ARCH" "${OS}" "$GOARCH"
[ "$IS_TERMUX" = "1" ] && printf '     оточення:    Termux\n'

install_termux_pkg() {
  if command -v pkg >/dev/null 2>&1; then
    say "встановлюю: $*"
    pkg install -y "$@" >/dev/null
  else
    warn "встановіть вручну: pkg install $*"
  fi
}

if [ "$IS_TERMUX" = "1" ]; then
  command -v git >/dev/null 2>&1 || install_termux_pkg git
  command -v termux-open-url >/dev/null 2>&1 || install_termux_pkg termux-api
fi

mkdir -p "$BIN_DIR" "$DATA_DIR"
mkdir -p "$DATA_DIR"/{etc,data,plugins,logs,cache,run}
chmod 700 "$DATA_DIR" "$DATA_DIR"/{etc,data,plugins,logs,cache,run} 2>/dev/null || true

# --- 1. prebuilt binary -------------------------------------------------------
fetch_prebuilt() {
  [ "${AURORA_FORCE_SRC:-0}" = "1" ] && return 1

  local tag="$VERSION"
  local candidates=()
  case "$OS" in
    android|linux)
      candidates=("aurora-android-${GOARCH}.tar.gz" "aurora-linux-${GOARCH}.tar.gz")
      ;;
    darwin)
      candidates=("aurora-darwin-${GOARCH}.tar.gz" "aurora-darwin-${GOARCH}")
      ;;
    *) return 1 ;;
  esac

  local tmp
  tmp="$(mktemp -d 2>/dev/null || mktemp -d -t aurora)"

  for asset in "${candidates[@]}"; do
    local url
    if [ "$tag" = "latest" ]; then
      url="https://github.com/$REPO/releases/latest/download/${asset}"
    else
      url="https://github.com/$REPO/releases/download/${tag}/${asset}"
    fi

    say "завантаження $asset..."
    if curl -fsSL --retry 2 --connect-timeout 10 --max-time 180 -o "$tmp/archive.tar.gz" "$url" 2>/dev/null; then
      if [ "${asset##*.}" = "gz" ]; then
        if tar -C "$tmp" -xzf "$tmp/archive.tar.gz" 2>/dev/null; then
          local found
          found="$(find "$tmp" -type f -name "aurora*" ! -name "*.tar.gz" | head -n 1)"
          if [ -n "$found" ] && [ -f "$found" ]; then
            mv "$found" "$BIN_DIR/aurora"
            chmod 755 "$BIN_DIR/aurora"
            rm -rf "$tmp"
            return 0
          fi
        fi
      else
        mv "$tmp/archive.tar.gz" "$BIN_DIR/aurora"
        chmod 755 "$BIN_DIR/aurora"
        rm -rf "$tmp"
        return 0
      fi
    fi
  done

  # Fallback: query API if direct latest redirect failed
  if [ "$tag" = "latest" ]; then
    local api_tag
    api_tag="$(curl -fsSL --max-time 15 "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null \
      | tr ',' '\n' | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\{0,1\}\([^",]*\)"\{0,1\}.*/\1/p' | head -1)"
    if [ -n "$api_tag" ]; then
      for asset in "${candidates[@]}"; do
        url="https://github.com/$REPO/releases/download/${api_tag}/${asset}"
        if curl -fsSL --retry 2 --max-time 180 -o "$tmp/archive.tar.gz" "$url" 2>/dev/null; then
          if tar -C "$tmp" -xzf "$tmp/archive.tar.gz" 2>/dev/null; then
            local found
            found="$(find "$tmp" -type f -name "aurora*" ! -name "*.tar.gz" | head -n 1)"
            if [ -n "$found" ] && [ -f "$found" ]; then
              mv "$found" "$BIN_DIR/aurora"
              chmod 755 "$BIN_DIR/aurora"
              rm -rf "$tmp"
              return 0
            fi
          fi
        fi
      done
    fi
  fi

  rm -rf "$tmp"
  return 1
}

# --- 2. fallback: build from source ------------------------------------------
build_from_source() {
  command -v git >/dev/null 2>&1 || die "потрібен git: pkg install git"
  if ! command -v go >/dev/null 2>&1; then
    if [ "$IS_TERMUX" = "1" ]; then
      install_termux_pkg golang
    else
      say "встановлюю Go локально у ~/.local/go"
      local go_root="$HOME/.local/go"
      if [ ! -x "$go_root/bin/go" ]; then
        curl -fsSL --retry 2 -o "$HOME/.local/go.tgz" \
          "https://go.dev/dl/go1.23.4.linux-${GOARCH}.tar.gz" || die "не вдалося завантажити Go"
        tar -C "$HOME/.local" -xzf "$HOME/.local/go.tgz"
        rm -f "$HOME/.local/go.tgz"
      fi
      export PATH="$go_root/bin:$PATH"
    fi
  fi
  command -v go >/dev/null 2>&1 || die "Go не знайдено в PATH"

  local src="${AURORA_SRC:-$HOME/.local/src/aurora}"
  if [ -d "$src/.git" ]; then
    git -C "$src" fetch --depth 1 origin
    git -C "$src" reset --hard origin/main
  else
    mkdir -p "$(dirname "$src")"
    git clone --depth 1 "https://github.com/$REPO.git" "$src" || die "git clone не вдався"
  fi

  say "збираю (CGO вимкнено — це важливо для Termux)"
  ( cd "$src" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" \
      go build -trimpath -ldflags "-s -w" -o "$BIN_DIR/aurora" ./cmd/aurora )
  chmod 755 "$BIN_DIR/aurora"
  SRC="$src"
}

SRC=""
if fetch_prebuilt; then
  say "готовий бінарник завантажено"
else
  warn "готових збірок для цієї платформи немає — збираю з вихідного коду"
  build_from_source
fi

# --- 3. PATH ------------------------------------------------------------------
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) warn "додайте в ~/.bashrc:"; printf '\n    export PATH="%s:$PATH"\n' "$BIN_DIR" ;;
esac

# --- 4. example plugins -------------------------------------------------------
if [ -n "$SRC" ] && [ -d "$SRC/plugins" ]; then
  for d in echo pulse autoaway; do
    if [ -d "$SRC/plugins/$d" ] && [ ! -d "$DATA_DIR/plugins/$d" ]; then
      cp -r "$SRC/plugins/$d" "$DATA_DIR/plugins/$d" 2>/dev/null || true
    fi
  done
  say "приклади плагінів скопійовано у $DATA_DIR/plugins"
fi

# --- 5. verify + first run ----------------------------------------------------
if ! "$BIN_DIR/aurora" version >/dev/null 2>&1; then
  die "бінарник не запускається. Перевірте архітектуру або зберіть з вихідного коду (AURORA_FORCE_SRC=1)"
fi

echo
say "Готово! $( "$BIN_DIR/aurora" version )"
cat <<EOF

  Бінарник:  $BIN_DIR/aurora
  Дані:      $DATA_DIR

  Швидке використання в Termux:

    1. Авторизація в Telegram:
         aurora login       # вхід у терміналі (номер -> код -> пароль)
         aurora login web   # вхід через веб-панель у браузері

    2. Робота з юзерботом:
         aurora start       # запуск у фоні (працює при згортанні Termux)
         aurora run         # запуск у відкритому терміналі
         aurora status      # перевірка активності та пам'яті (RAM)
         aurora logs        # живий журнал логів
         aurora stop        # зупинка процесу
         aurora panel       # адреса веб-панелі

    3. Додатково (якщо виникають блокування або потрібні власні ключі):
         aurora setup       # налаштування ключів my.telegram.org

EOF

if [ -t 0 ] && [ -t 1 ]; then
  if [ ! -f "$DATA_DIR/data/session.json" ]; then
    printf "\n\033[36m🌌 Оберіть спосіб входу в Telegram:\033[0m\n"
    printf "  1) \033[32mУ терміналі\033[0m прямо зараз (номер -> код -> пароль)\n"
    printf "  2) \033[33mУ веб-панелі\033[0m через браузер (відкриється автоматично)\n"
    printf "  3) Пропустити (увійти пізніше)\n"
    printf "Ваш вибір [1/2/3, за замовчуванням 1]: "
    read -r choice
    case "${choice:-1}" in
      1)
        "$BIN_DIR/aurora" login
        ;;
      2)
        "$BIN_DIR/aurora" login web
        ;;
      *)
        say "Ви можете увійти пізніше:"
        printf "    aurora login       # вхід у терміналі\n"
        printf "    aurora login web   # вхід через браузер\n"
        printf "    aurora start       # запуск фонової служби\n"
        ;;
    esac
  fi
fi
