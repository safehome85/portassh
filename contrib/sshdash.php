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
$REGISTRY    = '/home/user/web/itstandart.kz/private/portassh-online.txt';
$EXE         = '/home/user/web/itstandart.kz/private/portassh.exe'; // сборка для скачивания (копируется cron'ом)
$RELAY       = 'ВАШ_RELAY_IP'; // IP/домен relay-сервера для колонки «Команда подключения»
$ACTIVE_SECS = 180;           // heartbeat у exe раз в 60 с — запас ×3
$MAX_AGE     = 7 * 86400;     // записи старше недели игнорируются вовсе

// PHP-TZ может отличаться от системного (машине пишут локальное время) —
// калибруем mktime-эпохи по системным `date +%s` / `date +%H:%M`
$sysNow = (int) trim((string) @shell_exec('date +%s'));
[$sh, $sm] = explode(':', trim((string) @shell_exec('date +%H:%M')));
$SKEW = $sysNow - mktime((int)$sh, (int)$sm, 0);

header('Content-Type: text/html; charset=utf-8');
header('Cache-Control: no-store');

// скачивание сборки (за тем же basic auth): /sshdash?dl=1
if (isset($_GET['dl'])) {
    $exe = @file_get_contents($EXE);
    if ($exe === false) { http_response_code(404); exit('no build uploaded'); }
    header('Content-Type: application/octet-stream');
    header('Content-Disposition: attachment; filename="portassh.exe"');
    header('Content-Length: ' . strlen($exe));
    echo $exe;
    exit;
}
?>
<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="20">
<title>portassh — активные машины</title>
<style>
  body { font-family: -apple-system, 'Segoe UI', Roboto, sans-serif; background: #12151c; color: #e8ecf4; margin: 40px; }
  h1 { font-size: 22px; }
  h1 .count { color: #34d399; }
  table { border-collapse: collapse; width: 100%; max-width: 950px; }
  th, td { padding: 10px 14px; text-align: left; border-bottom: 1px solid #2a3040; }
  th { color: #8fa3c8; font-weight: 600; font-size: 13px; text-transform: uppercase; }
  tr:hover td { background: #1a1f2b; }
  .dot { display: inline-block; width: 10px; height: 10px; border-radius: 50%; background: #34d399; margin-right: 8px;
         box-shadow: 0 0 6px #34d399; animation: pulse 2s infinite; }
  @keyframes pulse { 50% { opacity: .5; } }
  .role-admin { color: #f59e0b; }
  .role-user  { color: #34d399; }
  .cmd { font-family: monospace; font-size: 13px; color: #93c5fd; user-select: all; }
  .empty { color: #8fa3c8; }
</style>
</head>
<body>
<?php
$lines = @file($REGISTRY, FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES) ?: [];
$latest = [];
foreach ($lines as $line) {
    $p = explode(' ', trim($line));
    if (count($p) < 4) continue;
    [$host, $port, $t, $role] = $p;
    if (!in_array($role, ['user', 'admin'], true)) continue;
    // формат времени из реестра: "ДД.ММ_ЧЧ:ММ" (год не пишется)
    [$dmy, $hm] = explode('_', $t);
    [$dd, $mm] = explode('.', $dmy);
    $ts = mktime((int)substr($hm, 0, 2), (int)substr($hm, 3, 2), 0, (int)$mm, (int)$dd) + $SKEW;
    if ($ts - $sysNow > 86400) $ts = strtotime('-1 year', $ts) + $SKEW; // смена года
    if ($sysNow - $ts > $MAX_AGE) continue;
    $key = $host . '|' . $role;
    if (!isset($latest[$key]) || $latest[$key]['ts'] < $ts) {
        $latest[$key] = compact('host', 'port', 'role', 'ts', 't');
    }
}
// оставляем только живые: heartbeat раз в минуту, окно ×3
$active = array_filter($latest, fn($e) => $sysNow - $e['ts'] <= $ACTIVE_SECS);
usort($active, fn($a, $b) => $b['ts'] <=> $a['ts']);

echo '<h1>portassh — активные машины: <span class="count">' . count($active) . '</span> <a style="float:right;font-size:14px" href="?dl=1">⬇ скачать portassh.exe</a></h1>';
?>
<table>
<tr><th>Машина</th><th>Роль</th><th>Порт</th><th>Команда подключения</th><th>Подтверждение</th></tr>
<?php
if (!$active) {
    echo '<tr><td colspan="5" class="empty">Нет активных машин — запустите portassh.exe на целевом компьютере</td></tr>';
}
foreach ($active as $e) {
    $sec = $sysNow - $e['ts'];
    $seen = $sec < 5 ? 'только что' : $sec . ' с назад';
    $cmd = "ssh -o StrictHostKeyChecking=accept-new -J root@{$GLOBALS['RELAY']} -p {$e['port']} anyuser@localhost";
    printf(
        "<tr><td><span class=\"dot\"></span>%s</td><td class=\"role-%s\">%s</td><td>%s</td><td class=\"cmd\">%s</td><td class=\"ago\">%s</td></tr>\n",
        htmlspecialchars($e['host']),
        $e['role'],
        $e['role'] === 'admin' ? 'админ' : 'пользователь',
        htmlspecialchars($e['port']),
        htmlspecialchars($cmd),
        htmlspecialchars($seen)
    );
}
?>
</table>
</body>
</html>
