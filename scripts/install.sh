#!/data/data/com.termux/files/usr/bin/bash
# Aurora universal installer for Termux & Linux
# Works offline (local file / Download folder) or online (GitHub Releases with GITHUB_TOKEN support).
#
#   curl -fsSL https://raw.githubusercontent.com/<owner>/Aurora-UserBot/main/scripts/install.sh | bash
#   або
#   bash install.sh
set -euo pipefail

REPO="${AURORA_REPO:-Sqwid-member/Aurora-UserBot}"
VERSION="${AURORA_VERSION:-v2.4.0}"
DATA_DIR="${AURORA_HOME:-$HOME/.local/share/aurora}"

say()  { printf '\033[35m▚▚▚\033[0m %s\n' "$*"; }
warn() { printf '\033[33m⚠\033[0m  %s\n' "$*" >&2; }
die()  { printf '\033[31m✖\033[0m  %s\n' "$*" >&2; exit 1; }

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

if [ -n "${PREFIX:-}" ] && [ "${PREFIX}" = "/data/data/com.termux/files/usr" ]; then
  IS_TERMUX=1
  DEFAULT_BIN_DIR="${PREFIX}/bin"
else
  IS_TERMUX=0
  DEFAULT_BIN_DIR="$HOME/bin"
fi
BIN_DIR="${AURORA_BIN_DIR:-$DEFAULT_BIN_DIR}"

say "Встановлення Aurora UserBot"
printf '     репозиторій: %s\n' "$REPO"
printf '     платформа:   %s/%s (GOARCH=%s)\n' "$OS" "$ARCH" "$GOARCH"
[ "$IS_TERMUX" = "1" ] && printf '     оточення:    Termux (Android)\n'

mkdir -p "$BIN_DIR" "$DATA_DIR"
mkdir -p "$DATA_DIR"/{etc,data,plugins,logs,cache,run}
chmod 700 "$DATA_DIR" "$DATA_DIR"/{etc,data,plugins,logs,cache,run} 2>/dev/null || true

# --- 1. offline local search first --------------------------------------------
find_local_package() {
  local script_dir
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd || echo "")"
  local root_dir
  root_dir="$(cd "$script_dir/.." 2>/dev/null && pwd || echo "")"

  local search_dirs=(
    "."
    "./dist"
    "$root_dir"
    "$root_dir/dist"
    "/sdcard/Download"
    "/storage/emulated/0/Download"
    "$HOME/storage/downloads"
    "$HOME/downloads"
    "$HOME/Download"
  )

  local candidates=(
    "aurora-linux-${GOARCH}.tar.gz"
    "aurora-${OS}-${GOARCH}.tar.gz"
    "aurora-android-${GOARCH}.tar.gz"
    "aurora-linux-${GOARCH}"
  )
  if [ "$GOARCH" = "arm64" ]; then
    candidates=("aurora-termux-ready.tar.gz" "${candidates[@]}")
  fi

  for d in "${search_dirs[@]}"; do
    [ -d "$d" ] || continue
    for f in "${candidates[@]}"; do
      if [ -f "$d/$f" ]; then
        say "Знайдено готовий локальний файл: $d/$f"
        local tmp
        tmp="$(mktemp -d 2>/dev/null || mktemp -d -t aurora)"
        if [[ "$f" == *.tar.gz ]]; then
          if tar -C "$tmp" -xzf "$d/$f" 2>/dev/null; then
            local found
            found="$(find "$tmp" -type f -name "aurora*" ! -name "*.tar.gz" | head -n 1)"
            if [ -n "$found" ] && [ -f "$found" ]; then
              cp "$found" "$BIN_DIR/aurora"
              chmod 755 "$BIN_DIR/aurora"
              if [ -d "$tmp/plugins" ]; then
                cp -r "$tmp/plugins/"* "$DATA_DIR/plugins/" 2>/dev/null || true
              fi
              rm -rf "$tmp"
              return 0
            fi
          fi
        else
          cp "$d/$f" "$BIN_DIR/aurora"
          chmod 755 "$BIN_DIR/aurora"
          rm -rf "$tmp"
          return 0
        fi
        rm -rf "$tmp"
      fi
    done
  done
  return 1
}

