#!/usr/bin/env bash
# Downloads the WASI build of CPython that the Python sandbox tool runs on,
# and checks it against a pinned hash.
set -euo pipefail

url="https://github.com/vmware-labs/webassembly-language-runtimes/releases/download/python%2F3.12.0%2B20231211-040d5a6/python-3.12.0.wasm"
sha256="e5dc5a398b07b54ea8fdb503bf68fb583d533f10ec3f930963e02b9505f7a763"
dest="$(dirname "$0")/../apps/api/third_party/python/python-3.12.0.wasm"

if [ -f "$dest" ] && echo "$sha256  $dest" | sha256sum -c --status; then
	echo "already present: $dest"
	exit 0
fi
mkdir -p "$(dirname "$dest")"
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
curl -fL --retry 3 -o "$tmp" "$url"
echo "$sha256  $tmp" | sha256sum -c --status || { echo "checksum mismatch" >&2; exit 1; }
mv "$tmp" "$dest"
echo "saved $dest"
