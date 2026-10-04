<?php

$config['product_name'] = 'Mailarky';

// Trust the bundled test certificate while still verifying the server name.
$config['imap_conn_options'] = [
    'ssl' => [
        'cafile' => '/certs/mailarky.crt',
        'verify_peer' => true,
        'verify_peer_name' => true,
    ],
];

// Runtime mailbox usernames are case-sensitive and need not be email addresses.
$config['login_lc'] = 0;

// Use the same account for SMTP submission and IMAP browsing.
$config['smtp_user'] = '%u';
$config['smtp_pass'] = '%p';
$config['smtp_conn_options'] = $config['imap_conn_options'];

// SMTP captures authenticated submissions in this account's Sent folder.
$config['no_save_sent_messages'] = true;
$config['enable_spellcheck'] = false;
