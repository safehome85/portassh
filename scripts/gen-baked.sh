#!/bin/sh
# Генерирует baked.go со свежими ключами ed25519 для указанного relay-сервера.
#
# Использование:
#   ./scripts/gen-baked.sh <relay-host> [--force]
#
# Скрипт печатает строку для authorized_keys на relay и ключ клиента,
# который принимает встроенный SSH-сервер.
set -e
cd "$(dirname "$0")/.."

RELAY_HOST="${1:?укажите адрес relay: ./scripts/gen-baked.sh relay.example.com}"
FORCE="${2:-}"

if [ -f "baked.go" ] && [ "$FORCE" != "--force" ]; then
  echo "ОШИБКА: baked.go уже существует." >&2
  echo "Пересоздать с новыми ключами (старые перестанут работать): $0 $1 --force" >&2
  exit 1
fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

ssh-keygen -t ed25519 -N '' -f "$TMP/relay" -C portassh-relay -q
ssh-keygen -t ed25519 -N '' -f "$TMP/host"  -C portassh-host  -q

RPUB_B64=$(base64 < "$TMP/relay" | tr -d '\n')
HPUB_B64=$(base64 < "$TMP/host" | tr -d '\n')
HPUB_LINE=$(cat "$TMP/host.pub")

# host key relay: реальный (если relay доступен) или заполнитель для тестов
HKLINE=$(ssh-keyscan -t ed25519 "$RELAY_HOST" 2>/dev/null | grep -v '^#' | head -1)
if [ -z "$HKLINE" ]; then
  echo "ПРЕДУПРЕЖДЕНИЕ: не удалось снять host key с $RELAY_HOST — вписан заполнитель." >&2
  echo "Перед реальным использованием замените relayHostKeyLine на вывод:" >&2
  echo "  ssh-keyscan -t ed25519 $RELAY_HOST" >&2
  HKLINE="$RELAY_HOST ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIВСТАВЬТЕ_РЕАЛЬНЫЙ_КЛЮЧ"
fi

# клиентский ключ: существующий ssh-ключ или свежесгенерированный
CPUB=$(cat "$HOME/.ssh/id_ed25519.pub" 2>/dev/null || cat "$HOME/.ssh/id_rsa.pub" 2>/dev/null || true)
if [ -z "$CPUB" ]; then
  ssh-keygen -t ed25519 -N '' -f "$TMP/client" -C portassh-client -q
  CPUB=$(cat "$TMP/client.pub")
  echo "ПРЕДУПРЕЖДЕНИЕ: ~/.ssh ключей не нашлось — сгенерирован $TMP/client (сохраните его!)" >&2
fi

cat > baked.go <<EOF
package main

// Сгенерировано scripts/gen-baked.sh $RELAY_HOST $(date +%Y-%m-%d).
// Приватные ключи не должны попадать в репозиторий (baked.go в .gitignore).

const (
	defaultRelay     = "$RELAY_HOST"
	defaultRelayUser = "portassh"
	defaultLPort     = 2222
	defaultRPort     = 2200

	relayPrivKeyB64 = "$RPUB_B64"

	relayHostKeyLine = "$HKLINE"

	serverHostKeyB64 = "$HPUB_B64"
	serverHostPub    = "$HPUB_LINE"

	allowedClientPubKey = "$CPUB"
)
EOF

echo "baked.go создан (ключи ed25519)."
echo
echo "=== ОДНОКРАТНАЯ НАСТРОЙКА RELAY ($RELAY_HOST) ==="
echo "1) Создайте пользователя туннеля и добавьте ключ:"
echo "   useradd -m -s /bin/sh portassh"
echo "   mkdir -p /home/portassh/.ssh && chmod 700 /home/portassh/.ssh"
echo "   echo '$(cat "$TMP/relay.pub")' > /home/portassh/.ssh/authorized_keys"
echo "   chown -R portassh:portassh /home/portassh/.ssh"
echo "2) Host key relay вшит в сборку: $HKLINE"
echo "3) Подключаться можно только с машины, чей ключ:"
echo "   $CPUB"
echo
echo "Сборка: ./build.sh"
