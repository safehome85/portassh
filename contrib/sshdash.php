<?php
/**
 * portassh — АКТИВНЫЕ клиентские машины (live).
 *
 * Активной считается машина, чей экземпляр portassh подтверждал туннель
 * в последние 3 минуты (heartbeat раз в минуту). Реестр читается из
 * приватной папки сайта, куда relay-хост копирует его cron'ом каждую минуту:
 *   * * * * * tail -n 2000 /home/portassh/online.txt 2>/dev/null > <этот файл>
 *
 * Секретный адрес — не публикуйте ссылку.
 */
$PRIVATE     = '/home/portassh/private'; // каталог артефактов (реестр, сборки, ключ)
$RELAY       = 'YOUR_RELAY_IP';  // IP/домен relay-сервера для колонки «Команда подключения»
$WSS_HOST    = 'YOUR_DOMAIN:8899'; // адрес моста wsterm (кнопка «Терминал»)
$ZIP_PASS    = 'YOUR_ZIP_PASSWORD'; // пароль portassh.zip и резервный SSH-пароль

// локальный конфиг живёт только на relay (вне репо, как baked.go для Go):
// переопределяет $PRIVATE, $RELAY, $WSS_HOST, $ZIP_PASS реальными значениями.
// ВАЖНО: подключать ДО производных путей ниже ($REGISTRY, $ALIASES_F, ...)
if (is_file(__DIR__ . '/sshdash-local.php')) { require __DIR__ . '/sshdash-local.php'; }

$REGISTRY    = is_readable('/home/portassh/online.txt') ? '/home/portassh/online.txt' : "$PRIVATE/portassh-online.txt";
$bcmdSaved   = '';
$broadcastHtml = '';
$ALIASES_F   = "$PRIVATE/portassh-aliases.json";
$PBF         = "$PRIVATE/playbooks.json";
$PB_DEFAULTS = [
    ['⚡ Экспресс-диагностика (диски, ОЗУ, службы)', 'sys:diag'],
    ['💾 Место на дисках', 'sys:disks'],
    ['📊 Процессы с памятью', 'sys:procs'],
    ['🛠 Службы Windows', 'sys:svcs'],
    ['🚀 Автозагрузка (Run / Startup)', 'sys:autoruns'],
    ['⚠️ Ошибки журналов (EventLog)', 'sys:events System 10'],
    ['👤 Пользователь и машина', 'sys:whoami'],
    ['🧹 Очистить временные файлы', 'del /q /s %temp%\* && echo Очищено'],
    ['🌐 Сбросить DNS-кэш', 'ipconfig /flushdns'],
    ['🌐 Сброс сетевого стека (Wi-Fi перенастроить!)', 'netsh winsock reset && ipconfig /flushdns'],
    ['🖨 Перезапустить очередь печати', 'net stop Spooler && del /q /f %systemroot%\System32\Spool\Printers\* && net start Spooler'],
    ['🔍 Проверка системных файлов (в фоне, 10-20 мин)', 'start /min cmd /c "sfc /scannow > %temp%\p_sfc.log 2>&1"'],
    ['🧩 Восстановление хранилища DISM (в фоне, долго)', 'start /min cmd /c "DISM /Online /Cleanup-Image /RestoreHealth > %temp%\p_dism.log 2>&1"'],
    ['🌐 Проверить интернет (ping 8.8.8.8)', 'ping 8.8.8.8 -n 4'],
    ['📡 Обновить групповые политики', 'gpupdate /force'],
    ['🖥 Перезапустить проводник', 'taskkill /f /im explorer.exe & start explorer.exe'],
    ['📜 Информация о Windows', 'systeminfo | findstr /B /C:"OS" /C:"System Boot"'],
    ['⏻ Перезагрузка (5 с, принудительно)', 'shutdown /r /t 5 /f'],
    ['⏻ Выключение (5 с, принудительно)', 'shutdown /s /t 5 /f'],
    ['🔒 Заблокировать экран', 'rundll32.exe user32.dll,LockWorkStation'],
];
$LATEST_RAW  = trim((string) @file_get_contents("$PRIVATE/portassh-version.txt"));
$LATEST_VER  = preg_split('/\s+/', $LATEST_RAW)[0] ?? '';
$LATEST_NAME = preg_split('/\s+/', $LATEST_RAW)[1] ?? '';
$EXE         = "$PRIVATE/portassh.exe"; // сборка для скачивания (копируется cron'ом)
$ACTIVE_SECS = 180;           // heartbeat у exe раз в 60 с — запас ×3
$MAX_AGE     = 7 * 86400;     // записи старше недели игнорируются вовсе

// PHP-TZ может отличаться от системного (машине пишут локальное время) —
// калибруем mktime-эпохи по системным `date +%s` / `date +%H:%M`
$sysNow = time(); // без shell_exec: он может быть в disable_functions
$SKEW = $sysNow - mktime((int)date('H'), (int)date('i'), 0); // смещение TZ PHP vs системное

header('Content-Type: text/html; charset=utf-8');
header('Cache-Control: no-store');

header('Content-Type: text/html; charset=utf-8');
header('Cache-Control: no-store');

// CSRF: basic-auth не защищает от чужих страниц — мутирующие действия
// принимаются только POST'ом с токеном, совпадающим с cookie (SameSite=Lax)
$CSRF = $_COOKIE['pa_csrf'] ?? '';
$isHttps = (!empty($_SERVER['HTTPS']) && $_SERVER['HTTPS'] !== 'off')
    || (isset($_SERVER['SERVER_PORT']) && $_SERVER['SERVER_PORT'] == 443)
    || (isset($_SERVER['HTTP_X_FORWARDED_PROTO']) && $_SERVER['HTTP_X_FORWARDED_PROTO'] === 'https');
if (!preg_match('/^[0-9a-f]{32}$/', $CSRF)) {
    $CSRF = bin2hex(random_bytes(16));
    setcookie('pa_csrf', $CSRF, ['expires' => 0, 'path' => '/', 'secure' => $isHttps, 'httponly' => true, 'samesite' => 'Lax']);
}
function csrf_guard(): void {
    if (($_SERVER['REQUEST_METHOD'] ?? '') !== 'POST') {
        http_response_code(405);
        exit('Метод не поддерживается (ожидается POST)');
    }
    $sent = (string) ($_POST['t'] ?? ($_SERVER['HTTP_X_CSRF'] ?? ''));
    $cookie = (string) ($_COOKIE['pa_csrf'] ?? '');
    if ($cookie !== '') {
        if (!hash_equals($cookie, $sent)) {
            http_response_code(403);
            exit('CSRF: несовпадение токена сессии');
        }
    } else {
        // Если кука не дошла (например, строгие настройки приватности или HTTP без SSL), проверяем формат токена
        if (!preg_match('/^[0-9a-f]{32}$/', $sent)) {
            http_response_code(403);
            exit('CSRF: отсутствует проверочный токен');
        }
    }
}

// метки и заметки машин: JSON рядом с реестром, редактируется из интерфейса
if (isset($_REQUEST['setalias'])) {
    csrf_guard();
    $h = preg_replace('/[^A-Za-z0-9._-]/', '', (string) ($_REQUEST['setalias'] ?? ''));
    if ($h !== '') {
        $name = mb_substr(trim((string) ($_REQUEST['name'] ?? '')), 0, 60);
        $note = mb_substr(trim((string) ($_REQUEST['note'] ?? '')), 0, 200);
        $al = json_decode((string) @file_get_contents($ALIASES_F), true);
        if (!is_array($al)) {
            $al = json_decode((string) @file_get_contents(__DIR__ . '/portassh-aliases.json'), true);
        }
        if (!is_array($al)) { $al = []; }
        if ($name === '' && $note === '') {
            unset($al[$h]);
        } else {
            $al[$h] = ['name' => $name, 'note' => $note];
        }
        $payload = json_encode($al, JSON_UNESCAPED_UNICODE | JSON_PRETTY_PRINT);
        $ok = @file_put_contents($ALIASES_F, $payload, LOCK_EX);
        if ($ok === false) {
            $fallbackFile = __DIR__ . '/portassh-aliases.json';
            $ok = @file_put_contents($fallbackFile, $payload, LOCK_EX);
            if ($ok === false) {
                http_response_code(500);
                exit('Ошибка записи в ' . htmlspecialchars($ALIASES_F) . ' (проверьте права веб-сервера на запись)');
            }
        }
    }
    if (!empty($_SERVER['HTTP_X_CSRF']) || !empty($_POST['t'])) {
        header('Content-Type: application/json');
        echo json_encode(['ok' => true]);
        exit;
    }
    header('Location: ' . strtok($_SERVER['REQUEST_URI'] ?? '/sshdash', '?'));
    exit;
}

// Быстрые сценарии: JSON на VPS, сид при первом запуске
$playbooks = json_decode((string) @file_get_contents($PBF), true);
if (!is_array($playbooks) || count($playbooks) === 0) {
    $playbooks = array_map(fn($p) => ['n' => $p[0], 'c' => $p[1]], $PB_DEFAULTS);
    file_put_contents($PBF, json_encode($playbooks, JSON_UNESCAPED_UNICODE | JSON_PRETTY_PRINT), LOCK_EX);
}
if (isset($_REQUEST['addpb'])) {
    csrf_guard();
    $n = mb_substr(trim((string) ($_REQUEST['pbname'] ?? '')), 0, 80);
    $c = trim((string) ($_REQUEST['pbcmd'] ?? ''));
    if ($n !== '' && $c !== '') {
        $playbooks[] = ['n' => $n, 'c' => $c];
        file_put_contents($PBF, json_encode($playbooks, JSON_UNESCAPED_UNICODE | JSON_PRETTY_PRINT), LOCK_EX);
    }
    if (!empty($_SERVER['HTTP_X_CSRF']) || !empty($_POST['t'])) {
        header('Content-Type: application/json');
        echo json_encode(['ok' => true]);
        exit;
    }
    header('Location: ' . strtok($_SERVER['REQUEST_URI'] ?? '/sshdash', '?'));
    exit;
}
if (isset($_REQUEST['delpb'])) {
    csrf_guard();
    $n = trim((string) ($_REQUEST['pbname'] ?? ''));
    $playbooks = array_values(array_filter($playbooks, fn($p) => ($p['n'] ?? '') !== $n));
    file_put_contents($PBF, json_encode($playbooks, JSON_UNESCAPED_UNICODE | JSON_PRETTY_PRINT), LOCK_EX);
    if (!empty($_SERVER['HTTP_X_CSRF']) || !empty($_POST['t'])) {
        header('Content-Type: application/json');
        echo json_encode(['ok' => true]);
        exit;
    }
    header('Location: ' . strtok($_SERVER['REQUEST_URI'] ?? '/sshdash', '?'));
    exit;
}

// SSE (Server-Sent Events) мост для живых обновлений состояния без перезагрузки страниц
if (isset($_GET['sse'])) {
    header('Content-Type: text/event-stream');
    header('Cache-Control: no-cache');
    header('Connection: keep-alive');
    header('X-Accel-Buffering: no');
    @ini_set('output_buffering', 'off');
    @ini_set('zlib.output_compression', false);
    while (ob_get_level()) { ob_end_flush(); }
    flush();

    $fp = @fsockopen('127.0.0.1', 8897, $errno, $errstr, 2);
    if (!$fp) {
        echo "event: error\ndata: hbd offline\n\n";
        exit;
    }
    fwrite($fp, "GET /api/events HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n");
    while (!feof($fp)) {
        $line = fgets($fp);
        if (trim($line) === '') break;
    }
    while (!feof($fp)) {
        $chunk = fread($fp, 2048);
        if ($chunk === '' || $chunk === false) break;
        echo $chunk;
        flush();
    }
    fclose($fp);
    exit;
}

// RDP: готовый файл подключения (адрес localhost:порт+10000 — туннель
// строится командой ssh -L, см. инструкцию ниже)
if (isset($_GET['rdp'])) {
    $p = (int) $_GET['rdp'];
    if ($p < 1024) { http_response_code(404); exit('bad port'); }
    $rdpRelay = $p + 10000;
    header('Content-Type: application/rdp');
    header('Content-Disposition: attachment; filename="portassh_rdp_' . $p . '.rdp"');
    echo "full address:s=localhost:{$rdpRelay}
screen mode id:i=1
desktopwidth:i=1280
desktopheight:i=720
authentication level:i=0
";
    exit;
}

// Массовый exec: параллельный опрос выбранных машин через их форвардинги
if (isset($_REQUEST['broadcast'])) {
    csrf_guard();
    set_time_limit(120);
        $bcmd = (string) ($_REQUEST['bcmd'] ?? '');
    $bports = array_values(array_filter(array_map('intval', explode(',', (string) ($_REQUEST['bports'] ?? ''))),
        fn($p) => $p >= 1024 && $p <= 65535));
    $bcmdSaved = $bcmd;
    if ($bcmd !== '' && $bports) {
        // exec выполняет hbd (Go) на relay: параллельно, без proc_open
        $url = 'http://127.0.0.1:8897/broadcast?ports=' . urlencode(implode(',', $bports))
            . '&cmd=' . urlencode($bcmd);
        $raw = @file_get_contents($url, false, stream_context_create(['http' => ['timeout' => 25]]));
        $decoded = $raw ? json_decode($raw, true) : null;
        $results = [];
        foreach ($bports as $p) {
            $r = isset($decoded[$p]) && is_array($decoded[$p]) ? $decoded[$p] : null;
            $results[$p] = $r ? ['code' => (int) $r['code'], 'out' => (string) $r['out']]
                              : ['code' => -1, 'out' => 'нет ответа от демона'];
        }
    } else {
        $broadcastHtml = '';
    }
}

// Экран: последний кадр из hbd
if (isset($_GET['screen'])) {
    $p = (int) $_GET['screen'];
    if ($p < 1024) { http_response_code(404); exit; }
    $raw = @file_get_contents('http://127.0.0.1:8897/screen?port=' . $p,
        false, stream_context_create(['http' => ['timeout' => 10]]));
    if ($raw === false) { http_response_code(404); exit('no frame'); }
    header('Content-Type: image/jpeg');
    echo $raw;
    exit;
}

// Ввод на машине: клики и текст (только POST — это внедрение ввода)
if (isset($_GET['input'])) {
    csrf_guard();
    $p = (int) $_GET['input'];
    $line = (string) ($_GET['line'] ?? '');
    if ($p < 1024 || $line === '' || mb_strlen($line) > 4096) { http_response_code(404); exit; }
    header('Content-Type: application/json');
    echo @file_get_contents('http://127.0.0.1:8897/input?port=' . $p,
        false, stream_context_create(['http' => ['method' => 'POST', 'timeout' => 10, 'content' => $line]]));
    exit;
}

// Файлы машины: листинг домашней папки
if (isset($_GET['ls'])) {
    $p = (int) $_GET['ls'];
    if ($p < 1024) { http_response_code(404); exit; }
    header('Content-Type: application/json');
    echo @file_get_contents('http://127.0.0.1:8897/ls?port=' . $p,
        false, stream_context_create(['http' => ['timeout' => 20]]));
    exit;
}

// Файлы: загрузка НА машину (multipart, поле f) → Downloads
if (isset($_GET['putfile'])) {
    csrf_guard();
    $p = (int) $_GET['putfile'];
    if ($p < 1024 || !isset($_FILES['f']) || $_FILES['f']['error'] !== UPLOAD_ERR_OK) { http_response_code(400); exit('no file'); }
    $name = basename($_FILES['f']['name']);
    $content = file_get_contents($_FILES['f']['tmp_name']);
    if ($content === false) { http_response_code(500); exit; }
    $ctx = stream_context_create(['http' => ['method' => 'POST', 'timeout' => 300,
        'header' => "Content-Type: application/octet-stream\r\nContent-Length: " . strlen($content),
        'content' => $content]]);
    $raw = @file_get_contents('http://127.0.0.1:8897/putfile?port=' . $p . '&name=' . urlencode($name), false, $ctx);
    header('Content-Type: application/json');
    echo $raw === false ? '{"error":"upload failed"}' : '{"ok":true}';
    exit;
}

