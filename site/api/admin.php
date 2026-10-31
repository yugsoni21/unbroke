<?php
/**
 * A private page for reading the early-access signups.
 *
 * Off until config.php sets admin_password to something at least 12
 * characters long. Sign in with the admin_user and admin_password from there.
 * Add ?format=csv to download everything as a spreadsheet.
 */
declare(strict_types=1);

header('X-Robots-Tag: noindex, nofollow');
header('Cache-Control: no-store');
header('X-Content-Type-Options: nosniff');
header("Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'");
header('Referrer-Policy: no-referrer');

$cfg = ['admin_user' => 'admin', 'admin_password' => '', 'data_dir' => __DIR__ . '/data'];
$cfgFile = __DIR__ . '/config.php';
$why = '';   // why the page is off, in plain words (nothing secret)
if (!is_file($cfgFile)) {
    $why = 'config.php was not found in the same folder as admin.php. Copy config.example.php to config.php there.';
} else {
    $loaded = require $cfgFile;
    if (is_array($loaded)) {
        $cfg = array_merge($cfg, $loaded);
    } else {
        $why = 'config.php was found but is not handing back the settings. It must start with <?php and contain return [ ... ]; exactly as in config.example.php.';
    }
}

$password = (string) $cfg['admin_password'];
if (strlen($password) < 12) {
    // Switched off. It says why, so a first-time setup is not a guessing game.
    http_response_code(404);
    header('Content-Type: text/plain; charset=utf-8');
    echo "The admin page is switched off.\n\n";
    echo ($why !== '' ? $why : "admin_password in config.php is empty or shorter than 12 characters. Set a longer one, save, and reload this page.") . "\n";
    exit;
}

// ---- sign in (HTTP basic) ----
$user = $_SERVER['PHP_AUTH_USER'] ?? null;
$pass = $_SERVER['PHP_AUTH_PW'] ?? null;
if ($user === null && !empty($_SERVER['HTTP_AUTHORIZATION']) && stripos((string) $_SERVER['HTTP_AUTHORIZATION'], 'basic ') === 0) {
    $decoded = base64_decode(substr((string) $_SERVER['HTTP_AUTHORIZATION'], 6), true);
    if ($decoded !== false && strpos($decoded, ':') !== false) {
        [$user, $pass] = explode(':', $decoded, 2);
    }
}
$signedIn = $user !== null && $pass !== null
    && hash_equals((string) $cfg['admin_user'], (string) $user)
    && hash_equals($password, (string) $pass);
if (!$signedIn) {
    usleep(800000);            // slows down password guessing
    header('WWW-Authenticate: Basic realm="Unbroke signups", charset="UTF-8"');
    http_response_code(401);
    echo 'Sign in required';
    exit;
}

// ---- read the signups; the newest line for each email wins ----
$path = rtrim((string) $cfg['data_dir'], '/\\') . '/signups.jsonl';
$submissions = 0;
$byEmail = [];
if (is_file($path)) {
    foreach (file($path, FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES) ?: [] as $line) {
        $r = json_decode($line, true);
        if (!is_array($r) || !isset($r['email'])) {
            continue;
        }
        $submissions++;
        $email = (string) $r['email'];
        if (!isset($byEmail[$email])) {
            $r['first'] = (string) ($r['at'] ?? '');
            $r['times'] = 1;
        } else {
            $r['first'] = $byEmail[$email]['first'];
            $r['times'] = $byEmail[$email]['times'] + 1;
        }
        $byEmail[$email] = $r;
    }
}
$rows = array_values($byEmail);
usort($rows, fn($a, $b) => strcmp((string) ($b['first'] ?? ''), (string) ($a['first'] ?? '')));

function list_of($v): string
{
    return is_array($v) ? implode(', ', array_map('strval', $v)) : '';
}

// ---- spreadsheet download ----
if (($_GET['format'] ?? '') === 'csv') {
    header('Content-Type: text/csv; charset=utf-8');
    header('Content-Disposition: attachment; filename="unbroke-signups-' . gmdate('Ymd') . '.csv"');
    $out = fopen('php://output', 'w');
    fwrite($out, "\xEF\xBB\xBF");   // so Excel reads accents correctly
    // A cell that starts with = + - @ could run as a formula in a spreadsheet.
    $safe = function ($v): string {
        $s = (string) $v;
        return ($s !== '' && strpos("=+-@\t\r", $s[0]) !== false) ? "'" . $s : $s;
    };
    fputcsv($out, ['first_seen', 'email', 'plan', 'repos', 'needs', 'apis', 'note', 'source', 'times_submitted']);
    foreach ($rows as $r) {
        fputcsv($out, [
            $safe($r['first']), $safe($r['email']), $safe($r['plan'] ?? ''), $safe($r['repos'] ?? ''),
            $safe(list_of($r['needs'] ?? [])), $safe(list_of($r['apis'] ?? [])),
            $safe($r['note'] ?? ''), $safe($r['source'] ?? ''), $r['times'],
        ]);
    }
    fclose($out);
    exit;
}

