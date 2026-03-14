#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

mkdir -p "$TMP_DIR/scripts"
cp "$ROOT_DIR/scripts/deploy-linux.sh" "$TMP_DIR/scripts/deploy-linux.sh"

set +e
OUTPUT="$(bash "$TMP_DIR/scripts/deploy-linux.sh" 2>&1)"
STATUS=$?
set -e

if [[ $STATUS -eq 0 ]]; then
  echo "expected deploy script to fail without .env"
  exit 1
fi

if [[ "$OUTPUT" != *"Missing .env"* ]]; then
  echo "expected missing .env message, got: $OUTPUT"
  exit 1
fi

echo "deploy guard test passed"
