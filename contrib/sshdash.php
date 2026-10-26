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
$PRIVATE     = '/home/user/web/itstandart.kz/private'; // каталог артефактов (реестр, сборки, ключ)
$REGISTRY    = "$PRIVATE/portassh-online.txt";
$bcmdSaved   = '';
$broadcastHtml = '';
$ALIASES_F   = "$PRIVATE/portassh-aliases.json";
$LATEST_RAW  = trim((string) @file_get_contents("$PRIVATE/portassh-version.txt"));
$LATEST_VER  = preg_split('/\s+/', $LATEST_RAW)[0] ?? '';
$LATEST_NAME = preg_split('/\s+/', $LATEST_RAW)[1] ?? '';
$EXE         = "$PRIVATE/portassh.exe"; // сборка для скачивания (копируется cron'ом)
$RELAY       = '5.35.81.198'; // IP/домен relay-сервера для колонки «Команда подключения»
$ACTIVE_SECS = 180;           // heartbeat у exe раз в 60 с — запас ×3
$MAX_AGE     = 7 * 86400;     // записи старше недели игнорируются вовсе

// PHP-TZ может отличаться от системного (машине пишут локальное время) —
// калибруем mktime-эпохи по системным `date +%s` / `date +%H:%M`
$sysNow = (int) trim((string) @shell_exec('date +%s'));
[$sh, $sm] = explode(':', trim((string) @shell_exec('date +%H:%M')));
$SKEW = $sysNow - mktime((int)$sh, (int)$sm, 0);

header('Content-Type: text/html; charset=utf-8');
header('Cache-Control: no-store');

