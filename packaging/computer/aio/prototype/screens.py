"""Fixed two-screen feasibility launcher, not the production Screen Runtime API.

All controls run through docker exec as root. Nothing is published to the host.
The container PID 1 reaps adopted children, including GUI-launched descendants.
"""

import json
import os
from pathlib import Path
import pwd
import signal
import subprocess
import sys
import time

HERE = Path(__file__).resolve().parent
STATE = Path('/var/lib/teamx-computer')
RUN = Path('/run/teamx-computer')


def config(screen):
    assert screen in ('A', 'B'), screen
    index = ord(screen) - ord('A')
    account = pwd.getpwnam('screen-' + screen.lower())
    return dict(screen=screen, uid=account.pw_uid, gid=account.pw_gid,
                home=account.pw_dir, display=':' + str(11 + index),
                cdp=9223 + index, vnc=5911 + index, view=5931 + index,
                mcp=18211 + index, aiod=18311 + index, worker=18411 + index)


def processes(screen):
    uid = config(screen)['uid']
    found = []
    for proc in Path('/proc').glob('[0-9]*'):
        try:
            fields = dict(line.split(':', 1) for line in (proc / 'status').read_text().splitlines() if ':' in line)
            if uid not in map(int, fields['Uid'].split()):
                continue
            found.append(dict(pid=int(proc.name), state=fields['State'].strip(),
                              ppid=int(fields['PPid']), sid=os.getsid(int(proc.name)),
                              name=fields['Name'].strip(),
                              command=(proc / 'cmdline').read_bytes().replace(b'\0', b' ').decode(errors='replace')[:4096]))
        except (FileNotFoundError, ProcessLookupError):
            pass
    return found


def owned_dir(path, cfg, mode=0o700):
    path.mkdir(parents=True, exist_ok=True)
    os.chown(path, cfg['uid'], cfg['gid'])
    path.chmod(mode)


def start(screen):
    cfg = config(screen)
    if processes(screen):
        raise RuntimeError('screen still has processes; stop and verify first')
    # XFCE's ICE sockets survive abrupt desktop termination. Container restart
    # reuses PIDs, so an old socket can prevent the new session from listening.
    # The UID is exclusive to this stopped screen; never clear another UID's sockets.
    for path in Path('/tmp/.ICE-unix').iterdir():
        if path.lstat().st_uid == cfg['uid']:
            path.unlink()
    home = Path(cfg['home'])
    for path in (home, RUN / screen, home / 'profile', home / '.config', home / '.cache',
                 Path('/workspace/agents') / screen, Path('/workspace/agents') / screen / 'downloads'):
        owned_dir(path, cfg)
    # A forced desktop reconstruction can leave these locks pointing to the old PID.
    for name in ('SingletonLock', 'SingletonSocket', 'SingletonCookie'):
        (home / 'profile' / name).unlink(missing_ok=True)
    # Permit persistent cookies, and keep downloads on the screen's delivery root.
    prefs = home / 'profile/Default/Preferences'
    if not prefs.exists():
        owned_dir(prefs.parent, cfg)
        prefs.write_text(json.dumps({'download': {'default_directory': f'/workspace/agents/{screen}/downloads',
                                                'prompt_for_download': False},
                                     'profile': {'default_content_setting_values': {'cookies': 1}}}))
        os.chown(prefs, cfg['uid'], cfg['gid'])
    env = dict(os.environ)
    env.update(HOME=cfg['home'], USER='screen-' + screen.lower(), LOGNAME='screen-' + screen.lower(),
               DISPLAY=cfg['display'], XAUTHORITY=str(RUN / screen / 'Xauthority'),
               XDG_RUNTIME_DIR=str(RUN / screen), XDG_CONFIG_HOME=str(home / '.config'),
               XDG_CACHE_HOME=str(home / '.cache'))
    # Do not inherit the upstream image's global CDP, MCP or display configuration.
    for name in ('DBUS_SESSION_BUS_ADDRESS', 'EXTRA_MCP_SERVERS', 'AIO_API_KEY', 'AIO_DEFAULT_USER'):
        env.pop(name, None)
    env.pop('SESSION_MANAGER', None)
    with (RUN / screen / 'session.log').open('ab', buffering=0) as log:
        process = subprocess.Popen(['dbus-run-session', '--', 'python3', str(HERE / 'screens.py'), 'session', screen],
                                   user=cfg['uid'], group=cfg['gid'], extra_groups=[],
                                   env=env, cwd=home, start_new_session=True, stdout=log, stderr=log)
    return dict(config=cfg, launcher_pid=process.pid)


def stop(screen):
    before = processes(screen)
    started = time.monotonic()
    # Ask Chromium to flush its profile before terminating the display and D-Bus.
    for proc in before:
        if proc['name'] in ('chrome', 'chromium') and '--type=' not in proc['command']:
            try:
                os.kill(proc['pid'], signal.SIGTERM)
            except ProcessLookupError:
                pass
    time.sleep(1)
    for sig, budget in ((signal.SIGTERM, 4), (signal.SIGKILL, 4)):
        deadline = time.monotonic() + budget
        while time.monotonic() < deadline:
            if os.getpid() == 1:
                reap()
            remaining = processes(screen)
            if not remaining:
                break
            for proc in remaining:
                try:
                    os.kill(proc['pid'], sig)
                except ProcessLookupError:
                    pass
            time.sleep(0.1)
    remaining = processes(screen)
    result = dict(screen=screen, before=before, remaining=remaining,
                  stopped=not remaining, seconds=round(time.monotonic() - started, 3))
    if remaining:
        raise RuntimeError(json.dumps(result))
    cfg = config(screen)
    number = cfg['display'][1:]
    Path('/tmp/.X' + number + '-lock').unlink(missing_ok=True)
    Path('/tmp/.X11-unix/X' + number).unlink(missing_ok=True)
    return result


