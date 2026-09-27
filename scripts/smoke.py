#!/usr/bin/env python3
"""Container smoke test; standard library only, unique mail and toxic ownership."""
import imaplib
import json
import os
from pathlib import Path
import smtplib
import ssl
import urllib.parse
import urllib.request
import uuid
from email.message import EmailMessage

api = os.environ.get('MAIL_SANDBOX_HTTP_URL', os.environ.get('IMAP_EMULATOR_HTTP_URL', 'http://localhost:8026'))
smtp_port = int(os.environ.get('MAIL_SANDBOX_SMTP_PORT', os.environ.get('SMTP_EMULATOR_PORT', '1025')))
imap_port = int(os.environ.get('MAIL_SANDBOX_IMAP_PORT', os.environ.get('IMAP_EMULATOR_PORT', '1993')))
token = str(uuid.uuid4())
name = 'smoke-' + token
ids = []
accounts = []

def call(path, method='GET', data=None):
    payload = None if data is None else json.dumps(data).encode()
    request = urllib.request.Request(api + path, data=payload, method=method,
                                     headers={'Content-Type': 'application/json'})
    with urllib.request.urlopen(request, timeout=10) as response:
        body = response.read()
        return json.loads(body) if response.headers.get_content_type() == 'application/json' else body

def send(identifier, recipient=None):
    mail = EmailMessage()
    mail['From'] = 'smoke@example.test'
    mail['To'] = recipient or token + '@example.test'
    mail['Message-ID'] = '<' + identifier + '@example.test>'
    mail['Subject'] = token
    mail.set_content('Container SMTP/HTTP/IMAP round trip')
    with smtplib.SMTP('localhost', smtp_port, timeout=10) as smtp:
        smtp.send_message(mail)

call('/api/v1/toxics', 'POST', {
    'name': name, 'type': 'smtp_reject', 'selector': {'message_id': token + '-reject@example.test'},
    'attributes': {'code': 451}, 'max_hits': 1,
})
try:
    try:
        send(token + '-reject')
        raise AssertionError('Scoped rejection did not fire')
    except smtplib.SMTPDataError as error:
        assert error.smtp_code == 451, error
    send(token)
    messages = call('/api/v1/search?query=' + urllib.parse.quote('subject:' + token))['messages']
    ids = [m['ID'] for m in messages]
    assert len(messages) == 1 and messages[0]['Folder'] == 'Sent', messages
    raw = call('/api/v1/message/' + ids[0] + '/raw')
    context = ssl.create_default_context(cafile=str(Path(__file__).resolve().parents[1] / 'testdata/tls/server.crt'))
    with imaplib.IMAP4_SSL('localhost', imap_port, ssl_context=context, timeout=10) as imap:
        imap.login('clinic@example.test', 'local-imap-only')
        imap.select('Sent', readonly=True)
        status, found = imap.uid('search', None, 'HEADER', 'Message-ID', token + '@example.test')
        assert status == 'OK' and len(found[0].split()) == 1, found
        status, body = imap.uid('fetch', found[0], '(BODY.PEEK[])')
        assert status == 'OK' and body[0][1] == raw, body
    assert call('/api/v1/toxics/' + name)['hits'] == 1
    # Account scopes permit identical message IDs and toxic names safely.
    for _ in range(2):
        accounts.append(call('/api/v1/mailboxes', 'POST', {}))
    first, second = accounts
    identifier = 'account-' + token
    call(first['api_base'] + '/api/v1/toxics', 'POST', {
        'name': name, 'type': 'smtp_reject',
        'selector': {'message_id': identifier + '@example.test'}, 'attributes': {'code': 451},
    })
    try:
        send(identifier, first['recipients'][0])
        raise AssertionError('Account-scoped rejection did not fire')
    except smtplib.SMTPDataError as error:
        assert error.smtp_code == 451, error
    send(identifier, second['recipients'][0])
    assert call(first['api_base'] + '/api/v1/messages')['total'] == 0
    isolated = call(second['api_base'] + '/api/v1/messages')['messages']
    assert len(isolated) == 1 and isolated[0]['MailboxID'] == second['id'], isolated
    assert len(call('/api/v1/search?query=' + urllib.parse.quote('subject:' + token))['messages']) == 1
    with imaplib.IMAP4_SSL('localhost', imap_port, ssl_context=context, timeout=10) as imap:
        imap.login(second['username'], second['password'])
        imap.select('Sent', readonly=True)
        status, found = imap.uid('search', None, 'HEADER', 'Message-ID', identifier + '@example.test')
        assert status == 'OK' and len(found[0].split()) == 1, found
    print('SMTP, HTTP, verified TLS IMAP, scoped toxics and separate accounts passed')
finally:
    for account in accounts:
        call('/api/v1/mailboxes/' + account['id'], 'DELETE')
    call('/api/v1/toxics/' + name, 'DELETE')
    if ids:
        call('/api/v1/messages', 'DELETE', {'IDs': ids})
