#!/usr/bin/env python3
"""Exercise the real one-minute scheduler (~3 minutes), using disposable data only.

Usage: python3 scripts/backup-acceptance.py [path-to-built-kymessages]
No network destination, live identity, private recovery key or custodian shares.
"""
import datetime as dt
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import socket
import sqlite3
import stat
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

REPO = Path(__file__).resolve().parent.parent
BINARY = Path(sys.argv[1]).resolve() if len(sys.argv) > 1 else REPO / 'kymessages'


def check(condition, message):
    if not condition:
        raise RuntimeError(message)


def wait_for(check_fn, seconds=75):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        result = check_fn()
        if result:
            return result
        time.sleep(0.25)
    raise TimeoutError('Acceptance condition did not complete before its deadline')


with tempfile.TemporaryDirectory(prefix='kymessages-backup-acceptance-') as scratch:
    work = Path(scratch)
    data, backups = work / 'data', work / 'backups'
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        port = listener.getsockname()[1]
    origin = f'http://127.0.0.1:{port}'
    password, replacement = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
    env = {'PATH': os.environ['PATH'], 'KY_HOST': '127.0.0.1', 'KY_PORT': str(port),
           'KY_APP_URL': origin, 'KY_DATA_DIR': str(data), 'KY_DB_DRIVER': 'sqlite',
           'KY_BACKUP_DIR': str(backups), 'KY_BACKUP_KEEP': '2',
           'KY_BACKUP_DEPOSIT_INTERVAL': '0', 'KY_ADMIN_PASSWORD': password,
           'KY_CAPTCHA_PROVIDER': 'none'}
    cookies = http.cookiejar.CookieJar()
    http = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cookies))

    def request(path, method='GET', body=None):
        headers = {'Origin': origin, 'Content-Type': 'application/json'}
        for cookie in cookies:
            if cookie.name == 'ky_csrf':
                headers['X-CSRF-Token'] = cookie.value
        raw = None if body is None else json.dumps(body).encode()
        with http.open(urllib.request.Request(origin + path, raw, headers, method=method), timeout=15) as response:
            return json.load(response)

    def login(secret):
        check(request('/api/auth/login', 'POST', {'username': 'admin', 'password': secret})['authenticated'], 'Login failed')

    def status():
        return request('/api/backup/status')

    def latest_since(previous=None):
        observed = status()
        return observed if observed.get('last_run') != previous else None

    def make_due(key='backup_last_attempt'):
        # Inject time only into this owned scratch database, never adjust host time.
        old = (dt.datetime.now(dt.timezone.utc) - dt.timedelta(minutes=16)).isoformat(timespec='seconds')
        with sqlite3.connect(data / 'ky_server.db', timeout=5) as db:
            db.execute('UPDATE server_settings SET value=? WHERE key=?', (old, key))

    with (work / 'server.log').open('w') as log:
        process = subprocess.Popen([str(BINARY)], env=env, cwd=work, stdout=log, stderr=log)
        try:
            def ready():
                check(process.poll() is None, 'Owned server exited during startup')
                try:
                    return request('/api/settings')
                except urllib.error.URLError:
                    return None
            wait_for(ready, 30)
            login(password)
            request('/api/auth/change-password', 'POST', {'current_password': password, 'new_password': replacement})
            login(replacement)
            fixture = json.loads((REPO / 'internal/backup/testdata/pairing-v050.json').read_text())
            request('/api/backup/pin-key', 'POST', {'public_key': fixture['PublicKey'], 'threshold': 2, 'total_shares': 3})
            request('/api/backup/schedule', 'PUT', {'interval_sec': 900})
            scheduled = wait_for(latest_since)
            check(scheduled['last_run']['outcome'] == 'success', 'Scheduled local backup failed')
            check(scheduled['last_run']['trigger'] == 'scheduled', 'No actual scheduled result')
            check(len(scheduled['local_copies']) == 1, 'Scheduler did not make exactly one copy')
            first = scheduled['last_run']
            next_run = dt.datetime.fromisoformat(scheduled['next_run_at'])
            check((next_run - dt.datetime.now(dt.timezone.utc)).total_seconds() > 850, 'Next run not based on attempt')
            print('PASS: timer observes admin schedule override and writes a sealed local copy', flush=True)

            # The messages kind is off until enabled and keeps its own schedule and directory.
            check(scheduled['messages']['interval_sec'] == 0 and 'next_run_at' not in scheduled['messages'], 'Messages schedule not off by default')
            request('/api/backup/messages/schedule', 'PUT', {'interval_sec': 900})
            make_due('messages_backup_last_attempt')
            msgs = wait_for(lambda: status() if status()['messages'].get('last_run') else None)['messages']
            check(msgs['last_run']['outcome'] == 'success' and msgs['last_run']['trigger'] == 'scheduled', 'Scheduled messages backup failed')
            check(len(msgs['local_copies']) == 1 and len(list((backups / 'messages').glob('*.kycap'))) == 1, 'Messages copy count wrong')
            after = status()
            check(len(after['local_copies']) == 1 and after['last_run'] == first, 'Messages run touched the people copies or result')
            print('PASS: messages schedule runs on its own timer into <dir>/messages and leaves people untouched', flush=True)

            # The configured path becomes a file: a deterministic local-destination failure.
            saved = work / 'saved-backups'
            backups.rename(saved)
            backups.write_text('synthetic destination failure')
            make_due()
            failed = wait_for(lambda: latest_since(first))
            check(failed['last_run']['outcome'] == 'failure', 'Scheduled failure concealed by prior success')
            check(failed['last_run']['trigger'] == 'scheduled', 'Failure was not scheduled')
            check((dt.datetime.fromisoformat(failed['next_run_at']) - dt.datetime.now(dt.timezone.utc)).total_seconds() > 850,
                  'Failed attempt did not move the retry deadline')
            print('PASS: failed scheduled run is durable and moves the retry deadline', flush=True)

            request('/api/backup/schedule', 'PUT', {'interval_sec': 0})
            make_due()
            failed_result = failed['last_run']
            # Cover the next actual one-minute tick while disabled.
            deadline = time.monotonic() + 65
            while time.monotonic() < deadline:
                check(process.poll() is None, 'Owned server exited')
                check(status()['last_run'] == failed_result, 'Disabled schedule attempted another backup')
                time.sleep(1)
            check('next_run_at' not in status(), 'Disabled schedule still reports a next run')
            backups.unlink()
            saved.rename(backups)
            for _ in range(3):
                result = request('/api/backup/deposit', 'POST')
                check(result['manifest']['service_name'] == 'KyMessages', 'Wrong capsule service name')
                check(result['manifest']['app_version'] == status()['app_version'], 'Capsule/status versions differ')
                check(stat.S_IMODE(Path(result['local_path']).stat().st_mode) == 0o600, 'Local capsule permissions too broad')
            final = status()
            check(len(final['local_copies']) == 2, 'Keep-newest pruning failed')
            check(final['last_run']['trigger'] == 'admin' and final['last_run']['outcome'] == 'success', 'Manual result not persisted')
            check(final['last_run']['capsule_id'] == result['manifest']['capsule_id'], 'Latest result refers to another capsule')
            print('PASS: disabling is live; manual runs preserve product identity, 0600 copies and keep-newest bounds', flush=True)
        finally:
            process.terminate()
            try:
                process.wait(timeout=20)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        check(process.returncode == 0, 'Server did not shut down cleanly')
print('PASS: owned server stopped; scratch database and sealed copies removed')
