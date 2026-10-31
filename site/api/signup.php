<?php
/**
 * Receives the early-access form on unbroke.si and saves it on this server.
 *
 * - Accepts only a JSON POST from the site's own pages.
 * - Saves one line per submission to data/signups.jsonl (never served to the web).
 * - Stops obvious bots (hidden field, too-fast submits) and limits each visitor
 *   to a few submissions an hour. Visitors are told nothing about the checks.
 * - Optionally emails you when someone new signs up (see config.example.php).
 *
 * Needs PHP 8.0 or newer, which Hostinger offers on its web hosting plans.
 */
declare(strict_types=1);

const MAX_BODY = 8192;

header('Content-Type: application/json; charset=utf-8');
header('Cache-Control: no-store');
header('X-Content-Type-Options: nosniff');

function reply(int $code, array $body): void
{
    http_response_code($code);
    echo json_encode($body, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE);
    exit;
}

function fail(int $code, string $message): void
{
    reply($code, ['ok' => false, 'error' => $message]);
}

if (PHP_VERSION_ID < 80000) {
    fail(500, 'This server needs PHP 8.0 or newer.');
}

// ---- settings ----
$cfg = [
    'notify_email' => '',
    'from_email' => '',
    'data_dir' => __DIR__ . '/data',
    'allowed_hosts' => [],
    'rate_limit_per_hour' => 8,
    'trust_ip_header' => '',
];
$cfgFile = __DIR__ . '/config.php';
if (is_file($cfgFile)) {
    $loaded = require $cfgFile;
    if (is_array($loaded)) {
        $cfg = array_merge($cfg, $loaded);
    }
}

// ---- who may call this ----
if (($_SERVER['REQUEST_METHOD'] ?? '') !== 'POST') {
    header('Allow: POST');
    fail(405, 'Use POST.');
}

function host_of(string $url): string
{
    $h = parse_url($url, PHP_URL_HOST);
    return is_string($h) ? strtolower($h) : '';
}

$self = strtolower((string) preg_replace('/:\d+$/', '', (string) ($_SERVER['HTTP_HOST'] ?? '')));
$allowedHosts = array_map('strtolower', array_map('strval', (array) $cfg['allowed_hosts']));
$allowedHosts[] = $self;
foreach (['HTTP_ORIGIN', 'HTTP_REFERER'] as $h) {
    if (!empty($_SERVER[$h])) {
        if (!in_array(host_of((string) $_SERVER[$h]), $allowedHosts, true)) {
            fail(403, 'This form can only be submitted from the Unbroke website.');
        }
        break;
    }
}

// A cross-site <form> cannot send application/json, so requiring it shuts that door.
if (stripos((string) ($_SERVER['CONTENT_TYPE'] ?? ''), 'application/json') !== 0) {
    fail(415, 'That request was not understood.');
}

$raw = file_get_contents('php://input', false, null, 0, MAX_BODY + 1);
if ($raw === false || $raw === '') {
    fail(400, 'That request was not understood.');
}
if (strlen($raw) > MAX_BODY) {
    fail(413, 'That was too much text. Please shorten your note.');
}
$d = json_decode($raw, true, 6);
// It must be an object ({"email": ...}), not a list ([1, 2, 3]).
if (!is_array($d) || ($d !== [] && array_keys($d) === range(0, count($d) - 1))) {
    fail(400, 'That request was not understood.');
}

// ---- clean every field ----
function text(array $d, string $key, int $max): string
{
    $v = $d[$key] ?? '';
    if (!is_string($v)) {
        return '';
    }
    $v = preg_replace('/[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]/u', '', $v);
    if ($v === null) {      // not valid UTF-8
        return '';
    }
    return mb_substr(trim($v), 0, $max, 'UTF-8');
}

// Bots fill the hidden field and submit in a blink. Pretend it worked and keep nothing.
if (text($d, 'website', 100) !== '') {
    reply(200, ['ok' => true]);
}
$elapsed = $d['t'] ?? null;
if (is_int($elapsed) || is_float($elapsed)) {
    if ($elapsed < 1500) {
        reply(200, ['ok' => true]);
    }
}

$email = strtolower(text($d, 'email', 320));
if ($email === '' || strlen($email) > 254 || filter_var($email, FILTER_VALIDATE_EMAIL) === false) {
    fail(422, 'Enter a valid email address, like you@company.com.');
}

$plan = text($d, 'plan', 20);
if (!in_array($plan, ['basic', 'pro', 'max', 'enterprise', 'unsure'], true)) {
    $plan = '';
}

$repos = null;
if (isset($d['repos']) && (is_int($d['repos']) || is_float($d['repos']))) {
    $repos = max(0, min(100000, (int) $d['repos']));
}

$needs = [];
if (isset($d['needs']) && is_array($d['needs'])) {
    foreach ($d['needs'] as $n) {
        if (is_string($n) && in_array($n, ['slack', 'fix', 'hourly', 'private'], true) && !in_array($n, $needs, true)) {
            $needs[] = $n;
        }
    }
}

$apis = [];
if (isset($d['apis']) && is_array($d['apis'])) {
    foreach ($d['apis'] as $a) {
        if (is_string($a) && preg_match('/^[a-z0-9][a-z0-9-]{0,39}$/', $a) === 1 && !in_array($a, $apis, true)) {
            $apis[] = $a;
            if (count($apis) >= 40) {
                break;
            }
        }
    }
}