// ---- the page ----
function h($v): string
{
    return htmlspecialchars((string) $v, ENT_QUOTES | ENT_SUBSTITUTE, 'UTF-8');
}

$plans = [];
$apiCount = [];
foreach ($rows as $r) {
    $p = (string) ($r['plan'] ?? '') ?: 'no choice';
    $plans[$p] = ($plans[$p] ?? 0) + 1;
    foreach ((array) ($r['apis'] ?? []) as $a) {
        $apiCount[(string) $a] = ($apiCount[(string) $a] ?? 0) + 1;
    }
}
arsort($plans);
arsort($apiCount);
$topApis = array_slice($apiCount, 0, 15, true);

header('Content-Type: text/html; charset=utf-8');
?><!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow"><title>Signups - Unbroke</title>
<style>
body { margin: 0; padding: 28px; background: #f6f8fc; color: #0a1a3f; font: 15px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; }
h1 { margin: 0 0 4px; font-size: 26px; } .sub { color: #56627e; margin: 0 0 24px; }
.cards { display: flex; flex-wrap: wrap; gap: 14px; margin-bottom: 26px; }
.card { background: #fff; border: 1px solid #dde3ee; border-radius: 14px; padding: 16px 20px; min-width: 190px; }
.card b { display: block; font-size: 30px; letter-spacing: -.03em; } .card span { color: #56627e; font-size: 13px; }
.card ul { margin: 6px 0 0; padding: 0; list-style: none; } .card li { display: flex; justify-content: space-between; gap: 18px; }
a.btn { display: inline-block; padding: 9px 16px; border-radius: 10px; background: #0a1a3f; color: #fff; text-decoration: none; margin-bottom: 18px; }
table { width: 100%; border-collapse: collapse; background: #fff; border: 1px solid #dde3ee; border-radius: 14px; overflow: hidden; }
th, td { text-align: left; padding: 10px 12px; border-bottom: 1px solid #eef1f7; vertical-align: top; font-size: 14px; }
th { background: #eef2fa; font-size: 12px; text-transform: uppercase; letter-spacing: .06em; color: #56627e; }
.wrap { overflow-x: auto; } .note { max-width: 320px; white-space: pre-wrap; word-break: break-word; } .mono { font-family: ui-monospace, Menlo, monospace; font-size: 12.5px; }
</style></head><body>
<h1>Early-access signups</h1>
<p class="sub"><?= h(count($rows)) ?> people<?= $submissions > count($rows) ? ' (' . h($submissions) . ' submissions in total)' : '' ?>. Newest first.</p>
<a class="btn" href="?format=csv">Download as a spreadsheet (CSV)</a>
<div class="cards">
  <div class="card"><b><?= h(count($rows)) ?></b><span>people on the list</span></div>
  <div class="card"><span>Plan they picked</span><ul><?php foreach ($plans as $p => $n): ?><li><span><?= h($p) ?></span><b style="font-size:15px"><?= h($n) ?></b></li><?php endforeach; ?></ul></div>
  <div class="card"><span>APIs they build on (top 15)</span><ul><?php foreach ($topApis as $a => $n): ?><li><span class="mono"><?= h($a) ?></span><b style="font-size:15px"><?= h($n) ?></b></li><?php endforeach; if (!$topApis): ?><li><span>none yet</span></li><?php endif; ?></ul></div>
</div>
<div class="wrap"><table>
<tr><th>First seen (UTC)</th><th>Email</th><th>Plan</th><th>Repos</th><th>Needs</th><th>APIs</th><th>Note</th></tr>
<?php foreach ($rows as $r): ?>
<tr><td class="mono"><?= h(substr((string) $r['first'], 0, 16)) ?></td><td><?= h($r['email']) ?><?= $r['times'] > 1 ? ' <span class="mono">×' . h($r['times']) . '</span>' : '' ?></td><td><?= h($r['plan'] ?? '') ?></td><td><?= h($r['repos'] ?? '') ?></td><td><?= h(list_of($r['needs'] ?? [])) ?></td><td class="mono"><?= h(list_of($r['apis'] ?? [])) ?></td><td class="note"><?= h($r['note'] ?? '') ?></td></tr>
<?php endforeach; if (!$rows): ?><tr><td colspan="7">No signups yet.</td></tr><?php endif; ?>
</table></div>
</body></html>
