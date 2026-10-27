#!/bin/sh
# portassh relay wrapper: туннельный ключ даёт ТОЛЬКО эти команды.
# SSH_ORIGINAL_COMMAND содержит запрошенное; всё вне белого списка — отказ.
REG=/home/portassh/online.txt
C="$SSH_ORIGINAL_COMMAND"

# 1) чтение артефактов (self-update, force-флаг)
case "$C" in
  "cat /home/portassh/updates/version.txt"|\
  "cat /home/portassh/updates/portassh.exe"|\
  "cat /home/portassh/force-flag.txt"|\
  "date +%s")
    exec sh -c "$C"
    ;;
esac

# 2) регистрация машины (fallback, если hbd недоступен) — оба варианта пути
case "$C" in
  "umask 022 && echo '"*"' >> /home/portassh/online.txt"|\
  "umask 022 && echo '"*"' >> ~/online.txt")
    line=$(printf '%s' "$C" | sed -e "s/^umask 022 && echo '//" -e "s/' >> .*online.txt\$//")
    set -- $line
    [ $# -ge 4 ] || { echo "portassh: bad registry line" >&2; exit 111; }
    case "$4" in
      user|admin) printf '%s\n' "$line" >> "$REG"; exit 0 ;;
      *) echo "portassh: bad role" >&2; exit 111 ;;
    esac
    ;;
  # graceful shutdown: удаление своих строк реестра (имя из sanitizeLine)
  sed\ -i\ *online.txt)
    H=$(printf '%s' "$C" | sed -n "s|^sed -i '/^\([^ ]*\) /d'.*|\1|p")
    case $H in
      *[!A-Za-z0-9._-]*|"") echo "portassh: bad hostname" >&2; exit 111 ;;
    esac
    exec sh -c "sed -i '/^$H /d' /home/portassh/online.txt"
    ;;
esac

# 3) reclaim ghost-порта: ss по портам 2200-2299 / 12200-12999
case "$C" in
  ss\ -tlnp\ *)
    PORT=$(printf '%s' "$C" | sed -n "s/.*grep ':\([0-9]*\) '.*/\1/p")
    case $PORT in
      220[0-9]|220[0-9]|22[0-9][0-9]|12[2-9][0-9][0-9]) exec sh -c "$C" ;;
      *) echo "portassh: port out of range" >&2; exit 111 ;;
    esac
    ;;
  kill\ [0-9]*)
    PID=${C#kill }
    [ "$(ps -o user= -p "$PID" 2>/dev/null | tr -d ' ')" = "portassh" ] || { echo "portassh: not your process" >&2; exit 111; }
    exec kill "$PID"
    ;;
  *)
    echo "portassh: command not allowed" >&2
    exit 111
    ;;
esac