# --- 2. download from GitHub Releases -----------------------------------------
fetch_prebuilt() {
  [ "${AURORA_FORCE_SRC:-0}" = "1" ] && return 1

  if ! command -v curl >/dev/null 2>&1; then
    if [ "$IS_TERMUX" = "1" ] && command -v pkg >/dev/null 2>&1; then
      pkg install -y curl >/dev/null 2>&1 || true
    fi
  fi
  command -v curl >/dev/null 2>&1 || return 1

  local curl_auth=()
  if [ -n "${GITHUB_TOKEN:-}" ]; then
    curl_auth=(-H "Authorization: Bearer $GITHUB_TOKEN")
  fi

  local tag="$VERSION"
  local candidates=(
    "aurora-termux-ready.tar.gz"
    "aurora-linux-${GOARCH}.tar.gz"
    "aurora-android-${GOARCH}.tar.gz"
    "aurora-linux-${GOARCH}"
  )

  local tmp
  tmp="$(mktemp -d 2>/dev/null || mktemp -d -t aurora)"

  for asset in "${candidates[@]}"; do
    local url="https://github.com/$REPO/releases/download/${tag}/${asset}"
    say "Спроба завантаження з GitHub: $asset..."
    if curl -fsSL "${curl_auth[@]}" --retry 2 --connect-timeout 10 --max-time 180 -o "$tmp/archive.bin" "$url" 2>/dev/null; then
      if [[ "$asset" == *.tar.gz ]]; then
        if tar -C "$tmp" -xzf "$tmp/archive.bin" 2>/dev/null; then
          local found
          found="$(find "$tmp" -type f -name "aurora*" ! -name "*.bin" ! -name "*.tar.gz" | head -n 1)"
          if [ -n "$found" ] && [ -f "$found" ]; then
            mv "$found" "$BIN_DIR/aurora"
            chmod 755 "$BIN_DIR/aurora"
            if [ -d "$tmp/plugins" ]; then
              cp -r "$tmp/plugins/"* "$DATA_DIR/plugins/" 2>/dev/null || true
            fi
            rm -rf "$tmp"
            return 0
          fi
        fi
      else
        mv "$tmp/archive.bin" "$BIN_DIR/aurora"
        chmod 755 "$BIN_DIR/aurora"
        rm -rf "$tmp"
        return 0
      fi
    fi
  done
  rm -rf "$tmp"
  return 1
}