// метки и заметки машин: JSON рядом с реестром, редактируется из интерфейса
if (isset($_GET['setalias'])) {
    $h = preg_replace('/[^A-Za-z0-9._-]/', '', (string) $_GET['setalias']);
    if ($h !== '') {
        $name = mb_substr(trim((string) ($_GET['name'] ?? '')), 0, 60);
        $note = mb_substr(trim((string) ($_GET['note'] ?? '')), 0, 200);
        $al = json_decode((string) @file_get_contents($ALIASES_F), true);
        if (!is_array($al)) { $al = []; }
        if ($name === '' && $note === '') {
            unset($al[$h]);
        } else {
            $al[$h] = ['name' => $name, 'note' => $note];
        }
        file_put_contents($ALIASES_F, json_encode($al, JSON_UNESCAPED_UNICODE | JSON_PRETTY_PRINT), LOCK_EX);
    }
    header('Location: /sshdash');
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
if (isset($_GET['broadcast'])) {
    set_time_limit(120);
        $bcmd = (string) ($_GET['bcmd'] ?? '');
    $bports = array_values(array_filter(array_map('intval', explode(',', (string) ($_GET['bports'] ?? ''))),
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

// Ввод на машине: клики и текст
if (isset($_GET['input'])) {
    $p = (int) $_GET['input'];
    $line = (string) ($_GET['line'] ?? '');
    if ($p < 1024 || $line === '' || mb_strlen($line) > 4096) { http_response_code(404); exit; }
    header('Content-Type: application/json');
    echo @file_get_contents('http://127.0.0.1:8897/input?port=' . $p,
        false, stream_context_create(['http' => ['method' => 'POST', 'timeout' => 10, 'content' => $line]]));
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
start "portassh-tunnel" /min ssh -C -L {$rdpRelay}:127.0.0.1:{$rdpRelay} support@5.35.81.198
timeout /t 2 >nul
mstsc /shadow:{$sid} /v:localhost:{$rdpRelay} /control
BAT;
    echo $bat;
    exit;

}

// принудительный update-check на клиентах: пишем таймстамп-флаг,
// exe проверяет его при каждом heartbeat (раз в минуту)
if (isset($_GET['force'])) {
    file_put_contents($PRIVATE . '/portassh-force.txt', (string) time());
    header('Location: /sshdash');
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
$lines = @file($REGISTRY, FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES) ?: [];
$latest = [];
foreach ($lines as $line) {
    $p = explode(' ', trim($line));
    if (count($p) < 4) continue;
    [$host, $port, $t, $role] = $p;
    $user = isset($p[4]) && $p[4] !== '-' ? $p[4] : '';
    $osv  = isset($p[5]) && $p[5] !== '-' ? $p[5] : '';
    $disk = isset($p[6]) && (int)$p[6] >= 0 ? (int)$p[6] : null;
    $ram  = isset($p[7]) && (int)$p[7] >= 0 ? (int)$p[7] : null;
    $ver  = isset($p[8]) && $p[8] !== '-' ? $p[8] : '';
    $alias = isset($aliases[$host]) ? $aliases[$host] : null;
    $meta = [];
    if ($user !== '')  { $meta[] = '👤 ' . htmlspecialchars($user); }
    if ($osv !== '')   { $meta[] = 'OS ' . htmlspecialchars($osv); }
    if ($disk !== null){ $meta[] = '💾 C: ' . $disk . ' GB'; }
    if ($ram !== null) { $meta[] = '🧠 RAM ' . $ram . '%'; }
    $verOk = false;
    if ($ver !== '') {
        $verOk = ($LATEST_NAME !== '' && $ver === $LATEST_NAME);
        $meta[] = $verOk
            ? '<span class="chip ver-ok">ver. ' . htmlspecialchars($ver) . '</span>'
            : ($LATEST_NAME !== ''
                ? '<span class="chip ver-old">устарела → ver. ' . htmlspecialchars($LATEST_NAME) . '</span>'
                : '<span class="chip">v ' . htmlspecialchars($ver) . '</span>');
    }
    if (!in_array($role, ['user', 'admin'], true)) continue;
    [$dmy, $hm] = explode('_', $t);
    [$dd, $mm] = explode('.', $dmy);
    $ts = mktime((int)substr($hm, 0, 2), (int)substr($hm, 3, 2), 0, (int)$mm, (int)$dd) + $SKEW;
    if ($ts - $sysNow > 86400) $ts = strtotime('-1 year', $ts) + $SKEW;
    if ($sysNow - $ts > $MAX_AGE) continue;
    $key = $host . '|' . $role;
    if (!isset($latest[$key]) || $latest[$key]['ts'] < $ts) {
        $latest[$key] = compact('host', 'port', 'role', 'ts', 't', 'user', 'osv', 'disk', 'ram', 'ver', 'verOk', 'alias', 'meta');
    }
}
$active = array_filter($latest, fn($e) => $sysNow - $e['ts'] <= $ACTIVE_SECS);
usort($active, fn($a, $b) => $b['ts'] <=> $a['ts']);
$bcmdSaved = isset($_GET['broadcast']) ? (string) ($_GET['bcmd'] ?? '') : '';
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
?>
<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="20">
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
  .chip.ver-ok { background: #ecfdf5; color: #059669; font-weight: 700; }
  .chip.ver-old { background: #fffbeb; color: #b45309; font-weight: 700; }
  .role-pill { display: inline-block; padding: 2px 10px; border-radius: 999px; font-size: 12px; font-weight: 600; }
  .role-admin { background: #fff7ed; color: #c2410c; }
  .role-user  { background: #eef2ff; color: #4f46e5; }
  .ago { color: var(--muted); font-size: 12.5px; white-space: nowrap; }
  .cmd { font-family: ui-monospace, Consolas, monospace; font-size: 12.5px; color: #334155; user-select: all; word-break: break-all; }
  .btnrow a { margin-right: 6px; margin-bottom: 4px; }
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
</style>
</head>
<body>
<div class="wrap">
<script>const INV_ALL_PORTS = <?= json_encode(array_map(fn($e) => (int) $e['port'], array_values($active))) ?>;</script>
<script>
function editAlias(host, cur, note) {
  const n = prompt('Имя машины (оставить пустым = удалить метку):', cur || '');
  if (n === null) return;
  const t = prompt('Заметка (необязательно):', note || '');
  if (t === null) return;
  location.href = '/sshdash?setalias=' + encodeURIComponent(host)
    + '&name=' + encodeURIComponent(n) + '&note=' + encodeURIComponent(t);
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
  document.querySelectorAll('tr[data-search]').forEach(tr => {
    tr.style.display = tr.getAttribute('data-search').includes(q) ? '' : 'none';
  });
}
</script>

<header class="top">
  <div>
    <h1>portassh <span class="badge-live">● <?= count($active) ?> активных</span></h1>
    <div class="sub">пульт дежурного · автообновление 20 с · актуальная сборка <b><?= $LATEST_NAME !== '' ? 'ver. ' . htmlspecialchars($LATEST_NAME) : '?' ?></b></div>
  </div>
  <div class="actions">
    <a class="btn" href="?dl=zip">⬇ .zip</a>
    <a class="btn" href="?dl=bin">⬇ .bin</a>
    <a class="btn" href="?dl=key">🔑 Ключ</a>
    <a class="btn amber" href="?force=1" title="клиенты проверят обновление в течение минуты">⟳ Обновить клиентов</a>
  </div>
</header>

<div class="tabs">
  <button id="tb-m" class="tabbtn active" onclick="showTab('m')">🖥 Машины</button>
  <button id="tb-i" class="tabbtn" onclick="showTab('i')">📦 Инвентарь</button>
</div>

<div id="tab-machines">
<section class="cards">
  <div class="card"><div class="num g"><?= count($active) ?></div><div class="lbl">активных машин</div></div>
  <div class="card"><div class="num a"><?= $staleActive ?></div><div class="lbl">требуют обновления</div></div>
  <div class="card"><div class="num i"><?= count(array_filter($active, fn($e) => $e['role'] === 'admin')) ?></div><div class="lbl">админских сессий</div></div>
  <div class="card"><div class="num"><?= count($aliases) ?></div><div class="lbl">меток в справочнике</div></div>
</section>

<div class="panel">
  <div class="panel-head">
    <h2>Машины</h2>
    <input type="search" id="q" placeholder="фильтр: имя, хост, пользователь…" oninput="filterRows(this.value)">
  </div>
  <form method="get" action="/sshdash">
  <table>
  <tr><th style="width:34px"><input type="checkbox" onchange="document.querySelectorAll('input[name=sel[]]').forEach(c=>c.checked=this.checked)"></th><th>Машина</th><th>Роль</th><th>Порт</th><th>Действия</th><th>Heartbeat</th></tr>
  <?php
  if (!$active) {
      echo '<tr><td colspan="6" class="empty">Нет активных машин — запустите portassh.exe на целевом компьютере</td></tr>';
  }
  foreach ($active as $e) {
      $sec = $sysNow - $e['ts'];
      $seen = $sec < 5 ? 'только что' : $sec . ' с назад';
      $cmd = "ssh -C -i portassh_client.key -o StrictHostKeyChecking=accept-new -J support@{$GLOBALS['RELAY']} -p {$e['port']} anyuser@localhost";
      $alias = $e['alias'];
      $meta = $e['meta'];
      if ($alias !== null && $alias['name'] !== '') {
          array_unshift($meta, '🖥 ' . htmlspecialchars($e['host']));
      }
      if ($alias !== null && $alias['note'] !== '') {
          $meta[] = '📝 ' . htmlspecialchars($alias['note']);
      }
      $metaHtml = $meta ? '<div class="meta">' . implode(' · ', $meta) . '</div>' : '';
      $dispName = ($alias !== null && $alias['name'] !== '') ? $alias['name'] : $e['host'];
      $search = mb_strtolower($dispName . ' ' . $e['host'] . ' ' . ($e['user'] ?? '') . ' ' . $e['role']);
      $editBtn = '<a href="#" onclick="editAlias(\'' . htmlspecialchars($e['host']) . '\', '
               . htmlspecialchars(json_encode($alias !== null ? $alias['name'] : '')) . ', '
               . htmlspecialchars(json_encode($alias !== null ? $alias['note'] : '')) . ');return false" title="метка/заметка">✏️</a>';
      $btnHtml = '<a class="btn sm primary" href="https://itstandart.kz:8899/wsterm?port=' . (int) $e['port'] . '" target="_blank">🖥 Терминал</a>'
               . ' <a class="btn sm sky" href="?rdp=' . (int) $e['port'] . '" title="скачать .rdp">🖥️ RDP</a>'
               . ' <a class="btn sm" href="#" onclick="openProcs(' . (int) $e['port'] . ', \'' . htmlspecialchars($dispName) . '\');return false" title="диспетчер задач">📊 Процессы</a>'
               . ' <a class="btn sm" href="?shadow=' . (int) $e['port'] . '" title="совместный экран">👥 Тень</a>'
               . ' <a class="btn sm" href="#" onclick="openScreen(' . (int) $e['port'] . ', \'' . htmlspecialchars($dispName) . '\');return false" title="экран в браузере">🖼 Экран</a>'
               . ' <a class="btn sm copy" href="#" onclick="copyCmd(' . htmlspecialchars(json_encode($cmd)) . ', this);return false">⧉ команда</a>';
      printf(
          '<tr data-search="%s"><td><input type="checkbox" name="sel[]" value="%d"></td>'
          . '<td><span class="dot"></span><span class="mname">%s</span>%s%s</td>'
          . '<td><span class="role-pill role-%s">%s</span></td>'
          . '<td><b>%d</b></td>'
          . '<td class="btnrow">%s</td>'
          . '<td class="ago">%s</td></tr>' . "\n",
          htmlspecialchars($search),
          (int) $e['port'],
          htmlspecialchars($dispName),
          $metaHtml,
          $editBtn,
          $e['role'],
          $e['role'] === 'admin' ? 'админ' : 'пользователь',
          (int) $e['port'],
          $btnHtml,
          htmlspecialchars($seen)
      );
  }
  ?>
  </table>
  <div class="bwrap">
    <input type="hidden" name="broadcast" value="1">
    <input type="text" name="bcmd" placeholder="команда для выбранных машин, напр. sys:disks" value="<?= htmlspecialchars($bcmdSaved) ?>">
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
<summary>📖 Описание и полная инструкция — раскрыть</summary>

<h2>Что это</h2>
<p>portassh — инструмент удалённого доступа к компьютерам через SSH-туннель. На целевом
компьютере запускается <b>portassh.exe</b>: он сам прокладывает туннель через relay-сервер
(VPS 5.35.81.198) и держит его открытым. Никаких пробросок портов на роутере и настроек
сети не нужно — машина доступна, пока на ней запущен exe, даже из другой страны.</p>
<p>Эта страница — пульт дежурного: показывает <b>только активные</b> машины (экземпляр
portassh подтверждает туннель каждую минуту; машина исчезает из списка
сразу при штатном выключении/перезагрузке (exe сообщает об этом сам) или
через ~3 минуты при резком отключении — так видно разницу между «выключена»
и «зависла»). Обновление — каждые 20 секунд.</p>

<h2>Установка на целевом компьютере (Windows)</h2>
<ol>
<li>Нажмите «⬇ .zip» выше.</li>
<li>Распакуйте архив. Пароль архива: <span class="pw">RtnM91jK.!</span></li>
<li>Запустите <span class="cmd">portassh.exe</span> двойным кликом. Окна нет — так и
задумано: программа работает в фоне.</li>
</ol>
<p>Готово — через минуту компьютер появится в таблице выше.</p>
<p style="font-size:12.5px;color:var(--muted)">Если Windows покажет синее окно (SmartScreen):
«Подробнее» → «Выполнить в любом случае».
Если антивирус удалил файл: добавьте portassh.exe в исключения антивируса и скачайте заново.</p>

<h2>Подключение с другого компьютера (Mac / Linux / Windows с OpenSSH)</h2>
<ol>
<li><b>Один раз:</b> нажмите «🔑 Ключ» выше — сохранится файл
<span class="cmd">portassh_client.key</span> (обычно в папку «Загрузки»).</li>
<li>Откройте терминал в той же папке:
<ul>
<li>Windows: Win+R → <span class="cmd">cmd</span> → <span class="cmd">cd %USERPROFILE%\Downloads</span></li>
<li>Mac/Linux: <span class="cmd">cd ~/Downloads</span></li>
</ul></li>
<li>Скопируйте команду нужной машины из таблицы и вставьте в терминал.</li>
</ol>
<p>Всё — вы на удалённом компьютере. С ключом пароль не спросится
(пароль — запасной вариант, если ключа нет).</p>
<p>Быстрая диагностика без шелла (мгновенно, не трогает cmd.exe):
<span class="cmd">sys:whoami</span>,
<span class="cmd">sys:hostname</span>,
<span class="cmd">sys:procs</span> (процессы),
<span class="cmd">sys:svcs</span> (службы),
<span class="cmd">sys:disks</span> (место на дисках) —
дописываются вместо команды: <span class="cmd">…anyuser@localhost "sys:disks"</span>.</p>

<h2>RDP — удалённый рабочий стол</h2>
<ol>
<li>Проверьте, что RDP на машине включён (машина Pro). Если нет — подключитесь
по SSH и выполните в админском сеансе:
<span class="cmd">reg add "HKLM\SYSTEM\CurrentControlSet\Control\Terminal Server" /v fDenyTSConnections /t REG_DWORD /d 0 /f &amp;&amp; netsh advfirewall firewall add rule name="RDP" dir=in action=allow protocol=tcp localport=3389</span>
(Windows 10 Home: дополнительно нужен RDP Wrapper — штатного RDP-хоста в Home нет.)</li>
<li>Постройте туннель для RDP (порт машины + 10000):
<span class="cmd">ssh -C -L 12200:127.0.0.1:12200 support@5.35.81.198</span>
(для машины с портом 2200; цифру замените на порт из таблицы)</li>
<li>Скачайте кнопкой «🖥️ RDP» файл и откройте его — подключение пойдёт
на localhost:12200 через туннель.</li>
</ol>

<p>Ещё две подсистемы:
<span class="cmd">ssh -s info …</span> — мгновенный JSON-паспорт машины
(модель, серийник-гид, CPU/RAM, диски, юзер, сетевые адаптеры);
<span class="cmd">ssh -s logs …</span> — живой стриминг portassh.log
(аналог tail -f, без скачивания файла; Ctrl+C — остановить).</p>
<p style="font-size:12.5px;color:var(--muted)">Команду можно запускать из любой папки, если
в ней лежит portassh_client.key. Храните ключ только у себя — он заменяет пароль.</p>
<p>Команда из таблицы выглядит так:</p>
<p class="cmd">ssh -C -i portassh_client.key -o StrictHostKeyChecking=accept-new -J support@5.35.81.198 -p &lt;ПОРТ&gt; anyuser@localhost</p>
<p>Если ключа нет (или он не подхватился), подключение попросит пароль <b>два раза</b> —
оба раза вводится один и тот же пароль (резервный вход):</p>
<table class="pwtab">
<tr><th>Запрос</th><th>Логин</th><th>Пароль</th></tr>
<tr><td>1. Прыжок через relay-сервер (support@…)</td><td>—</td><td class="pw">RtnM91jK.!</td></tr>
<tr><td>2. Целевая машина (anyuser@localhost)</td><td>—</td><td class="pw">RtnM91jK.!</td></tr>
</table>
<p>После входа вы в командной строке целевого компьютера (через PTY) либо сразу
выполняете одну команду, дописав её в конец: <span class="cmd">…anyuser@localhost "dir"</span>.</p>
<p><b>Примечание:</b> команды выполняются в постоянном шелле: переменные окружения
и текущая папка сохраняются между командами, как в одной сессии. Фоновые запуски
(<span class="cmd">&amp;</span>) и таймауты работают как обычно.</p>

<h2>Единый пароль — шпаргалка</h2>
<table class="pwtab">
<tr><th>Где</th><th>Логин</th><th>Пароль</th></tr>
<tr><td>Страница этого дашборда (basic auth браузера)</td><td>misha8585</td><td class="pw">RtnM91jK.!</td></tr>
<tr><td>Прыжок через relay (support@5.35.81.198) — с client-ключом не спрашивается</td><td>—</td><td class="pw">RtnM91jK.!</td></tr>
<tr><td>Целевая машина (встроенный SSH-сервер) — с client-ключом не спрашивается</td><td>любой</td><td class="pw">RtnM91jK.!</td></tr>
<tr><td>Zip-архив с этой страницы</td><td>—</td><td class="pw">RtnM91jK.!</td></tr>
</table>

<h2>Если что-то не работает</h2>
<ul>
<li><b>Машина не появляется в списке</b> — exe не запущен или туннель ещё поднимается
(подождите минуту). Активность в списке = exe работает прямо сейчас.</li>
<li><b>Connection refused на порту 2200</b> — туннель восстанавливается сам,
подождите до минуты; если дольше — перезапустите portassh.exe на целевой машине.
Порт у каждой машины свой и стабильный: после сбоя exe сам освобождает
«призрачный» порт прошлой сессии и возвращается на него же.</li>
<li><b>Скачивание заблокировано</b> — используйте кнопку «.bin» и переименуйте файл
обратно в portassh.exe.</li>
<li><b>Долгое зависание команды</b> — UAC-запросы (контроль учётных записей) ждут
клика на экране целевой машины; для GUI-программ запускайте в фоне: допишите
<span class="cmd">&amp;</span> в конец команды (например <span class="cmd">notepad &amp;</span>) —
сессия вернётся сразу. Есть и таймаут: <span class="cmd">timeout=60 &lt;команда&gt;</span>
— принудительно завершит команду через 60 секунд (код 124).</li>
</ul>
</details>
</div>
</div>
<div id="procmodal" style="display:none;position:fixed;inset:0;background:rgba(15,23,42,.55);z-index:50" onclick="if(event.target===this)closeProcs()">
  <div style="background:#fff;max-width:680px;margin:48px auto;border-radius:12px;padding:16px 18px;max-height:80vh;display:flex;flex-direction:column">
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
  <div style="background:#fff;max-width:680px;margin:48px auto;border-radius:12px;padding:16px 18px;max-height:80vh;display:flex;flex-direction:column">
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
        <button onclick="scrSendText()" style="padding:6px 14px;background:#4f46e5;color:#fff;border:0;border-radius:8px;cursor:pointer">Отправить</button>
        <a href="#" onclick="closeScreen();return false" style="color:#e8eefc;text-decoration:none;font-size:18px">✕</a>
      </div>
    </div>
    <img id="scrimg" draggable="false" style="max-width:100%;max-height:calc(100vh - 80px);cursor:crosshair;border:1px solid #2a3040;border-radius:8px;user-select:none" onmousedown="scrPress(event)" onmouseup="scrRelease(event)" onclick="scrClick(event, false)" ondblclick="scrClick(event, true)" oncontextmenu="scrRClick(event)">
  </div>
</div>
<script>
let scrPort = 0, scrTimer = null, scrBusy = false, scrDrag = false, scrLastMove = 0, scrWheelAcc = 0, scrWheelSent = true;
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
  fetchFrame();
  if (scrTimer) clearInterval(scrTimer);
  scrTimer = setInterval(fetchFrame, 1500);
}
function closeScreen() { clearInterval(scrTimer); scrTimer = null; document.getElementById('scrmodal').style.display = 'none'; }
function fetchFrame() {
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
  const x = Math.round((e.clientX - r.left) * (img.naturalWidth || r.width) / r.width);
  const y = Math.round((e.clientY - r.top) * (img.naturalHeight || r.height) / r.height);
  return x + ' ' + y;
}
function scrSendLine(line) {
  fetch('?input=' + scrPort + '&line=' + encodeURIComponent(line));
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
  fetch('?killp=' + pmPort + '&pid=' + pid).then(() => pmLoad());
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
  fetch('?svc=' + svcPort + '&act=' + act + '&name=' + encodeURIComponent(name))
    .then(r => r.json()).then(() => svLoad());
}
</script>
</body>
</html>
