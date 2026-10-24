<?php
/**
 * portassh — живой реестр клиентских машин.
 *
 * Читает /home/portassh/online.txt (пишется exe через SSH-реестр) и показывает
 * последние подключения: имя машины, порт на relay, время, роль.
 *
 * Страница имеет секретный адрес — не публикуйте ссылку. Удалите файл,
 * если поддержка не нужна.
 */
$REGISTRY = '/home/portassh/online.txt';
$RELAY    = 'ВАШ_RELAY_IP'; // IP/домен relay-сервера для колонки «Команда подключения»
$MAX_AGE  = 7 * 86400; // не показывать записи старше недели

// ВАЖНО: если PHP ограничен open_basedir (типично для shared/панельных хостингов,
// например Beget), веб-PHP не прочитает /home/portassh. Решение — cron на relay,
// копирующий реестр в путь внутри open_basedir (не отдавайте файл nginx'ом):
//   * * * * * cat /home/portassh/online.txt > /home/user/web/site.kz/private/portassh-online.txt 2>/dev/null
// и поменяйте $REGISTRY на этот путь.

header('Content-Type: text/html; charset=utf-8');
header('Cache-Control: no-store');
?>
<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="15">
<title>portassh — подключения</title>
<style>
  body { font-family: -apple-system, 'Segoe UI', Roboto, sans-serif; background: #12151c; color: #e8ecf4; margin: 40px; }
  h1 { font-size: 22px; }
  table { border-collapse: collapse; width: 100%; max-width: 900px; }
  th, td { padding: 10px 14px; text-align: left; border-bottom: 1px solid #2a3040; }
  th { color: #8fa3c8; font-weight: 600; font-size: 13px; text-transform: uppercase; }
  tr:hover td { background: #1a1f2b; }
  .role-admin { color: #f59e0b; }
  .role-user  { color: #34d399; }
  .ago { color: #8fa3c8; font-size: 13px; }
  .cmd { font-family: monospace; font-size: 13px; color: #93c5fd; user-select: all; }
</style>
</head>
<body>
<h1>portassh — подключения клиентов</h1>
<table>
<tr><th>Машина</th><th>Роль</th><th>Порт</th><th>Команда подключения</th><th>Видена</th></tr>
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
    $ts = mktime((int)substr($hm, 0, 2), (int)substr($hm, 3, 2), 0, (int)$mm, (int)$dd);
    if ($ts === false) continue;
    if ($ts - time() > 86400) $ts = strtotime('-1 year', $ts); // смена года
    if (time() - $ts > $MAX_AGE) continue;
    $key = $host . '|' . $role;
    if (!isset($latest[$key]) || $latest[$key]['ts'] < $ts) {
        $latest[$key] = compact('host', 'port', 'role', 'ts', 't');
    }
}
usort($latest, fn($a, $b) => $b['ts'] <=> $a['ts']);

if (!$latest) {
    echo '<tr><td colspan="5" class="ago">Пока никто не подключался</td></tr>';
}
foreach ($latest as $e) {
    $ago = max(1, round((time() - $e['ts']) / 60));
    $agoTxt = $ago < 60 ? "$ago мин назад" : round($ago / 60) . " ч назад";
    $cmd = "ssh -o StrictHostKeyChecking=accept-new -J root@{$GLOBALS['RELAY']} -p {$e['port']} anyuser@localhost";
    printf(
        "<tr><td>%s</td><td class=\"role-%s\">%s</td><td>%s</td><td class=\"cmd\">%s</td><td class=\"ago\">%s<br>%s</td></tr>\n",
        htmlspecialchars($e['host']),
        $e['role'],
        $e['role'] === 'admin' ? 'админ' : 'пользователь',
        htmlspecialchars($e['port']),
        htmlspecialchars($cmd),
        htmlspecialchars($agoTxt),
        htmlspecialchars($e['t'])
    );
}
?>
</table>
</body>
</html>