// Файлы: скачивание С машины
if (isset($_GET['getfile'])) {
    $p = (int) $_GET['getfile'];
    $name = preg_replace('/[^A-Za-z0-9._ ()-]/', '', (string) ($_GET['name'] ?? ''));
    if ($p < 1024 || $name === '') { http_response_code(404); exit; }
    $ctx = stream_context_create(['http' => ['timeout' => 300]]);
    $raw = @file_get_contents('http://127.0.0.1:8897/getfile?port=' . $p . '&name=' . urlencode($name), false, $ctx);
    if ($raw === false) { http_response_code(404); exit('not found'); }
    header('Content-Type: application/octet-stream');
    header('Content-Disposition: attachment; filename="' . addslashes($name) . '"');
    echo $raw;
    exit;
}

// AI: конфигурация моделей и ключей
if (isset($_GET['ai_config'])) {
    header('Content-Type: application/json');
    echo @file_get_contents('http://127.0.0.1:8897/ai/config', false, stream_context_create(['http' => ['timeout' => 5]]));
    exit;
}

// AI: сбор событий Windows
if (isset($_GET['events'])) {
    $p = (int) $_GET['events'];
    $ch = urlencode((string)($_GET['ch'] ?? 'System'));
    if ($p < 1024) { http_response_code(404); exit; }
    header('Content-Type: application/json');
    echo @file_get_contents('http://127.0.0.1:8897/events?port=' . $p . '&channel=' . $ch, false, stream_context_create(['http' => ['timeout' => 20]]));
    exit;
}

// Добавить хост в разрешенные (allowed-hosts.txt) через API hbd
if (isset($_GET['allow_host']) && $_GET['allow_host'] !== '') {
    $h = trim((string)$_GET['allow_host']);
    if (preg_match('/^[A-Za-z0-9._-]+$/', $h)) {
        $ctx = stream_context_create(['http' => [
            'method' => 'POST',
            'timeout' => 5,
            'header' => "Content-Type: application/json\r\n",
            'content' => json_encode(['host' => $h]),
        ]]);
        @file_get_contents('http://127.0.0.1:8897/api/allow-host', false, $ctx);
    }
    header('Location: ' . strtok($_SERVER['REQUEST_URI'], '?'));
    exit;
}

// Удалить хост из очереди неразрешенных (rejected-hosts.txt)
if (isset($_GET['del_rejected']) && $_GET['del_rejected'] !== '') {
    $target = trim((string)$_GET['del_rejected']);
    if ($target === 'all') {
        $ctx = stream_context_create(['http' => [
            'method' => 'POST',
            'timeout' => 5,
            'header' => "Content-Type: application/json\r\n",
            'content' => json_encode(['all' => true]),
        ]]);
        @file_get_contents('http://127.0.0.1:8897/api/dismiss-rejected', false, $ctx);
    } elseif (preg_match('/^[A-Za-z0-9._-]+$/', $target)) {
        $ctx = stream_context_create(['http' => [
            'method' => 'POST',
            'timeout' => 5,
            'header' => "Content-Type: application/json\r\n",
            'content' => json_encode(['host' => $target]),
        ]]);
        @file_get_contents('http://127.0.0.1:8897/api/dismiss-rejected', false, $ctx);
    }
    header('Location: ' . strtok($_SERVER['REQUEST_URI'], '?'));
    exit;
}

// AI: анализ журнала событий
if (isset($_GET['ai_explain_events'])) {
    @set_time_limit(180);
    $body = file_get_contents('php://input');
    header('Content-Type: application/json');
    $ctx = stream_context_create(['http' => [
        'method' => 'POST',
        'timeout' => 180,
        // ignore_errors: тело и статус ошибки hbd (429, модель недоступна...)
        // пробрасываются клиенту, а не превращаются в «AI server unreachable»
        'ignore_errors' => true,
        'header' => "Content-Type: application/json\r\nContent-Length: " . strlen($body),
        'content' => $body,
    ]]);
    $raw = @file_get_contents('http://127.0.0.1:8897/ai/explain-events', false, $ctx);
    if ($raw === false) { http_response_code(502); exit(json_encode(['error' => 'AI server unreachable'])); }
    echo $raw;
    exit;
}

// AI: SysAdmin Copilot чат с Tool Calling
if (isset($_GET['ai_chat'])) {
    @set_time_limit(180);
    $body = file_get_contents('php://input');
    header('Content-Type: application/json');
    $ctx = stream_context_create(['http' => [
        'method' => 'POST',
        'timeout' => 180,
        'ignore_errors' => true,
        'header' => "Content-Type: application/json\r\nContent-Length: " . strlen($body),
        'content' => $body,
    ]]);
    $raw = @file_get_contents('http://127.0.0.1:8897/ai/chat', false, $ctx);
    if ($raw === false) { http_response_code(502); exit(json_encode(['error' => 'AI server unreachable'])); }
    echo $raw;
    exit;
}

// Питание: перезагрузка / выключение / блокировка
if (isset($_GET['power'])) {
    csrf_guard();
    $p = (int) $_GET['power'];
    $act = (string) ($_GET['act'] ?? '');
    $cmds = [
        'reboot'   => 'shutdown /r /t 5 /f',
        'shutdown' => 'shutdown /s /t 5 /f',
        'lock'     => 'rundll32.exe user32.dll,LockWorkStation',
    ];
    if ($p < 1024 || !isset($cmds[$act])) { http_response_code(400); exit('bad act'); }
    header('Content-Type: application/json');
    echo @file_get_contents('http://127.0.0.1:8897/broadcast?ports=' . $p . '&cmd=' . urlencode($cmds[$act]),
        false, stream_context_create(['http' => ['timeout' => 20]]));
    exit;
}

// Процессы машины: JSON из hbd (sys:procs)
if (isset($_GET['procs'])) {
    $p = (int) $_GET['procs'];
    if ($p < 1024) { http_response_code(404); exit; }
    header('Content-Type: application/json');
    echo @file_get_contents('http://127.0.0.1:8897/broadcast?ports=' . $p . '&cmd=' . urlencode('sys:procs'),
        false, stream_context_create(['http' => ['timeout' => 25]]));
    exit;
}

// Службы машины: JSON из hbd (sys:svcs)
if (isset($_GET['svcs'])) {
    $p = (int) $_GET['svcs'];
    if ($p < 1024) { http_response_code(404); exit; }
    header('Content-Type: application/json');
    echo @file_get_contents('http://127.0.0.1:8897/broadcast?ports=' . $p . '&cmd=' . urlencode('sys:svcs'),
        false, stream_context_create(['http' => ['timeout' => 25]]));
    exit;
}

// Управление службой: start/stop/restart
if (isset($_GET['svc'])) {
    csrf_guard();
    $p = (int) $_GET['svc'];
    $act = preg_replace('/[^a-z]/', '', (string) ($_GET['act'] ?? ''));
    $name = preg_replace('/[^A-Za-z0-9._$-]/', '', (string) ($_GET['name'] ?? ''));
    if ($p < 1024 || !in_array($act, ['start', 'stop', 'restart'], true) || $name === '') { http_response_code(404); exit; }
    header('Content-Type: application/json');
    echo @file_get_contents('http://127.0.0.1:8897/broadcast?ports=' . $p . '&cmd=' . urlencode('sys:svc-' . $act . ' ' . $name),
        false, stream_context_create(['http' => ['timeout' => 30]]));
    exit;
}

// Снять задачу
if (isset($_GET['killp'])) {
    csrf_guard();
    $p = (int) $_GET['killp'];
    $pid = (int) ($_GET['pid'] ?? 0);
    if ($p < 1024 || $pid <= 0) { http_response_code(404); exit; }
    header('Content-Type: application/json');
    echo @file_get_contents('http://127.0.0.1:8897/broadcast?ports=' . $p . '&cmd=' . urlencode('sys:kill ' . $pid),
        false, stream_context_create(['http' => ['timeout' => 25]]));
    exit;
}

// Инвентарь: снимок подсистемы info цели → кэш private/inv_<порт>.json
if (isset($_GET['inv'])) {
    set_time_limit(60);
    $p = (int) $_GET['inv'];
    if ($p < 1024) { http_response_code(404); exit; }
    $raw = @file_get_contents('http://127.0.0.1:8897/inventory?port=' . $p,
        false, stream_context_create(['http' => ['timeout' => 25]]));
    if ($raw === false) { http_response_code(502); exit('inventory fetch failed'); }
    $decoded = json_decode($raw, true);
    if (!is_array($decoded)) { http_response_code(502); exit('bad json'); }
    $decoded['relayport'] = $p;
    file_put_contents(sprintf('%s/inv_%d.json', $PRIVATE, $p),
        json_encode(['ts' => time(), 'data' => $decoded], JSON_UNESCAPED_UNICODE), LOCK_EX);
    if (isset($_GET['quiet'])) { header('Content-Type: application/json'); echo '{"ok":true}'; exit; }
    header('Location: /sshdash#tab-inv');
    exit;
}

// Теневой сеанс (совместный экран): .bat с туннелем и mstsc /shadow
if (isset($_GET['shadow'])) {
    $p = (int) $_GET['shadow'];
    if ($p < 1024) { http_response_code(404); exit; }
    $rdpRelay = $p + 10000;
    $sid = 1;
    $j = json_decode((string) @file_get_contents("$PRIVATE/inv_{$p}.json"), true);
    if (isset($j['data']['session_id'])) { $sid = (int) $j['data']['session_id']; }
    header('Content-Type: application/octet-stream');
    header('Content-Disposition: attachment; filename="portassh_shadow_' . $p . '.bat"');
    $bat = <<<BAT
@echo off
echo Теневой сеанс: вы видите экран пользователя вместе с ним.
echo Закрытие окна SSH или mstsc завершает просмотр.
start "portassh-tunnel" /min ssh -C -L {$rdpRelay}:127.0.0.1:{$rdpRelay} support@$RELAY
timeout /t 2 >nul
mstsc /shadow:{$sid} /v:localhost:{$rdpRelay} /control
BAT;
    echo $bat;
    exit;

}

// принудительный update-check на клиентах: пишем таймстамп-флаг,
// exe проверяет его при каждом heartbeat (раз в минуту)
if (isset($_REQUEST['force'])) {
    csrf_guard();
    $now = (string) time();
    @file_put_contents($PRIVATE . '/portassh-force.txt', $now);
    @file_put_contents('/home/portassh/force-flag.txt', $now);
    header('Location: ' . ($_SERVER['REQUEST_URI'] ?? '/sshdash'));
    exit;
}

// скачивание сборки (за тем же basic auth):
//   ?dl=zip — portassh.zip (exe + инструкция) — основной вариант
//   ?dl=bin — portassh.bin: браузеры не блокируют переименованный exe,
//             на клиенте переименовать обратно в .exe
//   ?dl=1   — портассh.exe напрямую (может блокироваться браузером)
if (isset($_GET['dl'])) {
    if ($_GET['dl'] === 'zip') {
        $file = "$PRIVATE/portassh.zip";
        $name = 'portassh.zip';
    } elseif ($_GET['dl'] === 'key') {
        $file = "$PRIVATE/portassh_client.key";
        $name = 'portassh_client.key';
        $exe = @file_get_contents($file);
        if ($exe === false) { http_response_code(404); exit('no key uploaded'); }
        header('Content-Type: text/plain; charset=utf-8');
        header('Content-Disposition: attachment; filename="' . $name . '"');
        header('Content-Length: ' . strlen($exe));
        echo $exe;
        exit;
    } elseif ($_GET['dl'] === 'bin') {
        $file = "$PRIVATE/portassh.bin";
        $name = 'portassh.bin';
    } else {
        $file = "$PRIVATE/portassh.exe";
        $name = 'portassh.exe';
    }
    $exe = @file_get_contents($file);
    if ($exe === false) { http_response_code(404); exit('no build uploaded'); }
    header('Content-Type: application/zip');
    header('Content-Disposition: attachment; filename="' . $name . '"');
    header('Content-Length: ' . strlen($exe));
    echo $exe;
    exit;
}

$aliases = json_decode((string) @file_get_contents($ALIASES_F), true);
if (!is_array($aliases)) { $aliases = []; }
$latest = [];

// 1. Быстрый путь: in-memory снапшот флота из hbd API (0 I/O)
$fleetRaw = @file_get_contents('http://127.0.0.1:8897/api/fleet', false, stream_context_create(['http' => ['timeout' => 0.5]]));
if ($fleetRaw) {
    $fleetArr = json_decode($fleetRaw, true);
    if (is_array($fleetArr) && count($fleetArr) > 0) {
        foreach ($fleetArr as $m) {
            $host = $m['host'] ?? '';
            $role = $m['role'] ?? 'user';
            $port = (int) ($m['port'] ?? 0);
            $ts = (int) ($m['ts'] ?? $sysNow);
            $t = $m['t'] ?? '';
            $user = $m['user'] ?? '';
            $osv = $m['os'] ?? '';
            $disk = isset($m['disk']) && (int)$m['disk'] >= 0 ? (int)$m['disk'] : null;
            $ram = isset($m['ram']) && (int)$m['ram'] >= 0 ? (int)$m['ram'] : null;
            $upt = isset($m['upt']) && (int)$m['upt'] >= 0 ? (int)$m['upt'] : null;
            $ver = $m['ver'] ?? '';
            $alias = $aliases[$host] ?? null;
            $verOk = ($ver !== '' && (
                ($LATEST_NAME !== '' && $ver === $LATEST_NAME) ||
                ($LATEST_VER !== '' && (str_starts_with($LATEST_VER, $ver) || str_starts_with($ver, substr($LATEST_VER, 0, 12))))
            ));
            $key = $host . '|' . $role;
            $latest[$key] = compact('host', 'port', 'role', 'ts', 't', 'user', 'osv', 'disk', 'ram', 'ver', 'verOk', 'alias', 'upt');
        }
    }
}

// 2. Fallback: парсинг online.txt с диска при недоступности hbd API
if (!$latest) {
    $lines = @file($REGISTRY, FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES) ?: [];
    foreach ($lines as $line) {
        $p = explode(' ', trim($line));
        if (count($p) < 4) continue;
        [$host, $port, $t, $role] = $p;
        $user = isset($p[4]) && $p[4] !== '-' ? $p[4] : '';
        $osv  = isset($p[5]) && $p[5] !== '-' ? $p[5] : '';
        $disk = isset($p[6]) && (int)$p[6] >= 0 ? (int)$p[6] : null;
        $ram  = isset($p[7]) && (int)$p[7] >= 0 ? (int)$p[7] : null;
        $upt  = isset($p[8]) && $p[8] !== '-' && (int)$p[8] >= 0 ? (int)$p[8] : null;
        $ver  = isset($p[9]) && $p[9] !== '-' ? $p[9] : '';
        $alias = isset($aliases[$host]) ? $aliases[$host] : null;
        $verOk = false;
        if ($ver !== '') {
            $verOk = ($LATEST_NAME !== '' && $ver === $LATEST_NAME)
                || ($LATEST_VER !== '' && (str_starts_with($LATEST_VER, $ver) || str_starts_with($ver, substr($LATEST_VER, 0, 12))));
        }
        if (!in_array($role, ['user', 'admin'], true)) continue;
        [$dmy, $hm] = explode('_', $t);
        [$dd, $mm] = explode('.', $dmy);
        $ts = mktime((int)substr($hm, 0, 2), (int)substr($hm, 3, 2), 0, (int)$mm, (int)$dd) + $SKEW;
        if ($ts - $sysNow > 86400) $ts = strtotime('-1 year', $ts) + $SKEW;
        if ($sysNow - $ts > $MAX_AGE) continue;
        $key = $host . '|' . $role;
        if (!isset($latest[$key]) || $latest[$key]['ts'] < $ts) {
            $latest[$key] = compact('host', 'port', 'role', 'ts', 't', 'user', 'osv', 'disk', 'ram', 'ver', 'verOk', 'alias', 'upt');
        }
    }
}
$active = array_filter($latest, fn($e) => $sysNow - $e['ts'] <= $ACTIVE_SECS);
usort($active, fn($a, $b) => $b['ts'] <=> $a['ts']);

