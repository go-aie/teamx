#!/usr/bin/env python3
"""Build an independent notification module and deliver through normal startup."""
import argparse
import hashlib
import json
import os
import pathlib
import selectors
import shutil
import subprocess
import time
import urllib.error
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument('--teamx-root', required=True)
parser.add_argument('--output', required=True)
args = parser.parse_args()
root = pathlib.Path(args.teamx_root).resolve()
out = pathlib.Path(args.output).resolve()
out.mkdir(parents=True, exist_ok=True)
module = out / 'module'
shutil.copytree(pathlib.Path(__file__).resolve().parent, module)
env = {k: v for k, v in os.environ.items() if not k.startswith(('TEAMX_', 'DSH_'))}
env['GOWORK'] = 'off'

def run(argv):
    result = subprocess.run(argv, cwd=module, env=env, capture_output=True, timeout=120)
    assert result.returncode == 0, (argv, result.returncode, result.stderr.decode())
    return result.stdout

run(['go', 'mod', 'edit', '-replace=github.com/go-aie/teamx=' + str(root)])
binary = str(out / 'teamx')
run(['go', 'build', '-mod=mod', '-o', binary, './cmd/teamx'])
for source in module.rglob('*.go'):
    assert 'github.com/go-aie/teamx/internal/' not in source.read_text(), source
check = json.loads(run([binary, 'extensions', 'check']))
assert check['extensions'] == ['local-notification'] and check['tools'] == [], check
assert check['notifications'] == [{'provider': 'local-feed', 'channel': 'updates', 'schema': 1, 'input_codec': 'local-feed.notification'}], check

home = out / 'home'
home.mkdir(mode=0o700)
teams = home / 'teams' / 'local-feed'
teams.mkdir(parents=True)
(teams / 'team.json').write_text(json.dumps({
    'id': 'local-feed', 'name': 'External notifications', 'description': 'Offline external notification acceptance', 'opening_message': 'Local notification acceptance',
    'runtime': {'dsh': {'binary_path': '/usr/bin/false', 'artifact': {'source': 'unused-offline', 'version': '1'}},
                'model': {'protocol': 'openai-completions', 'base_url': 'http://127.0.0.1:1/v1', 'model_id': 'unused', 'api_key_env': 'TEAMX_EXTERNAL_FIXTURE_KEY'}},
    'members': [{'id': 'A', 'name': 'A', 'instructions': 'No model is executed.'}],
}))
token = 'external-notification-isolated-only-12345'
(home / 'token').write_text(token)
(home / 'token').chmod(0o600)
config = home / 'config.toml'
config.write_text('[server]\nlisten="127.0.0.1:0"\ndata_dir="./data"\nteams_dir="./teams"\n'
                  '[access]\noperator_id="owner_11111111111111111111111111111111"\nlogin_enabled=true\nadmin_token_file="./token"\n'
                  '[env]\nTEAMX_EXTERNAL_FIXTURE_KEY="unused-offline-only"\n')
config.chmod(0o600)
caller = {'kind': 'human', 'id': 'owner_11111111111111111111111111111111'}

