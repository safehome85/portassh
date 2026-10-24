# Конфигурация

Вся конфигурация запекается в exe при сборке через `baked.go` — целевая машина
не получает ни одного файла настроек.

## Быстрый старт

```sh
./scripts/gen-baked.sh relay.example.com   # генерирует ключи и baked.go
./build.sh                                  # собирает portassh.exe
```

Скрипт печатает строку для `authorized_keys` на relay — добавьте её один раз.

## Параметры baked.go

| Константа | Назначение | Примечание |
|---|---|---|
| `defaultRelay` | Адрес relay-сервера `host[:22]` | Любой SSH-сервер с `AllowTcpForwarding` |
| `defaultRelayUser` | Пользователь SSH на relay | Shell можно ограничить до `/bin/sh` — нужны только TCP-форварды |
| `defaultRPort` | Порт на relay, который пробрасывается на цель (2200) | Занят — берётся следующий (до +99) |
| `defaultLPort` | Локальный порт встроенного SSH-сервера (2222) | Только 127.0.0.1 |
| `relayPrivKeyB64` | Приватный ed25519-ключ туннеля, base64 | Публичная часть — в `authorized_keys` relay |
| `relayHostKeyLine` | Host key relay в формате known_hosts | Фиксируется при сборке: подмена relay обнаруживается сразу |
| `serverHostKeyB64` | Приватный host key встроенного SSH-сервера | Одинаковый у всех сборок — клиенты не видят предупреждений |
| `serverHostPub` | Публичная часть host key (для known_hosts клиентов) | |
| `allowedClientPubKey` | Единственный клиентский ключ, принимаемый сервером | Публичная часть ключа машины, с которой подключаетесь |

Генерация ключей вручную:

```sh
ssh-keygen -t ed25519 -N '' -f relay_ed25519 -C portassh-relay
base64 < relay_ed25519          # relayPrivKeyB64
```

Или используйте `./scripts/gen-baked.sh` — сгенерирует всё сам.

## Флаги запуска

| Флаг | Значение по умолчанию | Описание |
|---|---|---|
| *(нет)* | — | Обычный режим: туннель на порт 2200, пользовательский shell |
| `-admin` | — | Админ-экземпляр: окно UAC, туннель на порт 2201, shell с правами администратора |
| `-elevated` | — | Внутренний: маркер уже повышенного экземпляра |
| `-relay host[:22]` | из baked.go | Другой relay-сервер |
| `-rport N` | 2200 (admin: 2201) | Порт на relay; занят — берётся следующий |
| `-lport N` | 2222 (admin: 2223) | Локальный порт SSH-сервера (только loopback) |
| `-shell имя` | `cmd.exe` / `$SHELL` | Shell для сессий (например `powershell.exe`) |
| `-keydir путь` | `%LOCALAPPDATA%\portassh` | Каталог состояния |

Файлы рядом с exe (создаются автоматически):

| Файл | Содержимое |
|---|---|
| `portassh.log` | Статус туннеля, ошибки, стек паник |
| `portassh_connection.txt` | Готовая команда подключения |
| `portassh_connection_admin.txt` | Команда подключения админ-экземпляра |

## Псевдонимы на клиенте (по желанию)

```sh
alias pconnect='ssh -o StrictHostKeyChecking=accept-new -J root@<relay> -p 2200 anyuser@localhost'
alias pconnect-admin='ssh -o StrictHostKeyChecking=accept-new -J root@<relay> -p 2201 anyuser@localhost'
```
