#!/bin/sh
# Кросс-компиляция portassh для Windows из macOS/Linux.
# Один универсальный файл: 386 запускается на Win10/11 x64 (WOW64), x86 и ARM64 (эмуляция x86).
# -H=windowsgui: без консольного окна; статус пишется в portassh.log рядом с exe.
set -e
cd "$(dirname "$0")"

if [ ! -f "baked.go" ]; then
  echo "ОШИБКА: нет baked.go — скопируйте baked.go.example и заполните своими ключами." >&2
  echo "  cp baked.go.example baked.go  (затем отредактируйте)" >&2
  exit 1
fi

GOOS=windows GOARCH=386 go build -trimpath -ldflags "-s -w -H=windowsgui" -o portassh.exe .

# Условная подпись: положите рядом sign.pfx (и установите osslsigncode),
# задайте SIGN_PASS — и сборка подпишется автоматически. Пока файла нет, шаг пропускается.
if [ -f "sign.pfx" ] && command -v osslsigncode >/dev/null 2>&1; then
  osslsigncode sign -pkcs12 "sign.pfx" -pass "${SIGN_PASS:?SIGN_PASS не задан}" \
    -n "portassh" -i "https://example.com" -in portassh.exe -out portassh.exe.signed \
    && mv portassh.exe.signed portassh.exe && echo "подписано: portassh.exe"
else
  echo "skip: подпись (нет sign.pfx или osslsigncode)"
fi

ls -lh portassh.exe
