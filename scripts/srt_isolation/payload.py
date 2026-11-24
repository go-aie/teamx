#!/usr/bin/env python3
"""Untrusted test workload. Only synthetic files and fixture endpoints."""
import errno
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import time
import urllib.request
import urllib.error

mode, filename = sys.argv[1:3]
cfg = json.loads(Path(filename).read_text())
work = Path(cfg['work'])
checks, details = {}, {}


def denied(operation):
    try:
        operation()
        return False
    except OSError as error:
        return error.errno in (errno.EPERM, errno.EACCES, errno.ENOENT, errno.EROFS)


def request(url, token=None):
    headers = {'Authorization': 'Bearer ' + token} if token else {}
    try:
        with urllib.request.urlopen(urllib.request.Request(url, headers=headers), timeout=2) as response:
            return response.status, response.read(1024).decode()
    except urllib.error.HTTPError as error:
        return error.code, ''
    except OSError:
        return None, ''


def client(argv):
    run = subprocess.run(argv, env={**os.environ, 'SRT_TEST_TOKEN': cfg['token']},
                         capture_output=True, text=True, timeout=6)
    try:
        return json.loads(run.stdout)
    except ValueError:
        return {'invalid_receipt': True, 'exit_code': run.returncode}


if mode == 'files':
    (work / 'own.txt').write_text('OWN_OK')
    checks['own_read_write'] = (work / 'own.txt').read_text() == 'OWN_OK'
    agent = Path(cfg['agent_state']) / 'progress'
    try:
        agent.write_text('STATE_OK')
        checks['agent_state_read_write'] = agent.read_text() == 'STATE_OK'
    except OSError:
        checks['agent_state_read_write'] = False
    checks['own_config_read'] = Path(cfg['readonly_config']).read_text() == 'CONFIG_OK'
    checks['own_config_write_denied'] = denied(lambda: Path(cfg['readonly_config']).write_text('BAD'))
    for label, target in cfg['private'].items():
        checks[label + '_read_denied'] = denied(lambda: Path(target).read_bytes())
        checks[label + '_write_denied'] = denied(lambda: Path(target).write_text('BAD'))
    for label, target in cfg['aliases'].items():
        checks[label + '_read_denied'] = denied(lambda: Path(target).read_bytes())
        checks[label + '_write_denied'] = denied(lambda: Path(target).write_text('BAD'))
    foreign = cfg['private']['other_space']
    checks['create_foreign_hardlink_denied'] = denied(lambda: os.link(foreign, work / 'new-hardlink'))
    checks['release_write_denied'] = denied(lambda: Path('/mnt/srt/UNAUTHORIZED').write_text('BAD'))
    checks['policy_write_denied'] = denied(lambda: Path(cfg['policy']).write_text('{}'))
    checks['minimal_environment'] = 'SRT_CORE_SECRET' not in os.environ and 'SRT_OTHER_TOKEN' not in os.environ
    for label, argv in {
        'shell': ['/bin/sh', '-c', 'cat "$1" >/dev/null', 'sh', foreign],
        'python_child': ['/usr/bin/python3', '-c', 'import sys;open(sys.argv[1]).read()', foreign],
        'node_child': ['/mnt/node/bin/node', '/mnt/node-client.mjs', 'read', foreign],
        'go_child': ['/mnt/probe-client', 'read', foreign],
    }.items():
        run = subprocess.run(argv, capture_output=True, text=True, timeout=5)
        checks[label + '_denied'] = run.returncode != 0 and any(
            text in run.stdout + run.stderr for text in ('READ_DENIED', 'Permission denied', 'No such file', 'PermissionError', 'FileNotFoundError'))
    alias = work / 'preexisting-hardlink'
    details['preexisting_hardlink_readable'] = not denied(lambda: alias.read_bytes())
    details['preexisting_hardlink_writable'] = not denied(lambda: alias.write_text('HARDLINK_CHANGED'))

elif mode == 'network':
    status, body = request(cfg['allowed'] + '/ok', cfg['token'])
    checks['python_proxy_allowed'] = status == 200 and body == cfg['expected_body']
    checks['allowed_endpoint_requires_auth'] = request(cfg['allowed'] + '/ok')[0] == 401
    for label, url in cfg['forbidden_urls'].items():
        checks[label + '_proxy_denied'] = request(url, cfg['token'])[0] == 403
    checks['redirect_to_forbidden_port_denied'] = request(cfg['allowed'] + '/redirect', cfg['token'])[0] == 403
    for label, address in cfg['raw_addresses'].items():
        try:
            with socket.create_connection(tuple(address), timeout=0.3):
                checks[label + '_raw_denied'] = False
        except OSError:
            checks[label + '_raw_denied'] = True
    for label, address in cfg['unix_sockets'].items():
        try:
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
                connection.connect(address)
                checks[label + '_denied'] = False
        except OSError as error:
            checks[label + '_denied'] = error.errno in (errno.EPERM, errno.EACCES, errno.ENOENT, errno.ECONNREFUSED)
    node = client(['/mnt/node/bin/node', '/mnt/node-client.mjs', 'http', cfg['allowed'] + '/ok'])
    go = client(['/mnt/probe-client', 'proxy', cfg['allowed'] + '/ok'])
    checks['node_proxy_allowed'] = node.get('status') == 200 and node.get('body') == cfg['expected_body']
    checks['go_explicit_proxy_allowed'] = go.get('status') == 200 and go.get('body') == cfg['expected_body']
    if cfg.get('https_allowed'):
        checks['python_https_verified'] = request(cfg['https_allowed'] + '/ok', cfg['token']) == (200, cfg['expected_body'])
        secure_node = client(['/mnt/node/bin/node', '/mnt/node-client.mjs', 'http', cfg['https_allowed'] + '/ok'])
        secure_go = client(['/mnt/probe-client', 'proxy', cfg['https_allowed'] + '/ok'])
        checks['node_https_verified'] = secure_node.get('status') == 200 and secure_node.get('body') == cfg['expected_body']
        checks['go_https_verified'] = secure_go.get('status') == 200 and secure_go.get('body') == cfg['expected_body']
    denied_go = client(['/mnt/probe-client', 'proxy', cfg['forbidden_urls']['other_space']])
    checks['go_other_space_proxy_denied'] = denied_go.get('status') == 403
    # Client compatibility observation, not an SRT enforcement failure.
    direct_go = client(['/mnt/probe-client', 'env', cfg['allowed'] + '/ok'])
    details['go_default_transport_loopback_bypasses_proxy'] = direct_go.get('connection_failed') is True

elif mode in ('hold', 'network-hold'):
    child_code = ('import pathlib,sys,time; w=pathlib.Path(sys.argv[1]); '
                  'exec("while not (w/\'arm\').exists(): time.sleep(0.02)"); '
                  '(w/"armed").write_text("armed"); time.sleep(6); (w/"late").write_text("late")')
    child = subprocess.Popen(['/usr/bin/python3', '-c', child_code, str(work)])
    if mode == 'network-hold':
        if request(cfg['allowed'] + '/ok', cfg['token'])[0] != 200:
            raise SystemExit('network hold did not establish its permitted fixture connection')
    (work / 'ready').write_text(json.dumps({'pid': os.getpid(), 'child': child.pid}))
    while True:
        if mode == 'network-hold' and request(cfg['allowed'] + '/ok', cfg['token'])[0] != 200:
            (work / 'network-failed').write_text('observed')
        time.sleep(0.1)
else:
    raise SystemExit('unknown probe mode')

print(json.dumps({'checks': checks, 'observations': details}))
raise SystemExit(0 if all(checks.values()) else 1)