$note = text($d, 'note', 500);
$source = text($d, 'source', 30);
if (preg_match('/^[a-z0-9_-]*$/', $source) !== 1) {
    $source = '';
}

// ---- where things are kept ----
$dir = rtrim((string) $cfg['data_dir'], '/\\');
if (!is_dir($dir) && !@mkdir($dir, 0750, true) && !is_dir($dir)) {
    error_log('unbroke signup: cannot create ' . $dir);
    fail(500, 'We could not save that right now. Please try again in a few minutes.');
}
// Even if the web server is misconfigured, keep this folder private.
if (!is_file($dir . '/.htaccess')) {
    @file_put_contents($dir . '/.htaccess', "Require all denied\n<IfModule !mod_authz_core.c>\nDeny from all\n</IfModule>\n");
}
if (!is_file($dir . '/index.html')) {
    @file_put_contents($dir . '/index.html', '');
}

// ---- limit how often one visitor can submit ----
$saltFile = $dir . '/.salt';
if (!is_file($saltFile)) {
    @file_put_contents($saltFile, bin2hex(random_bytes(16)));
    @chmod($saltFile, 0640);
}
$salt = (string) @file_get_contents($saltFile);

$ip = (string) ($_SERVER['REMOTE_ADDR'] ?? '0.0.0.0');
$ipHeader = (string) $cfg['trust_ip_header'];
if ($ipHeader !== '' && !empty($_SERVER[$ipHeader])) {
    $candidate = trim(explode(',', (string) $_SERVER[$ipHeader])[0]);
    if (filter_var($candidate, FILTER_VALIDATE_IP) !== false) {
        $ip = $candidate;
    }
}

$rlDir = $dir . '/rl';
if (!is_dir($rlDir)) {
    @mkdir($rlDir, 0750, true);
}
// Tidy up old counters now and then. They hold only a scrambled address and timestamps.
if (random_int(1, 5) === 1) {
    foreach (glob($rlDir . '/*') ?: [] as $old) {
        if (is_file($old) && filemtime($old) < time() - 7200) {
            @unlink($old);
        }
    }
}
$rlFile = $rlDir . '/' . substr(hash('sha256', $salt . $ip), 0, 24);
$limit = max(1, (int) $cfg['rate_limit_per_hour']);
$now = time();
$rl = @fopen($rlFile, 'c+');
if ($rl !== false) {
    flock($rl, LOCK_EX);
    $recent = [];
    foreach (explode("\n", (string) stream_get_contents($rl)) as $line) {
        if ($line !== '' && ctype_digit($line) && (int) $line > $now - 3600) {
            $recent[] = (int) $line;
        }
    }
    if (count($recent) >= $limit) {
        flock($rl, LOCK_UN);
        fclose($rl);
        header('Retry-After: 3600');
        fail(429, 'Too many attempts from your connection. Please try again in a little while.');
    }
    $recent[] = $now;
    ftruncate($rl, 0);
    rewind($rl);
    fwrite($rl, implode("\n", $recent) . "\n");
    flock($rl, LOCK_UN);
    fclose($rl);
}

// ---- save ----
$record = [
    'id' => bin2hex(random_bytes(6)),
    'at' => gmdate('c'),
    'email' => $email,
    'plan' => $plan,
    'repos' => $repos,
    'needs' => $needs,
    'apis' => $apis,
    'note' => $note,
    'source' => $source,
];

$already = false;
try {
    $path = $dir . '/signups.jsonl';
    $fh = fopen($path, 'c+');
    if ($fh === false) {
        throw new RuntimeException('cannot open ' . $path);
    }
    flock($fh, LOCK_EX);
    while (($line = fgets($fh)) !== false) {
        $prev = json_decode($line, true);
        if (is_array($prev) && ($prev['email'] ?? '') === $email) {
            $already = true;
            break;
        }
    }
    fseek($fh, 0, SEEK_END);
    $written = fwrite($fh, json_encode($record, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE) . "\n");
    fflush($fh);
    flock($fh, LOCK_UN);
    fclose($fh);
    if ($written === false) {
        throw new RuntimeException('write failed');
    }
    @chmod($path, 0640);
} catch (Throwable $e) {
    error_log('unbroke signup: ' . $e->getMessage());
    fail(500, 'We could not save that right now. Please try again in a few minutes.');
}

// ---- tell the owner (optional; a mail problem never fails the signup) ----
$to = (string) $cfg['notify_email'];
$from = (string) $cfg['from_email'];
if (!$already && filter_var($to, FILTER_VALIDATE_EMAIL) !== false && filter_var($from, FILTER_VALIDATE_EMAIL) !== false) {
    $lines = [
        'Email: ' . $email,
        'Plan: ' . ($plan !== '' ? $plan : '(none)'),
        'Repositories: ' . ($repos ?? '(not given)'),
        'Needs: ' . ($needs ? implode(', ', $needs) : '(none)'),
        'APIs: ' . ($apis ? implode(', ', $apis) : '(none)'),
        'Note: ' . ($note !== '' ? $note : '(none)'),
    ];
    $headers = "From: Unbroke <$from>\r\nReply-To: $email\r\nContent-Type: text/plain; charset=utf-8";
    @mail($to, 'New Unbroke signup: ' . $email, implode("\n", $lines) . "\n", $headers);
}

reply(200, ['ok' => true, 'already' => $already]);
