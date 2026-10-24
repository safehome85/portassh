<div align="center">

# portassh

**Удалённый SSH-доступ к Windows-машинам без прав администратора, установки и настройки — один exe, запустил и подключился**

[![CI](https://github.com/safehome85/portassh/actions/workflows/ci.yml/badge.svg)](https://github.com/safehome85/portassh/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/Go-1.21%2B-00ADD8?logo=go&logoColor=white)
![Platform](https://img.shields.io/badge/Windows-10%2F11%20x64%20%7C%20x86%20%7C%20ARM64-0078D4?logo=windows&logoColor=white)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

*Удалённая помощь пользователям, администрирование парка, доступ к своей машине из любого места*

</div>

---

## Сценарий

Нужен терминал на Windows-машине, которая:
- за NAT или чужим файрволом (нет прямого доступа),
- принадлежит пользователю **без прав администратора**,
- на которой **нельзя ничего устанавливать**,
- а антивирус недоверчив к незнакомым программам.

Решение: один переносной exe. Запустили — он сам поднял SSH-сервер на loopback
и сам пробросил туннель через ваш relay-сервер. С вашего компьютера:

```sh
ssh -o StrictHostKeyChecking=accept-new -J root@<relay> -p 2200 anyuser@localhost
```

Ноль паролей, ноль файлов конфигурации на целевой машине, ни одного вопроса
от файрвола.

## Как это устроено

```
 ваш ПК                                relay (VPS, любой SSH)              целевая Windows
┌─────────┐   ssh -J   ┌─────────────────────────┐   reverse-туннель   ┌──────────────────────┐
│ клиент  ├───────────►│  sshd :22               │◄────SSH-сессия──────┤ portassh.exe         │
│         │  direct-   │  127.0.0.1:2200 ────────┼─────────────────────┤ • sshd  127.0.0.1:2222│
└─────────┘  tcpip     │  (проброс порта)        │                     │ • cmd.exe через ConPTY│
                       └─────────────────────────┘                     └──────────────────────┘
```

- На цели exe поднимает **встроенный SSH-сервер** (shell / exec / SFTP)
  только на loopback — файрвол не спрашивает, прав администратора не нужно
- Параллельно открывает **reverse-туннель** к relay по зашитому ключу —
  никакого входящего доступа к цели не требуется
- Все ключи и адреса запекаются в exe при сборке (`baked.go`)

## Возможности

| | |
|---|---|
| 🖥 **Интерактивный шелл** | cmd.exe / powershell через ConPTY ([go-pty]), полный терминал с resize |
| ⚡ **Exec** | сырая строка клиента доходит до cmd 1:1 — кавычки, пайпы, слэши без порчи |
| 📁 **SFTP** | `scp`/`sftp` файлы через тот же туннель |
| 🈳 **UTF-8** | вывод CP866 декодируется на лету, без спавна chcp |
| 🔁 **Живучесть** | auto-reconnect (backoff 5→30 с), retry spawn при блокировке антивирусом, изоляция паник |
| 🖥 **Мульти-машина** | реестр на relay, автоподбор портов, до 100 машин одновременно |
| 🔐 **Админ-режим** | `portassh.exe -admin` — UAC, отдельный туннель, shell от администратора |
| 🤫 **Скрытность** | окно не появляется, статус в `portassh.log`; консоли детей не мигают |
| 📦 **Один файл** | 386-сборка = Win10/11 x64 (WOW64) + x86 + ARM64 (эмуляция) |

## Быстрый старт

**1. Relay** — любой SSH-сервер (VPS). Пользователю туннеля нужны только
TCP-форварды:

```sh
useradd -m -s /bin/sh portassh
mkdir -p /home/portassh/.ssh && chmod 700 /home/portassh/.ssh
```

**2. Ключи и сборка** (нужен Go 1.21+ и `ssh-keygen`):

```sh
./scripts/gen-baked.sh relay.example.com   # ключи ed25519 + baked.go
./build.sh                                 # portassh.exe
```

Скрипт напечатает строку для `authorized_keys` на relay — добавьте её:

```sh
echo "<relay_ed25519.pub>" > /home/portassh/.ssh/authorized_keys
```

**3. Целевая машина:** скопируйте `portassh.exe`, запустите двойным кликом.
Готово — подключайтесь командой из шага 1 или из `portassh_connection.txt`
(файл появится рядом с exe).

> Полная инструкция: [docs/configuration.md](docs/configuration.md)

## Административный доступ

```sh
portassh.exe -admin        # на целевой машине: одно окно UAC
pconnect-admin             # с вашего ПК: shell с правами администратора
```

Админ-экземпляр держит собственный туннель (порт 2201) и не мешает обычному.
Подробнее — [docs/configuration.md](docs/configuration.md#параметры-bakedgo).

## Несколько машин

Каждый экземпляр регистрируется на relay: `имя_ПК порт время роль`.
Порт подбирается автоматически (2200, 2201, … до 100 машин).

```sh
pconnect-list              # все машины онлайн
pconnectgo DESKTOP-ABC123  # подключение к конкретной по имени
```

[docs/configuration.md](docs/configuration.md) •
[docs/troubleshooting.md](docs/troubleshooting.md)

## Безопасность

Модель доверия простая и честная:

- **exe = ключ.** Приватный ключ туннеля запечён в бинарник: держатель exe
  может открывать туннели через ваш relay. Shell на relay при этом недоступен
  (пользователь туннеля ограничен). Не публикуйте собранные бинарники
- **Один клиентский ключ**: встроенный сервер принимает подключение только
  с машины, чей публичный ключ зашит при сборке
- **Loopback + фиксированный host key**: сервер слушает только 127.0.0.1;
  подмена relay обнаруживается на этапе handshake
- Сообщения об уязвимостях — [SECURITY.md](SECURITY.md)

## Антивирусы

Неподписанный exe, запускающий процессы и открывающий туннели, привлекает
эвристики (проверено на Kaspersky). portassh минимизирует раздражители
(манифест `asInvoker`, отсутствие лишних спавнов, retry при блокировках),
но полное решение — «Доверенные приложения» в антивирусе или код-подпись.
Пошагово: [docs/troubleshooting.md](docs/troubleshooting.md#forkexec-cmdexe-access-is-denied-плавающий).

## Устранение неполадок

Всё, что сломалось за месяцы эксплуатации, и способы лечения —
в [docs/troubleshooting.md](docs/troubleshooting.md): плавающий
`Access is denied`, призрачные порты на relay, кириллица, лимит длины команды,
диагностика со стороны relay.

## Структура репозитория

```
├── main.go              # сервер, туннель, exec — ядро
├── oem_windows.go       # CP866→UTF-8 и SysProcAttr.CmdLine (Windows)
├── elevate_windows.go   # -admin: UAC через ShellExecuteW
├── baked.go.example     # шаблон конфигурации (ключи/relay)
├── scripts/gen-baked.sh # генерация ключей и baked.go
├── build.sh             # сборка + условная код-подпись
├── docs/                # конфигурация и устранение неполадок
└── .github/             # CI, шаблоны issues/PR, dependabot
```

## Разработка

```sh
go vet ./... && go test ./...   # e2e: реальный PTY через встроенный сервер
```

CI прогоняет тесты на Ubuntu и macOS, собирает Windows-артефакт и проверяет
кросс-компиляцию amd64/386/arm64. Сборка в CI использует сгенерированные
тестовые ключи — настоящие ключи в CI не нужны и не должны попадать.

## Благодарности

- [gliderlabs/ssh](https://github.com/gliderlabs/ssh) — SSH-сервер
- [go-pty](https://github.com/aymanbagabas/go-pty) — ConPTY/PTY (Windows-поддержка впервые появилась здесь, у creack/pty её нет)
- [pkg/sftp](https://github.com/pkg/sftp) — SFTP
- [golang.org/x/crypto](https://pkg.go.dev/golang.org/x/crypto/ssh) — SSH-клиент туннеля

## Лицензия

[MIT](LICENSE)

[go-pty]: https://github.com/aymanbagabas/go-pty
