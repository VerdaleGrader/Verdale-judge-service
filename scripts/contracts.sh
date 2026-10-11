#!/usr/bin/env bash
# Pins and verifies the vendored copy of the Verdale contracts.
#
#   scripts/contracts.sh sync <path-to-Verdale-core-service/contracts>
#       Copy the contract files used by this repo and write contracts/contracts.lock.json.
#   scripts/contracts.sh verify
#       Fail when a vendored file differs from the SHA-256 recorded in the lock.
#
# The same script lives in every consumer repo; only FILES differs.
set -euo pipefail

FILES=(judge.openapi.yaml HMAC.md hmac-test-vectors.json VERSION CHANGELOG.md)
DEST="$(cd "$(dirname "$0")/.." && pwd)/contracts"
LOCK="$DEST/contracts.lock.json"

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

case "${1:-}" in
  sync)
    SRC="${2:?usage: contracts.sh sync <path-to-core-contracts-dir>}"
    mkdir -p "$DEST"
    for file in "${FILES[@]}"; do cp "$SRC/$file" "$DEST/$file"; done
    version="$(tr -d '[:space:]' < "$DEST/VERSION")"
    {
      printf '{\n  "version": "%s",\n  "source": "Verdale-core-service/contracts",\n  "files": {\n' "$version"
      last=$((${#FILES[@]} - 1))
      for index in "${!FILES[@]}"; do
        separator=","; [ "$index" -eq "$last" ] && separator=""
        printf '    "%s": "%s"%s\n' "${FILES[$index]}" "$(sha256_of "$DEST/${FILES[$index]}")" "$separator"
      done
      printf '  }\n}\n'
    } > "$LOCK"
    echo "Pinned contracts $version"
    ;;
  verify)
    [ -f "$LOCK" ] || { echo "missing $LOCK" >&2; exit 1; }
    status=0
    for file in "${FILES[@]}"; do
      expected="$(grep "\"$file\"" "$LOCK" | sed -E 's/.*: "([a-f0-9]{64})".*/\1/')"
      actual="$(sha256_of "$DEST/$file")"
      if [ "$expected" != "$actual" ]; then
        echo "contracts/$file does not match contracts.lock.json (edit the source in Verdale-core-service and re-run sync)" >&2
        status=1
      fi
    done
    [ "$status" -eq 0 ] && echo "Contracts $(tr -d '[:space:]' < "$DEST/VERSION") verified"
    exit "$status"
    ;;
  *)
    echo "usage: contracts.sh sync <dir> | verify" >&2
    exit 2
    ;;
esac
