#!/usr/bin/env bash
set -euo pipefail

REPO="${REPO:-corruptmem/msx}"
WORKFLOW="${WORKFLOW:-package.yml}"
DEST_DIR="${1:-${DEST_DIR:-./bin}}"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"

case "$os" in
  linux) goos="linux" ;;
  darwin) goos="darwin" ;;
  msys*|mingw*|cygwin*) goos="windows" ;;
  *)
    echo "unsupported OS: $os" >&2
    exit 1
    ;;
esac

case "$arch" in
  x86_64|amd64) goarch="amd64" ;;
  arm64|aarch64) goarch="arm64" ;;
  *)
    echo "unsupported architecture: $arch" >&2
    exit 1
    ;;
esac

artifact="msx-${goos}-${goarch}"

if ! command -v gh >/dev/null 2>&1; then
  echo "gh CLI is required" >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  sha_cmd=(sha256sum --check)
elif command -v shasum >/dev/null 2>&1; then
  sha_cmd=(shasum -a 256 --check)
else
  echo "sha256sum or shasum is required for artifact verification" >&2
  exit 1
fi

run_id="$({
  gh run list \
    --repo "$REPO" \
    --workflow "$WORKFLOW" \
    --branch main \
    --status completed \
    --json databaseId,conclusion \
    --limit 20 \
    --jq '.[] | select(.conclusion == "success") | .databaseId' \
  | head -n 1
} || true)"

if [[ -z "$run_id" ]]; then
  echo "no successful $WORKFLOW run found on $REPO" >&2
  exit 1
fi

head_sha="$({
  gh run view "$run_id" \
    --repo "$REPO" \
    --json headSha \
    --jq '.headSha'
} || true)"

if [[ -z "$run_id" || "$run_id" == "null" ]]; then
  echo "failed to resolve workflow run id" >&2
  exit 1
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

gh run download "$run_id" --repo "$REPO" --name "$artifact" --dir "$tmp"
gh run download "$run_id" --repo "$REPO" --name "msx-checksums" --dir "$tmp/checksums"

mkdir -p "$DEST_DIR"

checksums_file="$tmp/checksums/SHA256SUMS"
if [[ ! -f "$checksums_file" ]]; then
  echo "expected checksum manifest not found: $checksums_file" >&2
  exit 1
fi

if [[ "$goos" == "windows" ]]; then
  archive="$tmp/${artifact}.zip"
  if [[ ! -f "$archive" ]]; then
    echo "expected archive not found: $archive" >&2
    exit 1
  fi
  (cd "$tmp" && grep " ${artifact}.zip$" "$checksums_file" | "${sha_cmd[@]}")
  unzip -q "$archive" -d "$tmp/unpacked"
  cp "$tmp/unpacked/${artifact}/msx.exe" "$DEST_DIR/msx.exe"
  echo "installed $DEST_DIR/msx.exe from run $run_id (commit $head_sha)"
  "$DEST_DIR/msx.exe" version || true
else
  archive="$tmp/${artifact}.tar.gz"
  if [[ ! -f "$archive" ]]; then
    echo "expected archive not found: $archive" >&2
    exit 1
  fi
  (cd "$tmp" && grep " ${artifact}.tar.gz$" "$checksums_file" | "${sha_cmd[@]}")
  tar -xzf "$archive" -C "$tmp"
  cp "$tmp/${artifact}/msx" "$DEST_DIR/msx"
  chmod +x "$DEST_DIR/msx"
  echo "installed $DEST_DIR/msx from run $run_id (commit $head_sha)"
  "$DEST_DIR/msx" version || true
fi
