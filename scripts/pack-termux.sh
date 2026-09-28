#!/bin/bash
# pack-termux.sh — Створення готового архіву для легкого перенесення в Termux
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

OUTPUT="${1:-aurora-termux.tar.gz}"

echo "Створення автономного архіву $OUTPUT..."
tar --exclude=".git"     --exclude="./dist"     --exclude="*.tmp"     --exclude="*.log"     --exclude="*.tar.gz"     -czf "$OUTPUT"     cmd internal plugins scripts .github go.mod go.sum Makefile README.md LICENSE

ls -lh "$OUTPUT"
echo "Готово! Розмір архіву: $(du -h "$OUTPUT" | cut -f1)"
echo "Для розпакування в Termux виконайте:"
echo "  tar -xzf $OUTPUT"
echo "  bash scripts/install-local.sh"