class Server:
    def __init__(self, number, extra=()):
        self.log_path = out / f'server-{number}.log'
        self.log = self.log_path.open('wb')
        self.process = subprocess.Popen([binary, *extra, 'serve', '--config', str(config)], env=env, stdout=subprocess.PIPE, stderr=self.log)
        try:
            with selectors.DefaultSelector() as selector:
                selector.register(self.process.stdout, selectors.EVENT_READ)
                assert selector.select(25), 'readiness timed out'
                line = self.process.stdout.readline()
            assert line, self.log_path.read_text()
            self.ready = json.loads(line)
        except BaseException:
            self.stop()
            raise

    def request(self, path, body=None):
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(self.ready['endpoint'] + path, data=data,
                    headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'})
        try:
            with urllib.request.urlopen(request, timeout=20) as response:
                return json.load(response)
        except urllib.error.HTTPError as error:
            raise RuntimeError(f'{path}: HTTP {error.code} {error.read().decode()}')

    def command(self, **values):
        return dict(service_epoch=self.ready['service_epoch'], caller=caller, **values)

    def open(self, identity, operation):
        deadline = time.monotonic() + 20
        while True:
            try:
                opened = self.request('/v1/rooms/open', self.command(**identity, open_id=operation))
                break
            except RuntimeError as error:
                # A source snapshot publication can briefly hold the existing
                # Room change gate. Retry this exact open operation only.
                if '"code":"busy"' not in str(error) or time.monotonic() >= deadline:
                    raise
                time.sleep(.05)
        while opened['status'] == 'running':
            assert time.monotonic() < deadline, opened
            time.sleep(.05)
            opened = self.request('/v1/rooms/open?service_epoch=' + self.ready['service_epoch'] + '&caller_id=' + caller['id'] + '&open_id=' + operation)
        return opened

    def preview(self, service, member):
        return self.request('/v1/member/inbox', dict(service, caller=caller,
                    agent_id=member['agent_id'], membership_id=member['membership_id']))

    def stop(self):
        self.process.terminate()
        try:
            code = self.process.wait(timeout=35)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait()
            raise AssertionError('shutdown did not settle')
        finally:
            self.process.stdout.close()
            self.log.close()
        assert code == 0, (code, self.log_path.read_text())

server = Server(1)
try:
    room = server.request('/v1/rooms/create', server.command(team_id='local-feed', create_id='external', name='External notification fixture'))
    identity = {k: room[k] for k in ('room_id', 'room_instance')}
    opened = server.open(identity, 'open-initial')
    assert opened['status'] == 'completed', opened
    cli_env = dict(env, TEAMX_ENDPOINT=server.ready['endpoint'], TEAMX_ADMIN_TOKEN_FILE=str(home / 'token'))
    members = subprocess.run([binary, 'member', 'list', '--room', identity['room_id'], '--json'], env=cli_env, capture_output=True, timeout=20)
    assert members.returncode == 0, members.stderr.decode()
    member, = json.loads(members.stdout)['members']
    assert server.preview(opened['service'], member)['entries'] == []
finally:
    server.stop()

# Explicit opt-in is provisioned only while the isolated Store is stopped.
# This is source-owned fixture state, not a new production configuration API.
room_dir = home / 'data' / 'rooms' / identity['room_id']
directory = room_dir / 'notifications'
directory.mkdir(mode=0o700, exist_ok=True)
snapshot = directory / 'local-feed.updates.11111111111111111111111111111111.json'
target = dict(mode='inbox', agent_id=member['agent_id'], membership_id=member['membership_id'])
snapshot.write_text(json.dumps({'provider': 'local-feed', 'channel': 'updates', 'schema': 1,
    'state': dict(owner=identity, target=target, subscription='11111111111111111111111111111111', version=1, emitted=False)}))
snapshot.chmod(0o600)

server = Server(2)
try:
    opened = server.open(identity, 'open-delivery')
    assert opened['status'] == 'completed', opened
    deadline = time.monotonic() + 20
    while True:
        page = server.preview(opened['service'], member)
        saved = json.loads(snapshot.read_text())['state']
        if len(page['entries']) == 1 and not saved.get('pending'):
            break
        assert time.monotonic() < deadline, (page, saved, server.log_path.read_text())
        time.sleep(.1)
    entry = page['entries'][0]
    assert entry['item']['provider'] == 'local-feed' and entry['item']['text'] == 'A notification from the external module', page
    assert saved['emitted'] and saved['target'] == target, saved
    assert server.preview(opened['service'], member) == page, 'preview consumed input'
finally:
    server.stop()

# Reopen the source and JSONL input partition. No model, provider account or
# remote reader exists; saved content must remain readable without them.
server = Server(3)
try:
    opened = server.open(identity, 'open-recovery')
    assert opened['status'] == 'completed', opened
    recovered = server.preview(opened['service'], member)
    assert recovered['entries'] == page['entries'] and len(recovered['entries']) == 1, recovered
    assert not json.loads(snapshot.read_text())['state'].get('pending')
finally:
    server.stop()

body_files = list(room_dir.rglob('*.jsonl'))
assert body_files, 'notification was not persisted in the production JSONL repository'
def body_digests():
    return {str(p.relative_to(room_dir)): hashlib.sha256(p.read_bytes()).hexdigest() for p in body_files}
before = body_digests()
server = Server(4, ['--fixture-without-codec'])
try:
    failed = server.open(identity, 'open-missing-codec')
    assert failed['status'] == 'failed' and failed.get('error') == 'input_version_unsupported', failed
    assert body_digests() == before, 'missing codec changed saved bodies'
finally:
    server.stop()

result = {'status': 'passed', 'module': 'example.com/teamx-local-notification', 'local_replace': str(root),
          'selected': check, 'entries': recovered['entries'], 'source_ack_after_inbox_receipt': True,
          'restart_without_duplicate': True, 'missing_codec_open': failed, 'body_digests_unchanged': before,
          'real_model': False, 'platform_accounts': 0, 'remote_business_writes': 0, 'server_exits': [0, 0, 0, 0]}
(out / 'result.json').write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({'status': 'passed', 'result': str(out / 'result.json')}))
