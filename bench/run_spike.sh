#!/usr/bin/env bash
# Phase 0 spike acceptance suite (SPEC.md §6/§24): mount the passthrough
# FUSE adapter over FUSE-T, run the acceptance checklist (ls, cat, dd 1GB
# read/write, git status, unmount), and print a go/no-go summary.
#
# Requires FUSE-T installed (`brew install --cask fuse-t`) and a built
# janusfs binary (`make build`).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT_DIR/build/janusfs-darwin-$(go env GOARCH)"
SRC="$(mktemp -d)"
MOUNT="$(mktemp -d)"

cleanup() {
  "$BIN" umount "$MOUNT" >/dev/null 2>&1 || true
  rm -rf "$SRC" "$MOUNT"
}
trap cleanup EXIT

echo "== Phase 0 spike: $SRC -> $MOUNT =="

echo "-- seeding source tree with a git repo + demo files --"
cp -R "$ROOT_DIR/testdata/demo-tree/." "$SRC/"
(cd "$SRC" && git init -q && git add -A && git -c user.email=spike@janusfs -c user.name=spike commit -q -m 'seed')

echo "-- mounting --"
"$BIN" mount "$SRC" "$MOUNT" &
MOUNT_PID=$!
sleep 2

FAIL=0

echo "-- ls -la --"
ls -la "$MOUNT" || FAIL=1

echo "-- cat --"
cat "$MOUNT/README.md" || FAIL=1

echo "-- git status (through the mount) --"
(cd "$MOUNT" && git status) || FAIL=1

echo "-- dd 1GB write + read-back --"
dd if=/dev/urandom of="$MOUNT/bigfile.bin" bs=1m count=1024 2>&1 || FAIL=1
WRITE_START=$(date +%s)
dd if=/dev/urandom of="$MOUNT/timed_write.bin" bs=1m count=1024 2>&1
WRITE_END=$(date +%s)
READ_START=$(date +%s)
dd if="$MOUNT/timed_write.bin" of=/dev/null bs=1m 2>&1
READ_END=$(date +%s)
echo "write: $((WRITE_END - WRITE_START))s, read: $((READ_END - READ_START))s (1024 MiB)"

echo "-- unmount --"
"$BIN" umount "$MOUNT" || FAIL=1
sleep 1
wait "$MOUNT_PID" 2>/dev/null || true

if [ "$FAIL" -eq 0 ]; then
  echo "== GO: spike acceptance list passed =="
else
  echo "== NO-GO: one or more checks failed, see output above =="
  exit 1
fi