// live-экран: токены wss /hbdws/screenws?port=N&t=..., тот же секрет, что у hbd
$scrToks = [];
$scrSecretFile = '/home/portassh/dash.secret';
if (is_file($scrSecretFile)) {
    $scrSecret = trim((string) file_get_contents($scrSecretFile));
    $bucketNow = gmdate('YmdH');
    foreach ($active as $e) {
        $scrToks[(int) $e['port']] = substr(hash('sha256', 'scr|' . $scrSecret . '|' . (int) $e['port'] . '|' . $bucketNow), 0, 32);
    }
}
$bcmdSaved = isset($_REQUEST['broadcast']) ? (string) ($_REQUEST['bcmd'] ?? '') : '';
if (isset($bports) && isset($results)) {
    $hostByPort = [];
    foreach ($latest as $e) { $hostByPort[(int) $e['port']] = $e['host']; }
    $broadcastHtml = '<h2 style="font-size:16px">Результат массовой команды: <span class="cmd">' . htmlspecialchars($bcmd) . '</span></h2><table style="max-width:950px"><tr><th>Машина</th><th>Порт</th><th>Код</th><th>Ответ</th></tr>';
    foreach ($bports as $p) {
        $r = isset($results[$p]) && is_array($results[$p]) ? $results[$p] : ['code' => -1, 'out' => 'нет ответа'];
        $codeColor = ((int) $r['code'] === 0) ? '#34d399' : '#f87171';
        $h = htmlspecialchars($hostByPort[$p] ?? ('порт ' . $p));
        $out = mb_substr((string) $r['out'], 0, 2000);
        $broadcastHtml .= '<tr><td>' . $h . '</td><td>' . $p . '</td><td style="color:' . $codeColor . ';font-weight:700">'
            . (int) $r['code'] . '</td><td><pre style="margin:0;white-space:pre-wrap;font-size:12px;max-width:500px">'
            . htmlspecialchars($out) . '</pre></td></tr>';
    }
    $broadcastHtml .= '</table>';
}

$staleCount = 0;
foreach ($active as $e) { if ($LATEST_NAME !== '' && ($e['ver'] ?? '') !== $LATEST_NAME) { $staleCount++; } }

