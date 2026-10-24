#!/bin/sh
# Cross-compile portassh for Windows from macOS/Linux.
# One universal file: 386 runs on Win10/11 x64 (WOW64), x86 and ARM64 (x86 emulation).
# -H=windowsgui: no console window; status is written to portassh.log next to the exe.
set -e
cd "$(dirname "$0")"

if [ ! -f "baked.go" ]; then
  echo "ERROR: baked.go not found — copy baked.go.example and fill in your keys." >&2
  echo "  cp baked.go.example baked.go  (then edit it)" >&2
  exit 1
fi

GOOS=windows GOARCH=386 go build -trimpath -ldflags "-s -w -H=windowsgui" -o portassh.exe .

# Optional code signing: place sign.pfx next to the script (and install
# osslsigncode), set SIGN_PASS — the build signs itself. Without the file the step is skipped.
if [ -f "sign.pfx" ] && command -v osslsigncode >/dev/null 2>&1; then
  osslsigncode sign -pkcs12 "sign.pfx" -pass "${SIGN_PASS:?SIGN_PASS is not set}" \
    -n "portassh" -i "https://example.com" -in portassh.exe -out portassh.exe.signed \
    && mv portassh.exe.signed portassh.exe && echo "signed: portassh.exe"
else
  echo "skip: signing (no sign.pfx or osslsigncode)"
fi

ls -lh portassh.exe