def reap():
    try:
        while os.waitpid(-1, os.WNOHANG)[0]:
            pass
    except ChildProcessError:
        pass


def session(screen):
    os.umask(0o007)
    cfg = config(screen)
    env = dict(os.environ)
    env.update(BROWSER_REMOTE_DEBUGGING_HOST='127.0.0.1', BROWSER_REMOTE_DEBUGGING_PORT=str(cfg['cdp']),
               AIO_COMPUTER_USE_LISTEN=f"127.0.0.1:{cfg['worker']}",
               AIO_COMPUTER_USE_URL=f"http://127.0.0.1:{cfg['worker']}",
               AIO_RUNTIME_DIR=str(RUN / screen / 'aiod'),
               EXTRA_MCP_SERVERS=json.dumps({'browser': {'url': f"http://127.0.0.1:{cfg['mcp']}/mcp"}}))
    children = []

    def launch(name, args):
        log = (RUN / screen / (name + '.log')).open('ab', buffering=0)
        child = subprocess.Popen(args, env=env, stdout=log, stderr=log)
        log.close()
        children.append(child)
        return child

    subprocess.run(['xauth', '-f', env['XAUTHORITY'], 'add', cfg['display'], '.', os.urandom(16).hex()], check=True)
    launch('vnc', ['Xvnc', cfg['display'], '-geometry', '1280x900', '-depth', '24', '-localhost',
                   '-rfbport', str(cfg['vnc']), '-SecurityTypes', 'None', '-AlwaysShared',
                   '-auth', env['XAUTHORITY'], '-nolisten', 'tcp'])
    deadline = time.monotonic() + 60
    while subprocess.run(['xdpyinfo'], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode:
        if time.monotonic() > deadline:
            raise RuntimeError('Xvnc startup timed out')
        time.sleep(0.2)
    launch('desktop', ['xfce4-session'])
    launch('browser', ['/usr/local/bin/browser', '--no-sandbox', '--disable-dev-shm-usage', '--disable-gpu',
                       '--disable-backgrounding-occluded-windows', '--disable-background-timer-throttling',
                       '--disable-renderer-backgrounding', '--disable-background-networking',
                       '--disable-infobars',
                       '--no-first-run', '--no-default-browser-check', '--disable-session-crashed-bubble',
                       '--force-renderer-accessibility', '--password-store=basic', '--use-mock-keychain',
                       '--user-data-dir=' + cfg['home'] + '/profile',
                       '--remote-debugging-address=127.0.0.1', '--remote-debugging-port=' + str(cfg['cdp']),
                       '--window-size=1280,840', '--window-position=0,30',
                       'http://127.0.0.1:18765/?screen=' + screen])
    launch('worker', ['computer-use'])
    launch('browser-mcp', ['mcp-server-browser', '--port', str(cfg['mcp']), '--host', '127.0.0.1',
                           '--browser', 'chrome', '--output-dir', f'/workspace/agents/{screen}/downloads',
                           '--cdp-endpoint', f"http://127.0.0.1:{cfg['cdp']}/json/version", '--viewport-size', '0,0'])
    launch('aiod', ['aiod', 'start', '--host', '127.0.0.1', '--port', str(cfg['aiod'])])
    launch('view', ['python3', str(HERE / 'view.py'), str(cfg['view']), str(cfg['vnc'])])
    (RUN / screen / 'environment.json').write_text(json.dumps({key: env[key] for key in
        ('DISPLAY', 'XAUTHORITY', 'XDG_RUNTIME_DIR', 'DBUS_SESSION_BUS_ADDRESS',
         'BROWSER_REMOTE_DEBUGGING_PORT', 'AIO_COMPUTER_USE_URL')}, indent=2))
    while True:
        failed = [p.pid for p in children if p.poll() is not None]
        if failed:
            raise RuntimeError('session child exited: ' + str(failed))
        time.sleep(0.2)


def serve():
    RUN.mkdir(parents=True, exist_ok=True)
    STATE.mkdir(parents=True, exist_ok=True)
    subprocess.Popen(['python3', str(HERE / 'fixture.py')])
    for screen in ('A', 'B'):
        print(json.dumps(start(screen)), flush=True)
    def shutdown(*_):
        for screen in ('A', 'B'):
            print(json.dumps(stop(screen)), flush=True)
        sys.exit(0)
    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGINT, shutdown)
    while True:
        reap()
        time.sleep(0.05)


if __name__ == '__main__':
    action = sys.argv[1]
    if action == 'serve':
        serve()
    elif action == 'session':
        session(sys.argv[2])
    elif action == 'start':
        print(json.dumps(start(sys.argv[2])))
    elif action == 'stop':
        print(json.dumps(stop(sys.argv[2])))
    elif action == 'status':
        print(json.dumps({s: dict(config=config(s), processes=processes(s)) for s in ('A', 'B')}))
    else:
        raise SystemExit('unknown action')
