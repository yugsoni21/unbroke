<?php
/**
 * Settings for the signup form. On your server, copy this file to config.php
 * (same folder) and fill it in. config.php is never committed to git.
 * Everything is optional: with no config.php the form still saves signups.
 */
return [
    // Email me when someone new signs up. Leave both empty to switch it off.
    // from_email should be an address on your own domain (for example
    // no-reply@unbroke.si, created in Hostinger's Emails section), or the
    // message may be filtered as spam.
    'notify_email' => '',
    'from_email' => '',

    // Turns on the private page at /api/admin.php where you can read the
    // signups and download them as a spreadsheet. It stays switched off until
    // the password is at least 12 characters. Use a long, unique one.
    'admin_user' => 'admin',
    'admin_password' => '',

    // Where signups are saved. The default is a protected folder next to this
    // file. For extra safety put it OUTSIDE public_html, for example
    // '/home/u123456789/unbroke-data' (the folder is created for you).
    'data_dir' => __DIR__ . '/data',

    // Extra website addresses allowed to submit the form (the address the
    // form is served from is always allowed).
    'allowed_hosts' => ['unbroke.si', 'www.unbroke.si'],

    // How many times one visitor can submit per hour.
    'rate_limit_per_hour' => 8,

    // Only if your site sits behind a proxy or CDN that hides visitors' real
    // addresses (for example Cloudflare): the name of the header that carries
    // the real address, such as 'HTTP_CF_CONNECTING_IP'. Leave empty otherwise.
    'trust_ip_header' => '',
];