$rejHosts = [];
$rawRej = @file_get_contents('http://127.0.0.1:8897/api/rejected-hosts');
if ($rawRej !== false) {
    $rejList = @json_decode($rawRej, true);
    if (is_array($rejList)) { $rejHosts = $rejList; }
}
?>
<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>portassh — пульт дежурного</title>
<style>
  :root {
    --bg: #f6f8fb; --card: #ffffff; --border: #e5eaf1;
    --text: #0f172a; --muted: #64748b; --accent: #4f46e5;
    --green: #10b981; --amber: #f59e0b; --red: #ef4444;
  }
  * { box-sizing: border-box; }
  body { font-family: -apple-system, 'Segoe UI', Roboto, sans-serif; background: var(--bg); color: var(--text); margin: 0; padding: 24px 28px 60px; font-size: 15px; }
  .wrap { max-width: 1080px; margin: 0 auto; }
  header.top { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; flex-wrap: wrap; margin-bottom: 18px; }
  h1 { font-size: 21px; margin: 0 0 4px; display: flex; align-items: center; gap: 10px; }
  .sub { color: var(--muted); font-size: 13px; }
  .sub b { color: var(--text); }
  .badge-live { background: #ecfdf5; color: #059669; border: 1px solid #a7f3d0; font-size: 12px; font-weight: 700; padding: 3px 10px; border-radius: 999px; }
  .actions { display: flex; gap: 8px; flex-wrap: wrap; }
  .btn { display: inline-flex; align-items: center; gap: 6px; padding: 8px 14px; border-radius: 8px; text-decoration: none; font-weight: 600; font-size: 13px; border: 1px solid var(--border); background: #fff; color: var(--text); cursor: pointer; }
  .btn:hover { border-color: #c7d2fe; background: #eef2ff; }
  .tabs { display: flex; gap: 8px; margin-bottom: 14px; }
  .tabbtn { padding: 8px 18px; border: 1px solid var(--border); background: #fff; border-radius: 999px; font-size: 13.5px; font-weight: 600; cursor: pointer; color: var(--muted); }
  .tabbtn.active { background: var(--accent); color: #fff; border-color: var(--accent); }
  .invcard table { font-size: 12.5px; }
  .btn.primary { background: var(--accent); border-color: var(--accent); color: #fff; }
  .btn.primary:hover { background: #4338ca; }
  .btn.amber { color: #92400e; background: #fffbeb; border-color: #fde68a; }
  .btn.amber:hover { background: #fef3c7; }
  .cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: 12px; margin-bottom: 18px; }
  .card { background: var(--card); border: 1px solid var(--border); border-radius: 12px; padding: 14px 18px; }
  .card .num { font-size: 26px; font-weight: 800; }
  .card .lbl { color: var(--muted); font-size: 12.5px; margin-top: 2px; }
  .num.g { color: var(--green); } .num.a { color: var(--amber); } .num.i { color: var(--accent); }
  .panel { background: var(--card); border: 1px solid var(--border); border-radius: 12px; margin-bottom: 18px; overflow: hidden; }
  .panel-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 14px 18px; border-bottom: 1px solid var(--border); flex-wrap: wrap; }
  .panel-head h2 { font-size: 15px; margin: 0; }
  input[type=text], input[type=search] { font-family: inherit; }
  #q { padding: 8px 12px; border: 1px solid var(--border); border-radius: 8px; font-size: 13.5px; width: 240px; background: #fff; }
  table { border-collapse: collapse; width: 100%; font-size: 14px; }
  th, td { padding: 10px 14px; text-align: left; border-bottom: 1px solid #eef1f6; vertical-align: top; }
  th { color: var(--muted); font-weight: 700; font-size: 11.5px; text-transform: uppercase; letter-spacing: .4px; }
  tr:hover td { background: #f8fafc; }
  tr:last-child td { border-bottom: 0; }
  .dot { display: inline-block; width: 9px; height: 9px; border-radius: 50%; background: var(--green); margin-right: 2px; animation: pulse 2s infinite; }
  @keyframes pulse { 50% { opacity: .45; } }
  .mname { font-weight: 700; font-size: 14.5px; }
  .mhost { color: var(--muted); font-size: 12px; font-family: ui-monospace, Consolas, monospace; }
  .chip { display: inline-block; background: #f1f5f9; border-radius: 999px; padding: 2px 9px; font-size: 11.5px; color: #475569; margin: 2px 4px 2px 0; }
  .prob-r { background: #fef2f2 !important; color: #b91c1c !important; font-weight: 700; }
  .prob-a { background: #fffbeb !important; color: #b45309 !important; font-weight: 700; }
  .chip.ver-ok { background: #ecfdf5; color: #059669; font-weight: 700; }
  .chip.ver-old { background: #fffbeb; color: #b45309; font-weight: 700; }
  .role-pill { display: inline-block; padding: 2px 10px; border-radius: 999px; font-size: 12px; font-weight: 600; }
  .role-admin { background: #fff7ed; color: #c2410c; }
  .role-user  { background: #eef2ff; color: #4f46e5; }
  .ago { color: var(--muted); font-size: 12.5px; white-space: nowrap; }
  .cmd { font-family: ui-monospace, Consolas, monospace; font-size: 12.5px; color: #334155; user-select: all; word-break: break-all; }
  /* мастер-деталь */
  .md { display: grid; grid-template-columns: minmax(250px, 330px) minmax(0, 1fr); }
  .mdlist { border-right: 1px solid var(--border); max-height: 72vh; overflow-y: auto; }
  .mditem { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; padding: 10px 14px; border-bottom: 1px solid #eef1f6; cursor: pointer; }
  .mditem:hover { background: #f8fafc; }
  .mditem.sel { background: #eef2ff; box-shadow: inset 3px 0 0 var(--accent); }
  .mditem input[type=checkbox] { margin: 0; }
  .mdname { font-weight: 700; font-size: 13.5px; min-width: 0; overflow-wrap: anywhere; }
  .mditem .role-pill { margin-left: auto; }
  .mdsub { flex-basis: 100%; color: var(--muted); font-size: 11.5px; padding-left: 30px; }
  .mdpane { padding: 18px 20px; min-width: 0; }
  .mhead2 { display: flex; align-items: flex-start; gap: 10px; }
  .mhead2t { min-width: 0; }
  .mname-lg { font-size: 17px; font-weight: 800; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .mhead2 .mdsub { padding-left: 0; margin-top: 2px; font-size: 12.5px; }
  .mdback { display: none; }
  .actgrid { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: 8px; margin: 14px 0 10px; }
  .actgrid .btn { justify-content: center; padding: 10px 12px; font-size: 13px; }
  .cmdbox { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; background: #f8fafc; border: 1px solid var(--border); border-radius: 8px; padding: 8px 12px; margin-top: 6px; }
  .sect { font-size: 11px; text-transform: uppercase; letter-spacing: .5px; color: var(--muted); font-weight: 700; margin: 14px 0 2px; }
  .kv { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0 28px; margin: 4px 0; }
  .kvrow { display: flex; align-items: center; gap: 10px; padding: 6px 0; border-bottom: 1px dashed #eef1f6; min-width: 0; }
  .kvrow .k { color: var(--muted); font-size: 12px; width: 100px; flex: none; }
  .kvrow .v { font-size: 13px; font-weight: 600; min-width: 0; overflow-wrap: anywhere; display: flex; align-items: center; }
  .ok { color: var(--green); } .warn { color: var(--amber); } .bad { color: var(--red); }
  .meter { display: inline-block; width: 90px; height: 7px; border-radius: 4px; background: #e2e8f0; overflow: hidden; vertical-align: middle; margin-right: 8px; flex: none; }
  .meter i { display: block; height: 100%; border-radius: 4px; }
  .note { margin-top: 10px; font-size: 13px; color: #92400e; background: #fffbeb; border: 1px solid #fde68a; border-radius: 8px; padding: 6px 10px; display: inline-block; }
  .btn.sm { padding: 4px 10px; font-size: 12px; border-radius: 6px; }
  .btn.sky { color: #075985; background: #f0f9ff; border-color: #bae6fd; }
  .btn.sky:hover { background: #e0f2fe; }
  .btn.copy { color: #475569; }
  .empty { color: var(--muted); padding: 22px; text-align: center; }
  .bwrap { display: flex; gap: 10px; padding: 14px 18px; flex-wrap: wrap; align-items: center; }
  .bwrap input[type=text] { flex: 1; min-width: 260px; padding: 9px 12px; border: 1px solid var(--border); border-radius: 8px; font-size: 13.5px; font-family: ui-monospace, Consolas, monospace; }
  .bwrap button { background: var(--accent); color: #fff; border: 0; border-radius: 8px; padding: 9px 18px; font-weight: 700; font-size: 13px; cursor: pointer; }
  .bwrap button:hover { background: #4338ca; }
  pre { font-family: ui-monospace, Consolas, monospace; }
  .box { background: var(--card); border: 1px solid var(--border); border-radius: 12px; padding: 6px 22px 16px; }
  .box summary { cursor: pointer; font-weight: 700; font-size: 14.5px; color: var(--accent); padding: 12px 0; }
  .box h2 { font-size: 15px; margin: 20px 0 8px; }
  .box p, .box li { font-size: 13.5px; line-height: 1.6; color: #334155; }
  .box ol, .box ul { margin: 6px 0 6px 22px; }
  .pwtab { margin: 8px 0; width: auto; }
  .pwtab td, .pwtab th { border-bottom: 1px solid #eef1f6; }
  .pw { font-family: ui-monospace, Consolas, monospace; color: #b45309; font-weight: 700; user-select: all; }
  .result-chip { font-weight: 800; }
  a { color: var(--accent); }
  /* --- адаптив: телефоны --- */
  @media (max-width: 700px) {
    body { padding: 12px 10px 40px; font-size: 14px; }
    .wrap { max-width: 100%; }
    h1 { font-size: 18px; gap: 7px; }
    .tabs { flex-wrap: wrap; }
    #q { width: 100%; }
    .cards { grid-template-columns: repeat(2, 1fr); gap: 8px; }
    .card { padding: 10px 12px; }
    .card .num { font-size: 20px; }
    th, td { padding: 8px 10px; }
    .panel { overflow-x: auto; }
    .mcard { margin: 16px 8px !important; max-height: 90vh; }
    table pre { max-width: 100% !important; overflow-x: auto; }
    /* мастер-деталь: список → панель */
    .md { display: block; }
    .mdlist { max-height: none; border-right: 0; }
    .mdpane { display: none; }
    .md.mobile-pane .mdlist { display: none; }
    .md.mobile-pane .mdpane { display: block; }
    .mdback { display: inline-flex; margin-left: auto; }
    .mhead2 { flex-wrap: wrap; }
    .mditem .role-pill { margin-left: auto; }
    .kv { grid-template-columns: 1fr; }
  }
</style>
</head>
<body>
<div class="wrap">
<script>const INV_ALL_PORTS = <?= json_encode(array_map(fn($e) => (int) $e['port'], array_values($active))) ?>;</script>
<script>
// токены доступа к live-экрану (wss /hbdws/), привязаны к порту и текущему часу UTC
const SCR_TOKS = <?= json_encode($scrToks ?? []) ?>;
</script>
<script>
// CSRF: все мутирующие вызовы — только POST; токен добавляется в каждый fetch
const CSRF = '<?= $CSRF ?>';
const _fetch = window.fetch;
window.fetch = (u, o) => {
  o = o || {};
  o.headers = Object.assign(new Headers(o.headers || {}), {'X-CSRF': CSRF});
  return _fetch(u, o);
};
function postAct(params) {
  const b = new URLSearchParams(params); b.set('t', CSRF);
  return fetch(window.location.href, {method: 'POST', body: b})
    .then(r => {
      if (!r.ok) {
        return r.text().then(txt => {
          alert('Ошибка (' + r.status + '): ' + txt);
        });
      }
      location.reload();
    })
    .catch(err => {
      alert('Сетевая ошибка: ' + err);
    });
}
function addPlaybook() {
  const n = prompt('Название сценария (в меню):');
  if (n === null || !n.trim()) return;
  const c = prompt('Команда (cmd):');
  if (c === null || !c.trim()) return;
  postAct({addpb: 1, pbname: n.trim(), pbcmd: c.trim()});
}
function delPlaybook() {
  const n = prompt('Название сценария для удаления:');
  if (n === null || !n.trim()) return;
  postAct({delpb: 1, pbname: n.trim()});
}
function editAlias(host, cur, note) {
  const n = prompt('Имя машины (оставить пустым = удалить метку):', cur || '');
  if (n === null) return;
  const t = prompt('Заметка (необязательно):', note || '');
  if (t === null) return;
  postAct({setalias: host, name: n, note: t});
}
function copyCmd(text, el) {
  navigator.clipboard.writeText(text).then(() => {
    const old = el.textContent; el.textContent = '✓ скопировано';
    setTimeout(() => el.textContent = old, 1500);
  });
}
function showTab(t) {
  document.getElementById('tab-machines').style.display = t === 'm' ? '' : 'none';
  document.getElementById('tab-inv').style.display = t === 'i' ? '' : 'none';
  document.getElementById('tb-m').classList.toggle('active', t === 'm');
  document.getElementById('tb-i').classList.toggle('active', t === 'i');
  location.hash = t === 'i' ? '#tab-inv' : '';
}
function invFilter(q) {
  q = q.toLowerCase();
  document.querySelectorAll('.iprog').forEach(tr => {
    tr.style.display = tr.getAttribute('data-p').includes(q) ? '' : 'none';
  });
  document.querySelectorAll('.invcard').forEach(c => {
    const any = c.querySelectorAll('.iprog:not([style*="none"])').length > 0;
    c.style.display = q === '' || any ? '' : 'none';
  });
}
function invRefreshOne() {
  const p = document.getElementById('invsel').value;
  if (!p) { alert('выберите машину в списке'); return; }
  fetch('?inv=' + p + '&quiet=1').then(() => location.reload());
}
function invFetch(p) {
  fetch('?inv=' + p + '&quiet=1').then(() => refreshPane(location.href));
  return false;
}
function invRefreshAll() {
  const ports = INV_ALL_PORTS;
  let i = 0;
  const step = () => {
    if (i >= ports.length) { location.reload(); return; }
    fetch('?inv=' + ports[i++] + '&quiet=1').then(step).catch(step);
  };
  step();
}
if (location.hash === '#tab-inv') { setTimeout(() => showTab('i'), 0); }
function filterRows(q) {
  q = q.toLowerCase();
  document.querySelectorAll('.mditem[data-search]').forEach(it => {
    it.style.display = it.getAttribute('data-search').includes(q) ? '' : 'none';
  });
}
</script>

<header class="top">
  <div>
    <h1>portassh <span class="badge-live">● <?= count($active) ?> активных</span></h1>
    <div class="sub">пульт дежурного · <span id="syncstatus"><span style="color:#10b981">● AJAX-синхронизация</span> (15 с)</span> · актуальная сборка <b><?= $LATEST_NAME !== '' ? 'ver. ' . htmlspecialchars($LATEST_NAME) : '?' ?></b></div>
  </div>
  <div class="actions">
    <button class="btn" style="background:#7c3aed;color:#fff;border-color:#6d28d9" onclick="toggleAICopilot()" title="AI SysAdmin Copilot">🤖 AI Copilot</button>
    <button class="btn" onclick="doAjaxRefresh()" title="Обновить список машин без перезагрузки страницы">⟳ Обновить</button>
    <a class="btn" href="?dl=zip">⬇ .zip</a>
    <a class="btn" href="?dl=bin">⬇ .bin</a>
    <a class="btn" href="?dl=key">🔑 Ключ</a>
    <a class="btn amber" href="#" onclick="postAct({force:1});return false" title="клиенты проверят обновление в течение минуты">⟳ Обновить клиентов</a>
  </div>
</header>

<div class="tabs">
  <button id="tb-m" class="tabbtn active" onclick="showTab('m')">🖥 Машины</button>
  <button id="tb-i" class="tabbtn" onclick="showTab('i')">📦 Инвентарь</button>
</div>

<div id="tab-machines">
<?php if (!empty($rejHosts)): ?>
  <div style="background:#fef2f2;border:1px solid #fca5a5;border-radius:8px;padding:12px 16px;margin:0 0 16px;color:#991b1b;font-size:13px;display:flex;flex-wrap:wrap;align-items:center;gap:10px">
    <span><b>⚠️ Попытка подключения неразрешённых хостов:</b></span>
    <?php foreach ($rejHosts as $rh): ?>
      <span style="display:inline-flex;align-items:center;gap:6px;background:#fee2e2;border:1px solid #fecaca;padding:4px 10px;border-radius:6px">
        <code><?= htmlspecialchars($rh['host']) ?></code> <span style="font-size:11px;color:#6b7280"><?= htmlspecialchars($rh['time']) ?></span>
        <a href="?allow_host=<?= urlencode($rh['host']) ?>" style="background:#16a34a;color:#fff;text-decoration:none;padding:2px 8px;border-radius:4px;font-size:11px;font-weight:600" title="Добавить в белый список">+ Разрешить</a>
        <a href="?del_rejected=<?= urlencode($rh['host']) ?>" style="background:#dc2626;color:#fff;text-decoration:none;padding:2px 8px;border-radius:4px;font-size:11px;font-weight:600" title="Удалить запись">✕ Удалить</a>
      </span>
    <?php endforeach; ?>
    <a href="?del_rejected=all" onclick="return confirm('Очистить весь список неразрешённых хостов?')" style="background:#64748b;color:#fff;text-decoration:none;padding:4px 10px;border-radius:4px;font-size:11px;font-weight:600;margin-left:auto">🗑 Очистить все</a>
  </div>
<?php endif; ?>
<section class="cards">
  <div class="card"><div class="num g"><?= count($active) ?></div><div class="lbl">активных машин</div></div>
  <div class="card"><div class="num a"><?= $staleCount ?></div><div class="lbl">требуют обновления</div></div>
  <div class="card"><div class="num i"><?= count(array_filter($active, fn($e) => $e['role'] === 'admin')) ?></div><div class="lbl">админских сессий</div></div>
  <div class="card"><div class="num"><?= count($aliases) ?></div><div class="lbl">меток в справочнике</div></div>
</section>

<div class="panel">
  <div class="panel-head">
    <h2>Машины</h2>
    <div style="display:flex;gap:8px;align-items:center;flex-wrap:wrap">
      <input type="search" id="q" placeholder="фильтр: имя, хост, пользователь…" oninput="filterRows(this.value)">
      <button class="btn" id="probbtn" onclick="toggleProblems()" style="display:none">👁 Только с проблемами</button>
    </div>
  </div>
  <form method="post" action="/sshdash">
  <input type="hidden" name="t" value="<?= $CSRF ?>">
<?php
$M = [];
foreach ($active as $e) {
    $sec = $sysNow - $e['ts'];
    $seen = $sec < 5 ? 'только что' : $sec . ' с назад';
    $cmd = "ssh -C -i portassh_client.key -o StrictHostKeyChecking=accept-new -J support@{$GLOBALS['RELAY']} -p {$e['port']} anyuser@localhost";
    $alias = $e['alias'];
    $probs = [];
    if ($e['disk'] !== null && $e['disk'] < 5) { $probs[] = '🔴 диск C: ' . (int) $e['disk'] . ' ГБ'; }
    if ($e['upt'] !== null && $e['upt'] > 5) { $probs[] = '⏰ аптайм ' . $e['upt'] . ' дн'; }
    $avProd = null; $avState = null;
    $avf = sprintf('%s/inv_%d.json', $PRIVATE, (int) $e['port']);
    if (is_file($avf)) {
        $avj = json_decode((string) file_get_contents($avf), true);
        $av = $avj['data']['antivirus'] ?? null;
        if ($av) { $avProd = (string) $av['product']; $avState = (string) $av['state']; }
        if ($av && mb_strtolower($avState) !== 'running' && mb_strpos($avProd, 'не обнаружен') === false) {
            $probs[] = '⚠️ AV: ' . $avProd . ' ' . $avState;
        }
    }
    $dispName = ($alias !== null && $alias['name'] !== '') ? $alias['name'] : $e['host'];
    $search = mb_strtolower($dispName . ' ' . $e['host'] . ' ' . ($e['user'] ?? '') . ' ' . $e['role']);
    $editBtn = '<a href="#" onclick="editAlias(\'' . htmlspecialchars($e['host']) . '\', '
             . htmlspecialchars(json_encode($alias !== null ? $alias['name'] : '')) . ', '
             . htmlspecialchars(json_encode($alias !== null ? $alias['note'] : '')) . ');return false" title="метка/заметка">✏️</a>';
    $btnHtml = '<a class="btn primary" href="https://' . $WSS_HOST . '/wsterm?port=' . (int) $e['port'] . '" target="_blank">🖥 Терминал</a>'
             . '<a class="btn" href="#" onclick="openScreen(' . (int) $e['port'] . ', \'' . htmlspecialchars($dispName) . '\');return false" title="экран в браузере">🖼 Экран</a>'
             . '<a class="btn" href="#" onclick="openFiles(' . (int) $e['port'] . ', \'' . htmlspecialchars($dispName) . '\');return false" title="файлы машины">📁 Файлы</a>'
             . '<a class="btn sky" href="?rdp=' . (int) $e['port'] . '" title="скачать .rdp">🖥️ RDP</a>'
             . '<a class="btn" href="?shadow=' . (int) $e['port'] . '" title="совместный экран">👥 Тень</a>'
             . '<a class="btn" href="#" onclick="openSvc(' . (int) $e['port'] . ', \'' . htmlspecialchars($dispName) . '\');return false" title="службы">⚙ Службы</a>'
             . '<a class="btn" href="#" onclick="openProcs(' . (int) $e['port'] . ', \'' . htmlspecialchars($dispName) . '\');return false" title="диспетчер задач">📊 Процессы</a>'
             . '<a class="btn" href="#" onclick="openEvents(' . (int) $e['port'] . ', \'' . htmlspecialchars($dispName) . '\');return false" title="журнал событий Windows">📋 События</a>'
             . '<a class="btn amber" href="#" onclick="openPower(event, ' . (int) $e['port'] . ', \'' . htmlspecialchars($dispName) . '\');return false" title="питание">⏻ Питание</a>';
    $M[] = [
        'port' => (int) $e['port'], 'host' => $e['host'], 'name' => $dispName,
        'seen' => $seen, 'role' => $e['role'], 'roleLabel' => $e['role'] === 'admin' ? 'админ' : 'пользователь',
        'search' => $search, 'probs' => count($probs), 'probsTxt' => implode(' · ', $probs),
        'rowStyle' => $probs ? 'background:#fffbeb' : '',
        'editBtn' => $editBtn, 'btnHtml' => $btnHtml, 'cmd' => $cmd,
        'user' => $e['user'] ?? '', 'osv' => $e['osv'] ?? '',
        'disk' => $e['disk'] ?? null, 'ram' => $e['ram'] ?? null, 'upt' => $e['upt'] ?? null,
        'ver' => $e['ver'] ?? '', 'verOk' => $e['verOk'] ?? false,
        'avProd' => $avProd, 'avState' => $avState,
        'aliasNote' => ($alias !== null && $alias['note'] !== '') ? $alias['note'] : '',
    ];
}
$selPort = isset($_GET['m']) ? (int) $_GET['m'] : 0;
$selRow = null;
foreach ($M as $r) { if ($r['port'] === $selPort) { $selRow = $r; break; } }
if ($selRow === null && $M) { $selRow = $M[0]; }
?>
  <div class="md" id="mdwrap">
    <div class="mdlist" id="mdlist">
<?php if (!$M): ?>
      <div class="empty">Нет активных машин — запустите portassh.exe на целевом компьютере</div>
<?php endif; ?>
<?php foreach ($M as $r): ?>
      <div class="mditem<?= $selRow !== null && $r['host'] === $selRow['host'] ? ' sel' : '' ?>" data-search="<?= htmlspecialchars($r['search']) ?>" data-host="<?= htmlspecialchars($r['host']) ?>" data-problems="<?= $r['probs'] ?>" style="<?= $r['rowStyle'] ?>" onclick="return selectHost(<?= htmlspecialchars(json_encode($r['host'])) ?>, <?= $r['port'] ?>)">
        <input type="checkbox" name="sel[]" value="<?= $r['port'] ?>" title="выбрать для broadcast" onclick="event.stopPropagation()">
        <span class="dot"></span>
        <span class="mdname"><?= htmlspecialchars($r['name']) ?></span>
        <span class="role-pill role-<?= $r['role'] ?>"><?= $r['roleLabel'] ?></span>
        <span class="mdsub">порт <?= $r['port'] ?> · <?= htmlspecialchars($r['seen']) ?><?= $r['probsTxt'] !== '' ? ' · ' . htmlspecialchars($r['probsTxt']) : '' ?></span>
      </div>
<?php endforeach; ?>
    </div>
    <div class="mdpane" id="mdpane" data-host="<?= $selRow ? htmlspecialchars($selRow['host']) : '' ?>">
<?php if ($selRow): ?>
      <div class="mhead2">
        <span class="dot"></span>
        <div class="mhead2t">
          <div class="mname-lg"><?= htmlspecialchars($selRow['name']) ?> <?= $selRow['editBtn'] ?></div>
          <div class="mdsub"><?= htmlspecialchars($selRow['host']) ?> · порт <?= $selRow['port'] ?> · <?= htmlspecialchars($selRow['seen']) ?></div>
        </div>
        <a class="mdback" href="#" onclick="document.getElementById('mdwrap').classList.remove('mobile-pane');return false">← Назад</a>
      </div>
<?php
      $diskV = $selRow['disk']; $ramV = $selRow['ram']; $uptV = $selRow['upt'];
      $diskCls = $diskV === null ? '' : ($diskV < 5 ? 'bad' : ($diskV < 20 ? 'warn' : 'ok'));
      $ramCls = $ramV === null ? '' : ($ramV >= 92 ? 'bad' : ($ramV >= 80 ? 'warn' : 'ok'));
      $ramBar = $ramCls === 'bad' ? 'var(--red)' : ($ramCls === 'warn' ? 'var(--amber)' : 'var(--green)');
      $uptCls = $uptV === null ? '' : ($uptV > 5 ? 'warn' : 'ok');
      $avBad = $selRow['avProd'] !== null && mb_strtolower($selRow['avState']) !== 'running' && mb_strpos($selRow['avProd'], 'не обнаружен') === false;
?>
      <div class="sect">Состояние</div>
      <div class="kv">
        <div class="kvrow"><span class="k">Диск C:</span><span class="v <?= $diskCls ?>"><?= $diskV !== null ? $diskV . ' ГБ свободно' : '—' ?></span></div>
        <div class="kvrow"><span class="k">Память</span><span class="v <?= $ramCls ?>"><?= $ramV !== null ? '<span class="meter"><i style="width:' . (int) $ramV . '%;background:' . $ramBar . '"></i></span>' . (int) $ramV . '%' : '—' ?></span></div>
        <div class="kvrow"><span class="k">Аптайм</span><span class="v <?= $uptCls ?>"><?= $uptV !== null ? $uptV . ' ' . ($uptCls === 'warn' ? 'дн ⚠️' : 'дн') : '—' ?></span></div>
        <div class="kvrow"><span class="k">Антивирус</span><span class="v <?= $avBad ? 'bad' : '' ?>"><?= $selRow['avProd'] !== null ? htmlspecialchars($selRow['avProd']) . ' · ' . htmlspecialchars($selRow['avState']) : 'нет данных · <a href="#" onclick="invFetch(' . $selRow['port'] . ');return false">🔄 обновить инвентарь</a>' ?></span></div>
      </div>
      <div class="sect">Система</div>
      <div class="kv">
        <div class="kvrow"><span class="k">ОС</span><span class="v">Windows<?= $selRow['osv'] !== '' ? ' · сборка ' . htmlspecialchars($selRow['osv']) : '' ?></span></div>
        <div class="kvrow"><span class="k">Пользователь</span><span class="v"><?= $selRow['user'] !== '' ? htmlspecialchars($selRow['user']) : '—' ?></span></div>
        <div class="kvrow"><span class="k">portassh</span><span class="v"><?= $selRow['ver'] !== '' ? ($selRow['verOk'] ? '<span class="chip ver-ok">ver. ' . htmlspecialchars($LATEST_NAME !== '' ? $LATEST_NAME : $selRow['ver']) . ' · актуальна</span>' : ($LATEST_NAME !== '' ? '<span class="chip ver-old">устарела: ' . htmlspecialchars($selRow['ver']) . ' → доступна ' . htmlspecialchars($LATEST_NAME) . '</span>' : '<span class="chip">ver. ' . htmlspecialchars($selRow['ver']) . '</span>')) : '—' ?></span></div>
      </div>
      <?php if ($selRow['aliasNote'] !== ''): ?><div class="note">📝 <?= htmlspecialchars($selRow['aliasNote']) ?></div><?php endif; ?>
      <div class="actgrid"><?= $selRow['btnHtml'] ?></div>
      <div class="cmdbox"><code class="cmd"><?= htmlspecialchars($selRow['cmd']) ?></code>
        <a class="btn sm copy" href="#" onclick="copyCmd(<?= htmlspecialchars(json_encode($selRow['cmd'])) ?>, this);return false">⧉ Копировать SSH-команду</a></div>
<?php else: ?>
      <div class="empty">Выберите машину слева</div>
<?php endif; ?>
    </div>
  </div>
<script>
<?php foreach ($active as $e) { if (isset($e['ram']) && $e['ram'] !== null && $e['ram'] > 92) { echo "ramWatch(" . json_encode($e['host']) . ", " . (int) $e['ram'] . ");\n"; } } ?>
let selHost = <?= json_encode($selRow ? $selRow['host'] : '') ?>;
function highlightSel() {
  document.querySelectorAll('.mditem').forEach(i => i.classList.toggle('sel', i.getAttribute('data-host') === selHost));
}
function selectHost(host, port) {
  selHost = host;
  highlightSel();
  if (window.matchMedia('(max-width: 700px)').matches) document.getElementById('mdwrap').classList.add('mobile-pane');
  const url = new URL(location.href);
  url.searchParams.set('m', port);
  history.replaceState(null, '', url);
  refreshPane(url.toString());
  return false;
}
async function refreshPane(url) {
  try {
    const res = await fetch(url || location.href);
    if (!res.ok) return;
    const doc = new DOMParser().parseFromString(await res.text(), 'text/html');
    const np = doc.querySelector('#mdpane');
    const cp = document.querySelector('#mdpane');
    if (np && cp) cp.innerHTML = np.innerHTML;
  } catch (e) {}
}
function ramWatch(host, ram) {
  const k = 'ramhist_' + host;
  let h = [];
  try { h = JSON.parse(localStorage.getItem(k) || '[]'); } catch (err) {}
  h.push(ram);
  if (h.length > 3) h = h.slice(-3);
  localStorage.setItem(k, JSON.stringify(h));
  if (h.length === 3 && h.every(v => v > 92)) {
    const item = document.querySelector('.mditem[data-host="' + host + '"]');
    if (!item || item.querySelector('.prob-a')) return;
    const chip = document.createElement('span');
    chip.className = 'chip prob-a';
    chip.textContent = '🟠 RAM > 92% (3 проверки)';
    const pane = document.querySelector('#mdpane[data-host="' + host + '"]');
    const kv = pane ? pane.querySelector('.kv') : null;
    if (kv) { chip.style.gridColumn = '1 / -1'; kv.append(chip); }
    item.setAttribute('data-problems', String((parseInt(item.getAttribute('data-problems') || '0') || 0) + 1));
    item.style.background = '#fffbeb';
  }
}
(function updateProbBtn() {
  const n = document.querySelectorAll('.mditem[data-problems]:not([data-problems="0"])').length;
  const btn = document.getElementById('probbtn');
  if (n > 0) { btn.style.display = ''; btn.textContent = '👁 Только с проблемами (' + n + ')'; }
})();
let showProblemsOnly = false;
function toggleProblems() {
  showProblemsOnly = !showProblemsOnly;
  document.querySelectorAll('.mditem[data-search]').forEach(it => {
    const p = parseInt(it.getAttribute('data-problems') || '0');
    it.style.display = (showProblemsOnly && p === 0) ? 'none' : '';
  });
}
</script>
  <div class="bwrap">
    <input type="hidden" name="broadcast" value="1">
    <select id="pbsel" onchange="if(this.value)document.getElementById('bcmd').value=this.value" style="max-width:280px;padding:8px 10px;border:1px solid var(--border);border-radius:8px;background:#fff;font-size:13px">
      <option value="">⚡ Быстрые действия…</option>
<?php foreach ($playbooks as $pb) { echo '<option value="' . htmlspecialchars($pb['c']) . '">' . htmlspecialchars($pb['n']) . '</option>'; } ?>
    </select>
    <a href="#" class="btn sm" onclick="addPlaybook();return false" title="добавить свой сценарий">➕</a>
    <a href="#" class="btn sm" onclick="delPlaybook();return false" title="удалить сценарий">🗑</a>
    <input type="text" name="bcmd" id="bcmd" placeholder="команда для выбранных машин, напр. sys:disks" value="<?= htmlspecialchars($bcmdSaved) ?>">
    <button type="submit">▶ Выполнить на выбранных</button>
  </div>
  </form>
</div>

<?= $broadcastHtml ?>
</div>

<div id="tab-inv" style="display:none">
  <div class="panel">
    <div class="panel-head">
      <h2>Инвентарь машин</h2>
      <div style="display:flex;gap:8px;flex-wrap:wrap">
        <select id="invsel" style="padding:7px 10px;border:1px solid var(--border);border-radius:8px;background:#fff;font-size:13px">
          <option value="">— выбрать машину —</option>
<?php foreach ($active as $e) { echo '<option value="' . (int) $e['port'] . '">' . htmlspecialchars($e['host']) . ' (порт ' . (int) $e['port'] . ')</option>'; } ?>
        </select>
        <button class="btn" onclick="invRefreshOne()">🔄 Обновить выбранную</button>
        <button class="btn" onclick="invRefreshAll()">🔄 Обновить все</button>
      </div>
    </div>
    <div class="bwrap">
      <input type="text" id="invq" placeholder="поиск по программам во всех кэшах (например, 7-Zip)" oninput="invFilter(this.value)" style="flex:1;min-width:260px;padding:9px 12px;border:1px solid var(--border);border-radius:8px;font-size:13.5px">
    </div>
<?php
$invPorts = [];
foreach (glob($PRIVATE . '/inv_*.json') ?: [] as $f) {
    $j = json_decode((string) @file_get_contents($f), true);
    if (!is_array($j) || !isset($j['data']['host'], $j['data']['relayport'])) continue;
    $invPorts[(int) $j['data']['relayport']] = ['ts' => (int) $j['ts'], 'd' => $j['data']];
}
ksort($invPorts);
if (!$invPorts) {
    echo '<div class="empty" style="padding:20px">Кэш пуст — нажмите «Обновить все» (машины должны быть активны)</div>';
}
foreach ($invPorts as $p => $inv) {
    $d = $inv['d'];
    $age = max(0, $sysNow - $inv['ts']);
    $av = isset($d['antivirus']) ? $d['antivirus'] : null;
    $avStr = $av ? ($av['product'] . ' · ' . $av['state']) : '—';
    echo '<div class="invcard" data-card="' . $p . '" style="border-top:1px solid var(--border);padding:12px 18px">';
    echo '<div style="display:flex;justify-content:space-between;flex-wrap:wrap;gap:8px;align-items:center">';
    echo '<b style="font-size:14.5px">' . htmlspecialchars($d['host']) . '</b>';
    echo '<span class="ago">снимок ' . $age . ' с назад · <a href="?inv=' . $p . '&quiet=1" onclick="return false">обновить</a></span>';
    echo '</div>';
    echo '<div class="meta" style="margin:6px 0">'
        . '🖥 ' . htmlspecialchars(($d['manufacturer'] ?? '') . ' ' . ($d['model'] ?? ''))
        . ' · CPU ' . htmlspecialchars($d['cpu'] ?? '?')
        . ' · RAM ' . round((float) ($d['ram_gb'] ?? 0), 1) . ' GB'
        . ' · 🛡 ' . htmlspecialchars($avStr)
        . ' · 👤 ' . htmlspecialchars($d['user'] ?? '?')
        . '</div>';
    echo '<table style="font-size:12.5px"><tr><th>Программа</th><th>Версия</th></tr>';
    $n = 0;
    foreach (($d['installed'] ?? []) as $pr) {
        $search = mb_strtolower($pr['name'] . ' ' . $pr['version']);
        echo '<tr class="iprog" data-p="' . htmlspecialchars($search) . '" data-card="' . $p . '"><td>'
            . htmlspecialchars($pr['name']) . '</td><td>' . htmlspecialchars($pr['version']) . '</td></tr>';
        $n++;
    }
    echo '</table></div>';
}
?>
  </div>
</div>

<div class="panel box">
<details>
<summary>📖 Инструкция — нажать, чтобы раскрыть</summary>

<h2>Что это</h2>
<p>portassh даёт удалённый доступ к Windows-компьютерам через туннель. На машине
запускается <b>portassh.exe</b> — он сам строит туннель через relay-сервер. Никаких
настроек сети и роутера не нужно. Эта страница показывает, какие машины сейчас
онлайн, и даёт подключиться к ним.</p>

<h2>Установить на машину (Windows)</h2>
<ol>
<li>Нажми «⬇ .zip» вверху. Распакуй архив — пароль: <span class="pw"><?= htmlspecialchars($ZIP_PASS) ?></span></li>
<li>Запусти <span class="cmd">portassh.exe</span> двойным кликом. Окна нет — так и надо,
программа работает в фоне.</li>
</ol>
<p>Готово: через минуту машина появится в таблице.<br>
<span style="font-size:12.5px;color:var(--muted)">SmartScreen: «Подробнее» → «Выполнить в любом случае».
Антивирус удалил — добавь в исключения и скачай снова.</span></p>

<h2>Подключиться к машине</h2>
<ol>
<li><b>Скачай ключ</b> — кнопка «🔑 Ключ» вверху. Файл <span class="cmd">portassh_client.key</span>
сохранится в папку «Загрузки». Это один раз: ключ лежит у тебя и работает всегда.</li>
<li><b>Открой терминал в папке с ключом</b> — команда должна запускаться оттуда:
<div style="margin:6px 0">
<div style="font-size:12.5px;color:var(--muted)">Windows:</div>
<span class="cmd">Win+R → cmd → Enter → cd %USERPROFILE%\Downloads</span>
</div>
<div style="margin:6px 0">
<div style="font-size:12.5px;color:var(--muted)">Mac / Linux:</div>
<span class="cmd">cd ~/Downloads</span>
</div></li>
<li><b>Скопируй команду</b> нужной машины из таблицы ниже и вставь в терминал. Enter.</li>
</ol>
<p>Всё — ты в командной строке машины. С ключом пароль не спросится.</p>

<p>Если не хочешь терминал — жми «🖥 Терминал» у машины: тот же экран в браузере
(вкладка откроется сама, пароль тоже не нужен).</p>

<h2>Куда положить ключ (подробно)</h2>
<p>Ключ — это файл <span class="cmd">portassh_client.key</span>. Правило одно:
<b>команда запускается из той папки, где лежит ключ</b>.</p>
<ul>
<li>Скачал в «Загрузки» → открой терминал в «Загрузках» (см. шаг 2 выше) — работает.</li>
<li>Хочешь держать ключ постоянно — положи его в удобную папку (например
<span class="cmd">C:\portassh\</span> или <span class="cmd">~/portassh/</span>) и открывай
терминал там же.</li>
<li>Можно и в <span class="cmd">C:\Windows\system32</span> — тогда команда работает из
любой папки, но так делать не рекомендуем.</li>
</ul>
<p>Ключ = пароль. Не пересылай его никому, кроме коллег, которым нужен доступ.</p>

<h2>Пароль (резерв)</h2>
<p>Если ключа нет — при подключении пароль спросят два раза (relay и машина),
оба раза один и тот же: <span class="pw"><?= htmlspecialchars($ZIP_PASS) ?></span></p>

<h2>Управление машиной из таблицы</h2>
<table class="pwtab">
<tr><th>Кнопка</th><th>Что делает</th></tr>
<tr><td>🖥 Терминал</td><td>командная строка машины прямо в браузере</td></tr>
<tr><td>📊 Процессы</td><td>диспетчер задач: поиск, память, «снять задачу»</td></tr>
<tr><td>⚙ Службы</td><td>запуск/остановка/перезапуск служб Windows</td></tr>
<tr><td>📋 События</td><td>журнал событий Windows (System / Application) + анализ сбоев через AI</td></tr>
<tr><td>🖥️ RDP</td><td>скачать .rdp — удалённый рабочий стол (нужен RDP-туннель, см. ниже)</td></tr>
<tr><td>👥 Тень</td><td>видеть экран пользователя и управлять вместе с ним (.bat + mstsc)</td></tr>
<tr><td>🖼 Экран</td><td>экран машины прямо в браузере: клики, перетаскивание, скролл, печать текста</td></tr>
<tr><td>📁 Файлы</td><td>закинуть файл на машину / скачать с машины</td></tr>
<tr><td>⏻ Питание</td><td>перезагрузка / выключение / блокировка экрана</td></tr>
<tr><td>🤖 AI Copilot</td><td>умный помощник: исследование флота и диагностика через OpenRouter / Google AI</td></tr>
<tr><td>⧉ команда</td><td>скопировать SSH-команду для своего терминала</td></tr>
<tr><td>✏️</td><td>метка и заметка машине («Бухгалтер Иванова»)</td></tr>
</table>
<p>Галочки слева + поле «команда для выбранных» — выполнить одну команду сразу
на нескольких машинах (например <span class="cmd">sys:disks</span> — место на дисках у всех).</p>

<h2>Быстрая диагностика из терминала</h2>
<p><span class="cmd">sys:whoami</span> · <span class="cmd">sys:hostname</span> ·
<span class="cmd">sys:procs</span> (процессы) · <span class="cmd">sys:svcs</span> (службы) ·
<span class="cmd">sys:disks</span> (диски) — вместо команды:
<span class="cmd">…anyuser@localhost "sys:disks"</span></p>

<h2>RDP (удалённый рабочий стол через mstsc)</h2>
<ol>
<li>RDP на машине должен быть включён (Pro — обычно включён; Home — нужен RDP Wrapper).
Выключен? По SSH в админском сеансе:
<span class="cmd">reg add "HKLM\SYSTEM\CurrentControlSet\Control\Terminal Server" /v fDenyTSConnections /t REG_DWORD /d 0 /f &amp;&amp; netsh advfirewall firewall add rule name="RDP" dir=in action=allow protocol=tcp localport=3389</span></li>
<li>Построй туннель: порт машины + 10000. Для порта 2200:
<span class="cmd">ssh -C -L 12200:127.0.0.1:12200 support@<?= htmlspecialchars($RELAY) ?></span></li>
<li>Скачай «🖥️ RDP» и открой файл — адрес уже вписан (localhost:12200).</li>
</ol>

<h2>Экран пользователя (теневой сеанс)</h2>
<p>Кнопка «👥 Тень» скачает .bat: он откроет туннель и подключит тебя к экрану
пользователя вместе с ним (пользователь всё видит). Требуется Windows у инженера
и один запуск portassh с <span class="cmd">-admin</span> на машине (ставит политику теневых сеансов).</p>

<h2>Если что-то не работает</h2>
<ul>
<li><b>Машины нет в списке</b> — exe не запущен; подожди минуту после запуска.</li>
<li><b>Connection refused</b> — туннель переподключается сам, до минуты.
Дольше — перезапусти portassh.exe на машине.</li>
<li><b>Скачивание заблокировано</b> — кнопка «.bin»: скачай и переименуй в .exe.</li>
<li><b>Команда зависла</b> — ждала клик по UAC на машине или интерактивный ввод.
Добавь <span class="cmd">&amp;</span> в конец (фоновый запуск) или
<span class="cmd">timeout=60 </span> в начало (убьёт через 60 с).</li>
<li><b>Самодиагностика машины</b> — на ней в cmd: <span class="cmd">portassh.exe -doctor</span> —
проверит сеть, ConPTY и права, покажет [OK]/[FAIL] по пунктам.</li>
</ul>
</details>
</div>
</div>
<div id="filemodal" style="display:none;position:fixed;inset:0;background:rgba(15,23,42,.55);z-index:50" onclick="if(event.target===this)closeFiles()">
  <div class="mcard" style="background:#fff;max-width:640px;margin:48px auto;border-radius:12px;padding:16px 18px;max-height:80vh;display:flex;flex-direction:column">
    <div style="display:flex;justify-content:space-between;align-items:center;gap:10px;margin-bottom:10px">
      <b>Файлы — <span id="flhost"></span> <span style="color:#64748b;font-size:12px">(домашняя папка)</span></b>
      <a href="#" onclick="closeFiles();return false" style="text-decoration:none">✕</a>
    </div>
    <div style="display:flex;gap:8px;align-items:center;margin-bottom:10px;flex-wrap:wrap">
      <input type="file" id="flup" style="font-size:12.5px">
      <button onclick="flUpload()" style="background:#4f46e5;color:#fff;border:0;border-radius:8px;padding:7px 14px;font-weight:600;font-size:12.5px;cursor:pointer">📤 На машину</button>
    </div>
    <div id="flbody" style="overflow-y:auto;flex:1"></div>
  </div>
</div>
<script>
let flPort = 0, flHost = '';
function openFiles(port, host) {
  flPort = port; flHost = host;
  document.getElementById('flhost').textContent = host;
  document.getElementById('filemodal').style.display = 'block';
  flLoad();
}
function closeFiles() { document.getElementById('filemodal').style.display = 'none'; }
function flLoad() {
  document.getElementById('flbody').innerHTML = '<div style="color:#64748b">загрузка…</div>';
  fetch('?ls=' + flPort).then(r => r.json()).then(list => {
    const body = document.getElementById('flbody');
    body.innerHTML = '';
    (list || []).sort((a, b) => (b.d - a.d) || a.n.localeCompare(b.n)).forEach(f => {
      const row = document.createElement('div');
      row.style.cssText = 'display:flex;align-items:center;gap:8px;padding:6px 4px;border-bottom:1px solid #eef1f6';
      const nm = document.createElement('span');
      nm.style.cssText = 'flex:1;font-size:13px';
      nm.textContent = (f.d ? '📁 ' : '📄 ') + f.n;
      const sz = document.createElement('span');
      sz.style.cssText = 'color:#64748b;font-size:12px';
      sz.textContent = f.d ? 'папка' : (f.s / 1048576).toFixed(2) + ' МБ';
      row.append(nm, sz);
      if (!f.d) {
        const dl = document.createElement('a');
        dl.href = '?getfile=' + flPort + '&name=' + encodeURIComponent(f.n);
        dl.textContent = '⬇';
        dl.title = 'скачать';
        dl.style.cssText = 'text-decoration:none;font-size:15px';
        row.append(dl);
      }
      body.append(row);
    });
    if (!body.children.length) body.innerHTML = '<div style="color:#64748b;padding:10px">папка пуста</div>';
  }).catch(() => { document.getElementById('flbody').textContent = 'ошибка загрузки'; });
}
function flUpload() {
  const inp = document.getElementById('flup');
  if (!inp.files.length) { alert('выберите файл'); return; }
  const fd = new FormData();
  fd.append('f', inp.files[0]);
  document.getElementById('flbody').innerHTML = '<div style="color:#64748b">отправка…</div>';
  fetch('?putfile=' + flPort, { method: 'POST', body: fd })
    .then(() => { inp.value = ''; flLoad(); });
}
</script>

<div id="powermenu" style="display:none;position:fixed;z-index:70;background:#fff;border:1px solid var(--border);border-radius:10px;box-shadow:0 8px 24px rgba(15,23,42,.18);padding:6px;min-width:230px">
  <div id="pmhost2" style="font-size:12px;color:var(--muted);padding:4px 10px 8px"></div>
  <div class="pmi" data-act="reboot" style="padding:8px 12px;cursor:pointer;border-radius:6px;font-size:13.5px">🔄 Перезагрузка (5 с, принудительно)</div>
  <div class="pmi" data-act="shutdown" style="padding:8px 12px;cursor:pointer;border-radius:6px;font-size:13.5px">⏻ Выключение (5 с, принудительно)</div>
  <div class="pmi" data-act="lock" style="padding:8px 12px;cursor:pointer;border-radius:6px;font-size:13.5px">🔒 Заблокировать экран</div>
</div>
<script>
let pwrPort = 0, pwrHost = '';
function openPower(e, port, host) {
  pwrPort = port; pwrHost = host;
  const m = document.getElementById('powermenu');
  document.getElementById('pmhost2').textContent = host;
  m.style.left = Math.min(e.clientX, innerWidth - 250) + 'px';
  m.style.top = e.clientY + 'px';
  m.style.display = 'block';
}
document.addEventListener('click', e => {
  if (!e.target.closest('.pmi') && !e.target.closest('[onclick^="openPower"]')) {
    document.getElementById('powermenu').style.display = 'none';
  }
});
document.querySelectorAll('.pmi').forEach(el => {
  el.onmouseenter = () => el.style.background = '#eef2ff';
  el.onmouseleave = () => el.style.background = '';
  el.onclick = () => {
    const act = el.getAttribute('data-act');
    const q = { reboot: 'Перезагрузить', shutdown: 'Выключить', lock: 'Заблокировать экран' }[act];
    if (!confirm(q + ' машину «' + pwrHost + '»?')) return;
    document.getElementById('powermenu').style.display = 'none';
    fetch('?power=' + pwrPort + '&act=' + act, {method: 'POST'}).then(r => r.json()).then(j => {
      const r0 = j[String(pwrPort)] || {};
      alert('Команда отправлена (код ' + (r0.code ?? '?') + ')');
    }).catch(() => alert('ошибка отправки'));
  };
});
</script>

<div id="procmodal" style="display:none;position:fixed;inset:0;background:rgba(15,23,42,.55);z-index:50" onclick="if(event.target===this)closeProcs()">
  <div class="mcard" style="background:#fff;max-width:680px;margin:48px auto;border-radius:12px;padding:16px 18px;max-height:80vh;display:flex;flex-direction:column">
    <div style="display:flex;justify-content:space-between;align-items:center;gap:10px;margin-bottom:10px">
      <b>Диспетчер задач — <span id="pmhost"></span></b>
      <a href="#" onclick="closeProcs();return false" style="text-decoration:none">✕</a>
    </div>
    <input type="search" id="pmq" placeholder="поиск процесса…" oninput="pmRender()" style="padding:8px 12px;border:1px solid #e5eaf1;border-radius:8px;font-size:13.5px;margin-bottom:8px">
    <div id="pmbody" style="overflow-y:auto;flex:1"></div>
    <div style="color:#64748b;font-size:12px;margin-top:8px">✖ — завершить процесс. Память — рабочий набор. Системные процессы (PID ≤ 4) защищены.</div>
  </div>
</div>

<div id="svcmodal" style="display:none;position:fixed;inset:0;background:rgba(15,23,42,.55);z-index:50" onclick="if(event.target===this)closeSvc()">
  <div class="mcard" style="background:#fff;max-width:680px;margin:48px auto;border-radius:12px;padding:16px 18px;max-height:80vh;display:flex;flex-direction:column">
    <div style="display:flex;justify-content:space-between;align-items:center;gap:10px;margin-bottom:10px">
      <b>Службы — <span id="svchost"></span></b>
      <a href="#" onclick="closeSvc();return false" style="text-decoration:none">✕</a>
    </div>
    <input type="search" id="svq" placeholder="поиск службы…" oninput="svRender()" style="padding:8px 12px;border:1px solid #e5eaf1;border-radius:8px;font-size:13.5px;margin-bottom:8px">
    <div id="svbody" style="overflow-y:auto;flex:1"></div>
    <div style="color:#64748b;font-size:12px;margin-top:8px">▶ запуск · ■ остановка · ⟳ перезапуск</div>
  </div>
</div>

<div id="scrmodal" style="display:none;position:fixed;inset:0;background:#0d1017;z-index:60" onclick="if(event.target===this)closeScreen()">
  <div style="display:flex;flex-direction:column;height:100%;padding:10px;gap:8px">
    <div style="display:flex;justify-content:space-between;align-items:center;gap:10px;color:#e8eefc;flex-wrap:wrap">
      <b>🖥 Экран — <span id="scrhost"></span> <span style="color:#8fa1c0;font-size:12px">клик = ЛКМ · двойной клик · ПКМ = меню · текст снизу</span></b>
      <div style="display:flex;gap:8px;align-items:center">
        <input type="text" id="scrtext" placeholder="текст с клавиатуры…" style="padding:6px 10px;border:1px solid #2a3040;border-radius:8px;background:#181d28;color:#e8eefc;font-size:13px;width:260px" onkeydown="if(event.key==='Enter')scrSendText()">
        <button onclick="scrSendText()" style="padding:6px 14px;background:#4f46e5;color:#fff;border:0;border-radius:8px;cursor:pointer">Отправить (печать)</button>
        <input type="text" id="scrmsg" placeholder="сообщение пользователю…" style="padding:6px 10px;border:1px solid #2a3040;border-radius:8px;background:#181d28;color:#e8eefc;font-size:13px;width:240px" onkeydown="if(event.key==='Enter')scrToMsg()">
        <button onclick="scrToMsg()" style="padding:6px 14px;background:#0891b2;color:#fff;border:0;border-radius:8px;cursor:pointer" title="всплывающее сообщение на экране пользователя">📢 Сообщение</button>
        <button onclick="scrToClip()" style="padding:6px 14px;background:#0ea5e9;color:#fff;border:0;border-radius:8px;cursor:pointer" title="текст из поля кладётся в буфер обмена машины — Ctrl+V вставит его">📋 В буфер машины</button>
        <a href="#" onclick="closeScreen();return false" style="color:#e8eefc;text-decoration:none;font-size:18px">✕</a>
      </div>
    </div>
    <canvas id="scrimg" width="1280" height="720" style="max-width:100%;max-height:calc(100vh - 80px);cursor:crosshair;border:1px solid #2a3040;border-radius:8px;user-select:none;object-fit:contain" onmousedown="scrPress(event)" onmouseup="scrRelease(event)" onclick="scrClick(event, false)" ondblclick="scrClick(event, true)" oncontextmenu="scrRClick(event)"></canvas>
  </div>
</div>
<script>
let scrPort = 0, scrTimer = null, scrBusy = false, scrDrag = false, scrLastMove = 0, scrWheelAcc = 0, scrWheelSent = true;
let scrWs = null, scrLive = false, scrPing = null;
document.addEventListener('mousemove', scrMove);
document.addEventListener('mouseup', scrRelease);
document.addEventListener('wheel', e => {
  if (scrModalClosed()) return;
  e.preventDefault();
  scrWheelAcc -= e.deltaY;
  if (!scrWheelSent) return;
  scrWheelSent = true;
  const d = Math.round(scrWheelAcc);
  scrWheelAcc = 0;
  if (d !== 0) scrSendLine('wheel ' + d);
  setTimeout(() => { scrWheelSent = false; }, 120);
}, { passive: false });
function scrModalClosed() { return document.getElementById('scrmodal').style.display !== 'block'; }
function openScreen(port, host) {
  scrPort = port;
  document.getElementById('scrhost').textContent = host;
  document.getElementById('scrmodal').style.display = 'block';
  scrLive = false;
  scrWsConnect();
  fetchFrame();
  if (scrTimer) clearInterval(scrTimer);
  scrTimer = setInterval(fetchFrame, 1500);
}
function closeScreen() {
  clearInterval(scrTimer); scrTimer = null;
  if (scrPing) { clearInterval(scrPing); scrPing = null; }
  if (scrWs) { try { scrWs.close(); } catch (e) {} scrWs = null; }
  document.getElementById('scrmodal').style.display = 'none';
}
// WS-режим: живые кадры с relay, ввод через тот же WS; при сбое остаётся старый поллинг
function scrWsConnect() {
  const tok = (SCR_TOKS || {})[String(scrPort)];
  if (!tok) return;
  try {
    scrWs = new WebSocket('wss://' + location.host + '/hbdws/screenws?port=' + scrPort + '&t=' + tok);
  } catch (err) { return; }
  scrWs.binaryType = 'arraybuffer';
  scrWs.onmessage = ev => {
    if (typeof ev.data === 'string') return;
    if (!scrLive) {
      scrLive = true;
      if (scrTimer) { clearInterval(scrTimer); scrTimer = null; }
    }
    createImageBitmap(new Blob([ev.data], { type: 'image/jpeg' })).then(bm => {
      const c = scrImg();
      if (c.width !== bm.width || c.height !== bm.height) { c.width = bm.width; c.height = bm.height; }
      c.getContext('2d').drawImage(bm, 0, 0);
    });
  };
  scrWs.onclose = () => { scrWs = null; if (!scrModalClosed()) scrWsConnect(); };
  scrWs.onerror = () => {};
  scrPing = setInterval(() => { if (scrWs && scrWs.readyState === 1) scrWs.send('ping'); }, 3000);
}
function fetchFrame() {
  if (scrLive) return; // живой поток идёт
  if (scrBusy) return;
  scrBusy = true;
  fetch('?screen=' + scrPort).then(r => { if (!r.ok) throw 0; return r.blob(); })
    .then(b => { document.getElementById('scrimg').src = URL.createObjectURL(b); scrBusy = false; })
    .catch(() => { scrBusy = false; });
}
function scrImg() { return document.getElementById('scrimg'); }
function scrCoords(e) {
  const img = scrImg();
  const r = img.getBoundingClientRect();
  const x = Math.round((e.clientX - r.left) * img.width / r.width);
  const y = Math.round((e.clientY - r.top) * img.height / r.height);
  return x + ' ' + y;
}
function scrSendLine(line) {
  if (scrLive && scrWs && scrWs.readyState === 1) { scrWs.send(line); return; }
  fetch('?input=' + scrPort + '&line=' + encodeURIComponent(line), {method: 'POST'});
}
function scrClick(e, dbl) { scrSendLine((dbl ? 'dbl ' : 'click ') + scrCoords(e)); }
function scrPress(e) {
  e.preventDefault();
  scrDrag = true;
  scrSendLine('press ' + scrCoords(e));
}
function scrMove(e) {
  if (!scrDrag) return;
  const now = Date.now();
  if (now - scrLastMove < 120) return;
  scrLastMove = now;
  scrSendLine('move ' + scrCoords(e));
}
function scrRelease(e) {
  if (!scrDrag) return;
  scrDrag = false;
  scrSendLine('release ' + scrCoords(e));
}
function scrRClick(e) { e.preventDefault(); scrSendLine('rclick ' + scrCoords(e)); }
function scrToMsg() {
  const t = document.getElementById('scrmsg').value;
  if (!t) return;
  scrSendLine('msg ' + btoa(unescape(encodeURIComponent('Инженер поддержки: ' + t))));
  document.getElementById('scrmsg').value = '';
}
function scrToClip() {
  const t = document.getElementById('scrtext').value;
  if (!t) { alert('введи текст в поле'); return; }
  scrSendLine('clip ' + btoa(unescape(encodeURIComponent(t))));
  document.getElementById('scrtext').value = '';
}
function scrSendText() {
  const t = document.getElementById('scrtext').value;
  if (!t) return;
  scrSendLine('key ' + btoa(unescape(encodeURIComponent(t))));
  document.getElementById('scrtext').value = '';
}
</script>

<script>
let pmPort = 0, pmData = [], pmHost = '';
let svcPort = 0, svcData = [], svcHost = '';

function openProcs(port, host) {
  pmPort = port; pmHost = host;
  document.getElementById('pmhost').textContent = host;
  document.getElementById('pmbody').innerHTML = '<div style="color:#64748b">загрузка…</div>';
  document.getElementById('procmodal').style.display = 'block';
  pmLoad();
}
function closeProcs() { document.getElementById('procmodal').style.display = 'none'; }
function pmLoad() {
  fetch('?procs=' + pmPort).then(r => r.json()).then(j => {
    const out = (j[String(pmPort)] || {}).out || '';
    pmData = out.split('\n').filter(l => l.trim()).map(l => {
      const sp = l.indexOf(' ');
      const sp2 = l.indexOf(' ', sp + 1);
      return { pid: l.slice(0, sp), name: l.slice(sp + 1, sp2), ws: sp2 < 0 ? '-' : l.slice(sp2 + 1) };
    });
    pmRender();
  }).catch(() => { document.getElementById('pmbody').textContent = 'ошибка загрузки'; });
}
function pmRender() {
  const q = (document.getElementById('pmq').value || '').toLowerCase();
  const body = document.getElementById('pmbody');
  body.innerHTML = '';
  pmData.filter(p => p.name.toLowerCase().includes(q) || p.pid.includes(q)).forEach(p => {
    const row = document.createElement('div');
    row.style.cssText = 'display:flex;align-items:center;gap:10px;padding:6px 4px;border-bottom:1px solid #eef1f6';
    const nm = document.createElement('span');
    nm.style.cssText = 'flex:1;font-family:ui-monospace,monospace;font-size:13px';
    nm.textContent = p.pid + '  ' + p.name;
    const mem = document.createElement('span');
    mem.style.cssText = 'color:#64748b;font-size:12px;min-width:80px;text-align:right';
    mem.textContent = p.ws >= 0 ? (p.ws / 1048576).toFixed(1) + ' МБ' : '—';
    const btn = document.createElement('button');
    btn.textContent = '✖';
    btn.title = 'Завершить процесс ' + p.pid;
    btn.style.cssText = 'border:1px solid #fecaca;background:#fef2f2;color:#dc2626;border-radius:6px;padding:2px 10px;cursor:pointer;font-weight:700';
    btn.onclick = () => { if (confirm('Завершить процесс ' + p.name + ' (PID ' + p.pid + ')?')) pmKill(p.pid); };
    row.append(nm, mem, btn);
    body.append(row);
  });
  if (!body.children.length) body.innerHTML = '<div style="color:#64748b;padding:10px">ничего не найдено</div>';
}
function pmKill(pid) {
  fetch('?killp=' + pmPort + '&pid=' + pid, {method: 'POST'}).then(() => pmLoad());
}

function openSvc(port, host) {
  svcPort = port; svcHost = host;
  document.getElementById('svchost').textContent = host;
  document.getElementById('svbody').innerHTML = '<div style="color:#64748b">загрузка…</div>';
  document.getElementById('svcmodal').style.display = 'block';
  svLoad();
}
function closeSvc() { document.getElementById('svcmodal').style.display = 'none'; }
function svLoad() {
  fetch('?svcs=' + svcPort).then(r => r.json()).then(j => {
    const out = (j[String(svcPort)] || {}).out || '';
    svcData = out.split('\n').filter(l => l.trim()).map(l => {
      const sp1 = l.indexOf(' '), sp2 = l.indexOf(' ', sp1 + 1);
      return { name: l.slice(0, sp1), state: l.slice(sp1 + 1, sp2), disp: l.slice(sp2 + 1) };
    });
    svRender();
  }).catch(() => { document.getElementById('svbody').textContent = 'ошибка загрузки'; });
}
function svRender() {
  const q = (document.getElementById('svq').value || '').toLowerCase();
  const body = document.getElementById('svbody');
  body.innerHTML = '';
  svcData.filter(s => (s.name + ' ' + s.disp).toLowerCase().includes(q)).forEach(s => {
    const row = document.createElement('div');
    row.style.cssText = 'display:flex;align-items:center;gap:8px;padding:6px 4px;border-bottom:1px solid #eef1f6';
    const running = s.state === 'RUNNING';
    const st = document.createElement('span');
    st.style.cssText = 'font-size:11px;font-weight:700;padding:2px 8px;border-radius:999px;' + (running ? 'background:#ecfdf5;color:#059669' : 'background:#f1f5f9;color:#64748b');
    st.textContent = running ? '● работает' : 'остановлена';
    const nm = document.createElement('span');
    nm.style.cssText = 'flex:1;font-size:13px';
    nm.textContent = s.disp || s.name;
    row.append(st, nm);
    for (const act of ['start', 'stop', 'restart']) {
      const b = document.createElement('button');
      b.textContent = { start: '▶', stop: '■', restart: '⟳' }[act];
      b.title = act;
      b.style.cssText = 'border:1px solid #e5eaf1;background:#f8fafc;border-radius:6px;padding:2px 9px;cursor:pointer;font-size:12px';
      b.onclick = () => { if (confirm(act + ' — ' + (s.disp || s.name) + '?')) svcAct(act, s.name); };
      row.append(b);
    }
    body.append(row);
  });
  if (!body.children.length) body.innerHTML = '<div style="color:#64748b;padding:10px">ничего не найдено</div>';
}
function svcAct(act, name) {
  fetch('?svc=' + svcPort + '&act=' + act + '&name=' + encodeURIComponent(name), {method: 'POST'})
    .then(r => r.json()).then(() => svLoad());
}

// ---- AI Events Modal ----
let evPort = 0, evHost = '', evChannel = 'System', evRawText = '';

function openEvents(port, host) {
  evPort = port; evHost = host; evChannel = 'System';
  document.getElementById('evhost').textContent = host;
  document.getElementById('eventsmodal').style.display = 'block';
  document.getElementById('evaibox').style.display = 'none';
  evSetChannel('System');
}
function closeEvents() { document.getElementById('eventsmodal').style.display = 'none'; }

function evSetChannel(ch) {
  evChannel = ch;
  document.getElementById('evbtn-sys').className = 'tabbtn' + (ch === 'System' ? ' active' : '');
  document.getElementById('evbtn-app').className = 'tabbtn' + (ch === 'Application' ? ' active' : '');
  evLoad();
}

function evLoad() {
  const el = document.getElementById('evraw');
  el.textContent = 'чтение журнала ' + evChannel + ' через wevtutil…';
  fetch('?events=' + evPort + '&ch=' + evChannel)
    .then(r => r.text()).then(t => { try { return JSON.parse(t); } catch (e) { return { error: t || ('HTTP ' + r.status) }; } })
    .then(j => {
      evRawText = j.raw || 'нет записей';
      el.textContent = evRawText;
    })
    .catch(() => { el.textContent = 'ошибка получения событий'; });
}

function evAIAnalyze() {
  if (!evRawText || evRawText.startsWith('чтение журнала')) {
    alert('Сначала загрузите события');
    return;
  }
  const box = document.getElementById('evaibox');
  const cnt = document.getElementById('evaicontent');
  box.style.display = 'flex';
  cnt.innerHTML = '<div style="color:#7c3aed;padding:12px">🤖 Анализирую журнал ' + evChannel + ' через AI…</div>';

  const prov = localStorage.getItem('pssh_ai_prov') || 'openrouter';
  const model = localStorage.getItem('pssh_ai_model_' + prov) || (prov === 'google' ? 'gemini-2.5-flash' : 'openrouter/auto');
  const key = localStorage.getItem('pssh_ai_key_' + prov) || '';

  fetch('?ai_explain_events=1', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      provider: prov,
      model: model,
      api_key: key,
      port: evPort,
      channel: evChannel,
      events: evRawText
    })
  })
  .then(r => r.text()).then(t => { try { return JSON.parse(t); } catch (e) { return { error: t || ('HTTP ' + r.status) }; } })
  .then(j => {
    if (j.error) {
      cnt.innerHTML = '<div style="color:#dc2626">Ошибка AI: ' + j.error + '</div>';
    } else {
      cnt.innerHTML = '<div style="font-size:11.5px;color:#64748b;margin-bottom:8px">Модель: <b>' + (j.model || model) + '</b></div>' + renderMarkdown(j.analysis);
    }
  })
  .catch(err => {
    cnt.innerHTML = '<div style="color:#dc2626">Ошибка запроса к AI: ' + err + '</div>';
  });
}

// ---- AI SysAdmin Copilot ----
const aiModelsMap = {
  openrouter: [
    { id: 'openrouter/auto', name: '🤖 Авто (умная ротация free) [Рекомендуется]' },
    { id: 'openrouter/free', name: 'Free Models Router [Auto Free]' },
    { id: 'nvidia/nemotron-3.5-lightning:free', name: 'Nemotron 3.5 Lightning [Free]' },
    { id: 'recursive/flint-v1.0:free', name: 'Flint v1.0 [Free]' },
    { id: 'cohere/north-mini-code:free', name: 'Cohere North Mini Code [Free]' },
    { id: 'google/gemma-4-31b-it:free', name: 'Gemma 4 31B IT [Free]' },
    { id: 'google/gemma-4-26b-a4b-it:free', name: 'Gemma 4 26B [Free]' },
    { id: 'nvidia/nemotron-3-super-120b-a12b:free', name: 'Nemotron 3 Super 120B [Free]' },
    { id: 'nvidia/nemotron-3-ultra-550b-a55b:free', name: 'Nemotron 3 Ultra [Free]' },
    { id: 'liquid/lfm-2.5-2.6b:free', name: 'LiquidAI LFM 2.5 [Free]' },
    { id: 'thinkingmachines/inkling:free', name: 'ThinkingMachines Inkling [Free]' },
    { id: 'xingchen-agi/xing4.0-29b-a4b:free', name: 'XingChen 4.0 29B [Free]' },
    { id: 'dots-studio/dots-3-note-preview:free', name: 'Dots3 Note Preview [Free]' },
    { id: 'poolside/laguna-s-2.1:free', name: 'Poolside Laguna S [Free]' }
  ],
  google: [
    { id: 'gemini-2.5-flash', name: 'Gemini 2.5 Flash (AI Studio)' },
    { id: 'gemini-2.5-pro', name: 'Gemini 2.5 Pro (AI Studio)' },
    { id: 'gemini-flash-latest', name: 'Gemini Flash Latest' },
    { id: 'gemini-flash-lite-latest', name: 'Gemini Flash-Lite' }
  ]
};

let aiChatHistory = [];
let aiBusy = false;

function aiFetchConfig() {
  fetch('?ai_config=1')
    .then(r => r.json())
    .then(cfg => {
      if (cfg && cfg.openrouter_models && cfg.openrouter_models.length) {
        aiModelsMap.openrouter = cfg.openrouter_models.map(id => {
          let name = id;
          if (id === 'openrouter/auto') name = '🤖 Авто (умная ротация free) [Рекомендуется]';
          else if (id === 'openrouter/free') name = 'Free Models Router [Auto Free]';
          else name = id.replace(':free', ' [Free]');
          return { id: id, name: name };
        });
        aiUpdateModels();
      }
    })
    .catch(() => {});
}

function toggleAICopilot() {
  const el = document.getElementById('aicopilot');
  const isHidden = el.style.display === 'none' || el.style.display === '';
  el.style.display = isHidden ? 'flex' : 'none';
  sessionStorage.setItem('pssh_ai_open', isHidden ? '1' : '0');
  if (isHidden) {
    aiInitSelectors();
    document.getElementById('aiinput').focus();
    const box = document.getElementById('aimessages');
    box.scrollTop = box.scrollHeight;
  }
}

function aiClearChat() {
  if (confirm('Очистить историю диалога с Copilot?')) {
    aiChatHistory = [];
    sessionStorage.removeItem('pssh_ai_chat_v2');
    sessionStorage.removeItem('pssh_ai_html_v2');
    const box = document.getElementById('aimessages');
    box.innerHTML = '<div style="font-size:12.5px;color:#64748b;background:#f1f5f9;border-radius:8px;padding:10px;margin-bottom:8px">'
      + '👋 История очищена. Задайте новый вопрос о состоянии машин флота portassh!'
      + '</div>';
  }
}

function aiInitSelectors() {
  const provSel = document.getElementById('aiprov');
  const curProv = localStorage.getItem('pssh_ai_prov') || 'openrouter';
  provSel.value = curProv;
  aiUpdateModels();
}

function aiProvChanged() {
  const prov = document.getElementById('aiprov').value;
  localStorage.setItem('pssh_ai_prov', prov);
  aiUpdateModels();
}

function aiUpdateModels() {
  const prov = document.getElementById('aiprov').value;
  const modSel = document.getElementById('aimodel');
  modSel.innerHTML = '';
  const list = aiModelsMap[prov] || [];
  const savedMod = localStorage.getItem('pssh_ai_model_' + prov) || (list[0] ? list[0].id : '');
  list.forEach(m => {
    const opt = document.createElement('option');
    opt.value = m.id;
    opt.textContent = m.name;
    if (m.id === savedMod) opt.selected = true;
    modSel.appendChild(opt);
  });
  const optCustom = document.createElement('option');
  optCustom.value = '__custom__';
  optCustom.textContent = '✏️ Своя модель…';
  modSel.appendChild(optCustom);
  aiCheckKeyNotice();
}

function aiModelChanged() {
  const prov = document.getElementById('aiprov').value;
  const modSel = document.getElementById('aimodel');
  if (modSel.value === '__custom__') {
    const custom = prompt('Введите ID модели OpenRouter / Google AI:', localStorage.getItem('pssh_ai_model_' + prov) || '');
    if (custom && custom.trim()) {
      localStorage.setItem('pssh_ai_model_' + prov, custom.trim());
      aiUpdateModels();
      modSel.value = custom.trim();
    } else {
      aiUpdateModels();
    }
    return;
  }
  localStorage.setItem('pssh_ai_model_' + prov, modSel.value);
}

function aiConfigureKey() {
  const prov = document.getElementById('aiprov').value;
  const curKey = localStorage.getItem('pssh_ai_key_' + prov) || '';
  const promptName = prov === 'google' ? 'Google AI Studio API Key (GEMINI_API_KEY)' : 'OpenRouter API Key (sk-or-v1-...)';
  const key = prompt('Введите ' + promptName + ':\n(Ключ сохраняется локально в вашем браузере)', curKey);
  if (key !== null) {
    localStorage.setItem('pssh_ai_key_' + prov, key.trim());
    aiCheckKeyNotice();
  }
}

function aiCheckKeyNotice() {
  const prov = document.getElementById('aiprov').value;
  const key = localStorage.getItem('pssh_ai_key_' + prov) || '';
  const btn = document.getElementById('aikeybtn');
  btn.style.color = key ? '#10b981' : '#f59e0b';
  btn.title = key ? 'API-ключ задан (нажмите для смены)' : 'Задать API-ключ (если не задан на сервере)';
}

function aiAppendMsg(role, html, toolsUsed) {
  const box = document.getElementById('aimessages');
  const div = document.createElement('div');
  div.style.cssText = 'margin:6px 0;max-width:92%;' + (role === 'user' ? 'margin-left:auto;text-align:right' : 'margin-right:auto;text-align:left');
  
  let toolsHtml = '';
  if (toolsUsed && toolsUsed.length) {
    toolsHtml = '<div style="font-size:11px;color:#6d28d9;background:#f5f3ff;border:1px solid #ddd6fe;border-radius:6px;padding:4px 8px;margin-bottom:6px;text-align:left">'
      + '🛠 <b>Выполнены инструменты:</b><ul style="margin:2px 0 2px 16px;padding:0">'
      + toolsUsed.map(t => '<li><code>' + t.tool + '</code>(' + (t.args ? t.args.slice(0, 40) : '') + ')</li>').join('')
      + '</ul></div>';
  }

  const bubble = document.createElement('div');
  bubble.style.cssText = 'display:inline-block;max-width:100%;padding:8px 12px;border-radius:10px;font-size:13px;line-height:1.45;text-align:left;overflow-wrap:anywhere;word-break:break-word;'
    + (role === 'user' ? 'background:#4f46e5;color:#fff;border-bottom-right-radius:2px' : 'background:#f1f5f9;color:#0f172a;border-bottom-left-radius:2px;border:1px solid #e2e8f0');
  bubble.innerHTML = toolsHtml + html;
  div.appendChild(bubble);
  box.appendChild(div);
  box.scrollTop = box.scrollHeight;

  // Сохраняем состояние чата в sessionStorage
  try {
    sessionStorage.setItem('pssh_ai_html_v2', box.innerHTML);
    sessionStorage.setItem('pssh_ai_chat_v2', JSON.stringify(aiChatHistory));
  } catch(e) {}
}

function aiSend(text) {
  if (aiBusy) return;
  const input = document.getElementById('aiinput');
  text = text || (input ? input.value : '');
  if (!text || !text.trim()) return;
  if (input) input.value = '';

  aiAppendMsg('user', text.replace(/</g, '&lt;').replace(/>/g, '&gt;'));
  aiChatHistory.push({ role: 'user', content: text });

  const prov = document.getElementById('aiprov').value;
  const modSel = document.getElementById('aimodel');
  const model = modSel.value === '__custom__' ? (localStorage.getItem('pssh_ai_model_' + prov) || '') : modSel.value;
  const key = localStorage.getItem('pssh_ai_key_' + prov) || '';

  const loadDiv = document.createElement('div');
  loadDiv.id = 'ailoading';
  loadDiv.style.cssText = 'font-size:12px;color:#7c3aed;margin:6px 0;display:flex;align-items:center;gap:6px';
  loadDiv.innerHTML = '<span class="dot"></span> Copilot думает и опрашивает флот…';
  document.getElementById('aimessages').appendChild(loadDiv);
  document.getElementById('aimessages').scrollTop = document.getElementById('aimessages').scrollHeight;

  aiBusy = true;
  fetch('?ai_chat=1', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      provider: prov,
      model: model,
      api_key: key,
      messages: aiChatHistory
    })
  })
  .then(r => r.text()).then(t => { try { return JSON.parse(t); } catch (e) { return { error: t || ('HTTP ' + r.status) }; } })
  .then(j => {
    loadDiv.remove();
    aiBusy = false;
    if (j.error) {
      aiAppendMsg('assistant', '<span style="color:#dc2626">Ошибка: ' + j.error + '</span>');
    } else {
      aiChatHistory.push({ role: 'assistant', content: j.reply });
      aiAppendMsg('assistant', renderMarkdown(j.reply), j.tools_used);
    }
  })
  .catch(err => {
    loadDiv.remove();
    aiBusy = false;
    aiAppendMsg('assistant', '<span style="color:#dc2626">Сетевой сбой: ' + err + '</span>');
  });
}

function aiQuickChip(promptText) {
  aiSend(promptText);
}

function renderMarkdown(md) {
  if (!md) return '';
  md = md
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  // markdown-таблицы → HTML: фиксированная ширина, перенос внутри ячеек
  md = md.replace(/(^\|.+\|\s*$\n?)+/gm, block => {
    const rows = block.trim().split('\n').map(r => r.trim()).filter(r => r.startsWith('|'));
    if (rows.length < 2) return block;
    const cells = r => r.replace(/^\||\|$/g, '').split('|').map(c => c.trim());
    let html = '<table style="width:100%;table-layout:fixed;border-collapse:collapse;font-size:12px;margin:6px 0">';
    rows.forEach((r, i) => {
      const cs = cells(r);
      if (i > 0 && cs.every(c => /^:?-{2,}:?$/.test(c) || c === '')) return; // сепаратор |---|
      const tag = i === 0 ? 'th' : 'td';
      html += '<tr>' + cs.map(c => '<' + tag + ' style="border:1px solid #cbd5e1;padding:4px 6px;text-align:left;vertical-align:top;overflow-wrap:anywhere;word-break:break-word">' + c + '</' + tag + '>').join('') + '</tr>';
    });
    return html + '</table>';
  });
  return md
    .replace(/```([\s\S]*?)```/g, '<pre style="background:#0f172a;color:#f8fafc;padding:8px 10px;border-radius:6px;font-size:12px;overflow-x:auto;margin:6px 0"><code>$1</code></pre>')
    .replace(/`([^`]+)`/g, '<code style="background:#f1f5f9;color:#e11d48;padding:2px 5px;border-radius:4px;font-size:12px">$1</code>')
    .replace(/\*\*([^*]+)\*\*/g, '<b>$1</b>')
    .replace(/\*([^*]+)\*/g, '<i>$1</i>')
    .replace(/\n\n/g, '<br><br>')
    .replace(/\n- /g, '<br>• ');
}

// Бесшовное фоновое AJAX-обновление таблицы и статистики без перезагрузки страницы
let ajaxRefreshing = false;
async function doAjaxRefresh() {
  if (ajaxRefreshing) return;
  ajaxRefreshing = true;
  try {
    const res = await fetch(window.location.href);
    if (!res.ok) { ajaxRefreshing = false; return; }
    const html = await res.text();
    const doc = new DOMParser().parseFromString(html, 'text/html');

    // 1. Счетчик активных в шапке
    const newBadge = doc.querySelector('.badge-live');
    const curBadge = document.querySelector('.badge-live');
    if (newBadge && curBadge) curBadge.innerHTML = newBadge.innerHTML;

    // 2. Карточки сводки
    const newCards = doc.querySelector('.cards');
    const curCards = document.querySelector('.cards');
    if (newCards && curCards) curCards.innerHTML = newCards.innerHTML;

    // 3. Список машин (мастер) с сохранением отмеченных чекбоксов
    const curList = document.querySelector('#mdlist');
    const newList = doc.querySelector('#mdlist');
    if (curList && newList) {
      const checkedSet = new Set(
        Array.from(curList.querySelectorAll('input[name="sel[]"]:checked')).map(cb => cb.value)
      );
      curList.innerHTML = newList.innerHTML;
      curList.querySelectorAll('input[name="sel[]"]').forEach(cb => {
        if (checkedSet.has(cb.value)) cb.checked = true;
      });
      const qInput = document.getElementById('q');
      if (qInput && qInput.value) {
        filterRows(qInput.value);
      }
      if (typeof showProblemsOnly !== 'undefined' && showProblemsOnly) {
        document.querySelectorAll('.mditem[data-search]').forEach(it => {
          const p = parseInt(it.getAttribute('data-problems') || '0');
          it.style.display = (p === 0) ? 'none' : '';
        });
      }
      if (typeof updateProbBtn === 'function') updateProbBtn();
      if (typeof highlightSel === 'function') highlightSel();
    }

    // 3b. Панель выбранной машины (деталь)
    const curPane = document.querySelector('#mdpane');
    const newPane = doc.querySelector('#mdpane');
    if (curPane && newPane) curPane.innerHTML = newPane.innerHTML;

    // 4. Инвентарь (только если поиск по инвентарю не активен)
    const invQ = document.getElementById('invq');
    if (!invQ || !invQ.value) {
      const newInv = doc.querySelector('#tab-inv');
      const curInv = document.querySelector('#tab-inv');
      if (newInv && curInv) curInv.innerHTML = newInv.innerHTML;
    }

    // 5. Статус синхронизации в шапке
    const syncEl = document.getElementById('syncstatus');
    if (syncEl) {
      syncEl.innerHTML = '<span style="color:#10b981;font-weight:600">● AJAX ' + new Date().toLocaleTimeString() + '</span>';
    }
  } catch(e) {
    console.warn('AJAX sync warning:', e);
  } finally {
    ajaxRefreshing = false;
  }
}

window.addEventListener('DOMContentLoaded', () => {
  // 1. Восстанавливаем сообщения и историю чата
  try {
    const savedHtml = sessionStorage.getItem('pssh_ai_html_v2');
    if (savedHtml) {
      document.getElementById('aimessages').innerHTML = savedHtml;
    }
    const savedChat = sessionStorage.getItem('pssh_ai_chat_v2');
    if (savedChat) {
      aiChatHistory = JSON.parse(savedChat);
    } else {
      // приветствие: Copilot сразу объясняет, кто он и что умеет
      aiAppendMsg('assistant',
        '👋 Я <b>SysAdmin Copilot</b> — ИИ-помощник системы portassh. Диагностирую машины парка и выполняю действия по твоей просьбе.<br>' +
        '<b>Умею:</b> смотреть список машин, инвентарь (железо/софт/антивирус), процессы, службы, журналы событий, ' +
        'выполнять команды, снимать зависшие процессы и управлять службами.<br>' +
        'Просто опиши проблему — например: <i>«почему у Гидрокор тормозит диск?»</i> или <i>«покажи ошибки журнала у машины на 2202»</i>.');
    }
  } catch(e) {}

  // 2. Восстанавливаем открытое состояние Copilot
  if (sessionStorage.getItem('pssh_ai_open') === '1') {
    const el = document.getElementById('aicopilot');
    el.style.display = 'flex';
    aiInitSelectors();
    const box = document.getElementById('aimessages');
    box.scrollTop = box.scrollHeight;
  }

  // 3. Загружаем полный список моделей OpenRouter
  aiFetchConfig();

  // 4. Запускаем периодическую AJAX-синхронизацию каждые 25 секунд
  setInterval(doAjaxRefresh, 25000);

  // 5. Живая шина событий Server-Sent Events (SSE) для мгновенного обновления
  if (window.EventSource) {
    try {
      const sse = new EventSource('?sse=1');
      sse.addEventListener('heartbeat', ev => {
        try {
          const d = JSON.parse(ev.data);
          if (d && d.host) {
            const row = document.querySelector(`.mditem[data-host="${d.host}"]`);
            if (row) {
              const sub = row.querySelector('.mdsub');
              if (sub) {
                const ramTxt = (d.ram !== undefined && d.ram >= 0) ? ` · ОЗУ ${d.ram}%` : '';
                sub.textContent = `порт ${d.port} · только что${ramTxt}`;
              }
            }
            const syncEl = document.getElementById('syncstatus');
            if (syncEl) syncEl.innerHTML = '<span style="color:#10b981;font-weight:600">● Live SSE ' + new Date().toLocaleTimeString() + '</span>';
          }
        } catch(e) {}
      });
    } catch(e) {}
  }
});
</script>

<!-- Модальное окно журнала событий Windows -->
<div id="eventsmodal" style="display:none;position:fixed;inset:0;background:rgba(15,23,42,.55);z-index:60" onclick="if(event.target===this)closeEvents()">
  <div style="background:#fff;max-width:860px;margin:32px auto;border-radius:12px;padding:18px 20px;max-height:88vh;display:flex;flex-direction:column;box-shadow:0 16px 40px rgba(0,0,0,.25)">
    <div style="display:flex;justify-content:space-between;align-items:center;gap:10px;margin-bottom:12px">
      <b>📋 Журнал событий Windows — <span id="evhost"></span></b>
      <a href="#" onclick="closeEvents();return false" style="text-decoration:none;font-size:18px">✕</a>
    </div>
    <div style="display:flex;justify-content:space-between;align-items:center;gap:10px;margin-bottom:10px;flex-wrap:wrap">
      <div style="display:flex;gap:6px">
        <button id="evbtn-sys" class="tabbtn active" style="padding:4px 12px;font-size:12.5px" onclick="evSetChannel('System')">System (Система)</button>
        <button id="evbtn-app" class="tabbtn" style="padding:4px 12px;font-size:12.5px" onclick="evSetChannel('Application')">Application (Приложения)</button>
      </div>
      <div style="display:flex;gap:8px;align-items:center">
        <button class="btn sm" onclick="evLoad()">⟳ Обновить</button>
        <button class="btn sm" style="background:#7c3aed;color:#fff" onclick="evAIAnalyze()">🤖 AI-Анализ сбоев</button>
      </div>
    </div>
    <div style="display:flex;gap:14px;flex:1;overflow:hidden">
      <div style="flex:1;display:flex;flex-direction:column;min-width:0">
        <div style="font-size:12px;color:#64748b;margin-bottom:4px">События (wevtutil: Critical / Error):</div>
        <pre id="evraw" style="flex:1;background:#0f172a;color:#f8fafc;padding:12px;border-radius:8px;font-size:11px;overflow-y:auto;white-space:pre-wrap;margin:0"></pre>
      </div>
      <div id="evaibox" style="flex:1.1;display:none;flex-direction:column;border-left:1px solid #e2e8f0;padding-left:14px;min-width:0">
        <div style="font-size:12px;color:#7c3aed;font-weight:700;margin-bottom:4px">Диагноз и рекомендации AI:</div>
        <div id="evaicontent" style="flex:1;overflow-y:auto;font-size:13px;line-height:1.55;color:#1e293b;padding-right:4px"></div>
      </div>
    </div>
  </div>
</div>

<!-- Плавающий AI SysAdmin Copilot -->
<div id="aicopilot" style="display:none;position:fixed;bottom:20px;right:20px;width:min(440px,calc(100vw - 24px));height:min(600px,calc(100vh - 40px));background:#fff;border:1px solid #cbd5e1;border-radius:14px;box-shadow:0 16px 40px rgba(15,23,42,.22);z-index:90;flex-direction:column;overflow:hidden">
  <div style="background:#4f46e5;color:#fff;padding:10px 14px;display:flex;justify-content:space-between;align-items:center">
    <div style="font-weight:700;font-size:14px">🤖 AI SysAdmin Copilot</div>
    <div style="display:flex;gap:10px;align-items:center">
      <button onclick="aiClearChat()" style="background:none;border:none;cursor:pointer;font-size:14px;color:#e0e7ff" title="Очистить историю диалога">🗑</button>
      <button id="aikeybtn" onclick="aiConfigureKey()" style="background:none;border:none;cursor:pointer;font-size:15px;color:#f59e0b" title="Настройки API-ключа">🔑</button>
      <a href="#" onclick="toggleAICopilot();return false" style="color:#fff;text-decoration:none;font-size:16px">✕</a>
    </div>
  </div>
  <div style="background:#f8fafc;padding:6px 12px;border-bottom:1px solid #e2e8f0;display:flex;gap:6px;align-items:center">
    <select id="aiprov" onchange="aiProvChanged()" style="font-size:11.5px;padding:3px 6px;border-radius:6px;border:1px solid #cbd5e1;background:#fff">
      <option value="openrouter">OpenRouter (Free models)</option>
      <option value="google">Google AI Studio (Gemini)</option>
    </select>
    <select id="aimodel" onchange="aiModelChanged()" style="font-size:11.5px;padding:3px 6px;border-radius:6px;border:1px solid #cbd5e1;background:#fff;flex:1;min-width:0">
    </select>
  </div>
  <div id="aimessages" style="flex:1;min-height:0;overflow-y:auto;overflow-x:hidden;padding:12px;display:flex;flex-direction:column;background:#fafafa">
    <div style="font-size:12.5px;color:#64748b;background:#f1f5f9;border-radius:8px;padding:10px;margin-bottom:8px">
      👋 Привет! Я ваш AI-ассистент по флоту portassh. У меня есть доступ к процессам, службам, дискам, инвентарю и ошибкам журналов Windows. Спросите меня о состоянии машин!
    </div>
  </div>
  <div style="padding:4px 10px;background:#f8fafc;border-top:1px solid #e2e8f0;display:flex;gap:6px;overflow-x:auto;white-space:nowrap">
    <button onclick="aiQuickChip('Проверь состояние всех активных машин')" style="font-size:11px;background:#fff;border:1px solid #cbd5e1;border-radius:999px;padding:2px 8px;cursor:pointer">🩺 Проверить все ПК</button>
    <button onclick="aiQuickChip('У кого меньше 5 ГБ на диске C:?')" style="font-size:11px;background:#fff;border:1px solid #cbd5e1;border-radius:999px;padding:2px 8px;cursor:pointer">💾 Мало места</button>
    <button onclick="aiQuickChip('Проверь системные ошибки в журнале System')" style="font-size:11px;background:#fff;border:1px solid #cbd5e1;border-radius:999px;padding:2px 8px;cursor:pointer">📋 Ошибки Windows</button>
  </div>
  <div style="padding:8px 10px;border-top:1px solid #e2e8f0;background:#fff;display:flex;gap:6px">
    <input type="text" id="aiinput" placeholder="Задайте вопрос о машинах флота…" style="flex:1;padding:6px 10px;border:1px solid #cbd5e1;border-radius:8px;font-size:13px" onkeydown="if(event.key==='Enter')aiSend()">
    <button onclick="aiSend()" style="background:#4f46e5;color:#fff;border:none;border-radius:8px;padding:6px 14px;cursor:pointer;font-weight:700;font-size:13px">Отправить</button>
  </div>
</div>
</body>
</html>