# --- 3. fallback: build from source ------------------------------------------
build_from_source() {
  command -v git >/dev/null 2>&1 || die "потрібен git: pkg install git"
  if ! command -v go >/dev/null 2>&1; then
    if [ "$IS_TERMUX" = "1" ]; then
      say "Встановлюю Go компілятор..."
      pkg install -y golang
    else
      die "Для компіляції встановіть Go або надайте попередньо зібраний бінарник"
    fi
  fi

  local src="${AURORA_SRC:-$HOME/.local/src/aurora}"
  local clone_url="https://github.com/$REPO.git"
  if [ -n "${GITHUB_TOKEN:-}" ]; then
    clone_url="https://${GITHUB_TOKEN}@github.com/$REPO.git"
  fi

  if [ -d "$src/.git" ]; then
    say "Оновлення репозиторію..."
    git -C "$src" fetch origin main || true
    git -C "$src" reset --hard origin/main || true
  else
    rm -rf "$src"
    mkdir -p "$(dirname "$src")"
    git clone --depth 1 "$clone_url" "$src" || die "git clone не вдався"
  fi

  if [ -f "$src/prebuilt/aurora-${GOARCH}" ]; then
    say "Використовую перевірений готовий бінарник: prebuilt/aurora-${GOARCH}"
    cp "$src/prebuilt/aurora-${GOARCH}" "$BIN_DIR/aurora"
    chmod 755 "$BIN_DIR/aurora"
  else
    say "Компіляція статичного бінарника..."
    ( cd "$src" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" \
        go build -trimpath -ldflags "-s -w" -o "$BIN_DIR/aurora" ./cmd/aurora )
  fi
  chmod 755 "$BIN_DIR/aurora"
  if [ -d "$src/plugins" ]; then
    for d in echo pulse autoaway hello; do
      [ -d "$src/plugins/$d" ] && cp -r "$src/plugins/$d" "$DATA_DIR/plugins/$d" 2>/dev/null || true
    done
  fi
}

# Execution
if find_local_package; then
  say "Встановлено з локального файлу."
elif fetch_prebuilt; then
  say "Завантажено та встановлено реліз з GitHub."
else
  warn "Локального архіву чи доступу до релізу немає. Спроба збірки з вихідного коду..."
  build_from_source
fi

# --- 4. Termux & Shell environment setup -------------------------------------
if [ "$IS_TERMUX" = "1" ]; then
  # Утримання фонової роботи процесора при вимкненому екрані
  termux-wake-lock 2>/dev/null || true

  # Додавання корисних скорочень (аліасів)
  BASHRC="$HOME/.bashrc"
  touch "$BASHRC"
  if ! grep -q "alias a='aurora'" "$BASHRC" 2>/dev/null; then
    cat >> "$BASHRC" << 'ALIASES'

# Aurora UserBot shortcuts
alias a='aurora'
alias a-status='aurora status'
alias a-logs='aurora logs'
alias a-start='aurora start'
alias a-stop='aurora stop'
alias a-web='aurora login web'
ALIASES
  fi

  # Налаштування автозапуску при перезавантаженні Android
  BOOT_DIR="$HOME/.termux/boot"
  mkdir -p "$BOOT_DIR" 2>/dev/null || true
  if [ -d "$BOOT_DIR" ]; then
    cat > "$BOOT_DIR/aurora-boot.sh" << 'BOOTSCRIPT'
#!/data/data/com.termux/files/usr/bin/sh
termux-wake-lock
sleep 5
aurora start
BOOTSCRIPT
    chmod 755 "$BOOT_DIR/aurora-boot.sh"
  fi
fi

# Ensure BIN_DIR is in PATH
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *)
    if [ -f "$HOME/.bashrc" ]; then
      echo "export PATH=\"$BIN_DIR:\$PATH\"" >> "$HOME/.bashrc"
    fi
    export PATH="$BIN_DIR:$PATH"
    ;;
esac

# --- 5. verify + first run ----------------------------------------------------
if ! "$BIN_DIR/aurora" version >/dev/null 2>&1; then
  die "Бінарник не запускається. Перевірте архітектуру."
fi

echo
echo
say "Встановлення завершено успішно! $( "$BIN_DIR/aurora" version )"
cat <<EOF

  Бінарник:   $BIN_DIR/aurora
  Дані:       $DATA_DIR
  Автостарт:  налаштовано (wake-lock увімкнено)
  Скорочення: a, a-status, a-logs, a-start, a-stop, a-web

EOF

if [ -t 0 ] && [ -t 1 ]; then
  if [ ! -f "$DATA_DIR/data/session.json" ]; then
    printf "\n\033[36m🌌 Бажаєте увійти в Telegram зараз?\033[0m\n"
    printf "  1) \033[33mУ веб-панелі\033[0m через браузер телефону (рекомендовано)\n"
    printf "  2) \033[32mУ терміналі\033[0m прямо тут\n"
    printf "  3) Пропустити\n"
    printf "Вибір [1/2/3, за замовчуванням 1]: "
    read -r choice || choice="1"
    case "${choice:-1}" in
      1)
        "$BIN_DIR/aurora" login web
        ;;
      2)
        "$BIN_DIR/aurora" login
        ;;
      *)
        say "Для запуску виконайте: aurora start"
        ;;
    esac
  fi
fi
