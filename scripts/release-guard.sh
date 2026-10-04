#!/usr/bin/env bash
# release-guard.sh — сторож bare-асетів релізу.
#
# Після кожного релізу хтось регулярно заливає поверх справжніх бінарників
# застарілі копії з prebuilt/ (v2.7.2). Цей скрипт перевіряє, що bare-асети
# `aurora-arm64` / `aurora-amd64` справді містять збірку вказаного тега,
# і за потреби переливає їх із локального prebuilt/ (який оновлюється
# разом із релізом і тому завжди свіжий).
#
# Використання:
#   GH_TOKEN=<токен> scripts/release-guard.sh v2.7.9          # лише перевірка
#   GH_TOKEN=<токен> scripts/release-guard.sh v2.7.9 --fix   # полагодити
#
# Може висіти на кроні/Termux:JobScheduler після кожного релізу.
# Потрібні: curl, python3, tar, sha256sum. Go-тулчейн НЕ потрібен.
set -euo pipefail

REPO="sqwid-member/aurora-userbot"
TAG="${1:?використання: $0 <тег> [--fix]}"
FIX=0
[ "${2:-}" = "--fix" ] && FIX=1
: "${GH_TOKEN:?встановіть GH_TOKEN}"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
api() { curl -s -H "Authorization: Bearer $GH_TOKEN" "$@"; }

RID="$(api "https://api.github.com/repos/$REPO/releases/tags/$TAG" |
  python3 -c "import json,sys; print(json.load(sys.stdin)['id'])")"

fail=0
for arch in arm64 amd64; do
  name="aurora-$arch"
  local_bin="prebuilt/$name"

  # 1. Локальний prebuilt має нести версію тега, інакше лагодити нічим.
  # (через case, а не grep -q: з pipefail обірваний SIGPIPE дає хибний FAIL)
  dump="$(strings "$local_bin" 2>/dev/null || true)"
  case "$dump" in
    *"$TAG"*) ;;
    *)
      echo "✖ $local_bin не містить $TAG — спочатку оновіть prebuilt/"
      fail=1
      continue
      ;;
  esac

  # 2. Що лежить у релізі?
  info="$(api "https://api.github.com/repos/$REPO/releases/tags/$TAG" |
    python3 -c "import json,sys; d=json.load(sys.stdin); a=[x for x in d['assets'] if x['name']=='$name'][0]; print(a['id'],a['size'])")"
  aid="$(echo "$info" | awk '{print $1}')"
  curl -sL -H "Authorization: Bearer $GH_TOKEN" -H "Accept: application/octet-stream" \
    -o "$TMP/$name" "https://api.github.com/repos/$REPO/releases/assets/$aid"
  # 2. Що лежить у релізі? Go-збірки не біт-в-біт відтворювані, тому
  # мірило свіжості — вшита версія, а не хеш байтів.
  ver="$(strings "$TMP/$name" | grep -m1 -o 'v2\.[0-9.]*' || true)"

  if [ "$ver" = "$TAG" ]; then
    echo "✓ $name: свіжий ($TAG)"
    continue
  fi
  echo "✖ $name: ПРОТУХЛИЙ (всередині ${ver:-невідомо}, чекаємо $TAG)"
  fail=1
  [ "$FIX" -eq 0 ] && continue

  echo "→ переливаю $name з $local_bin ..."
  (cd "$(dirname "$local_bin")" && tar -czf "$TMP/$name.tar.gz" "$(basename "$local_bin")" && cp "$local_bin" "$TMP/$name")
  (sha256sum "$TMP/$name" | cut -d' ' -f1 > "$TMP/$name.sha256")
  for a in "$name" "$name.sha256" "$name.tar.gz"; do
    old="$(api "https://api.github.com/repos/$REPO/releases/tags/$TAG" |
      python3 -c "import json,sys; d=json.load(sys.stdin); print([x['id'] for x in d['assets'] if x['name']=='$a'][0])")"
    curl -s -o /dev/null -X DELETE -H "Authorization: Bearer $GH_TOKEN" \
      "https://api.github.com/repos/$REPO/releases/assets/$old"
  done
  ct() { case "$1" in *.sha256) echo text/plain;; *.tar.gz) echo application/gzip;; *) echo application/octet-stream;; esac; }
  for a in "$name" "$name.sha256" "$name.tar.gz"; do
    curl -s -o /dev/null -w "$a -> %{http_code}\n" -X POST \
      -H "Authorization: Bearer $GH_TOKEN" -H "Content-Type: $(ct "$a")" \
      --data-binary "@$TMP/$a" \
      "https://uploads.github.com/repos/$REPO/releases/$RID/assets?name=$a"
  done
done

[ "$fail" -eq 0 ] && echo "ALL CLEAN" || { echo "GUARD FAILED"; exit 1; }
