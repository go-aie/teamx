"""Explicit real-provider probe inside the disposable container, no model needed."""

import base64
from concurrent.futures import ThreadPoolExecutor
import hashlib
import io
import json
import os
from pathlib import Path
import re
import secrets
import socket
import struct
import subprocess
import sys
import threading
import time
import urllib.request

from PIL import Image
from screens import config, processes, start, stop, HERE, RUN
from view import read

OUT = Path('/evidence')
OUT.mkdir(exist_ok=True)
PHASE = sys.argv[1]
RESULT = {'phase': PHASE, 'passed': False, 'checks': [], 'calls': []}
LOCK = json.loads((HERE.parent / 'tools.lock.json').read_text())


def save(name, value):
    (OUT / name).write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')


def checkpoint(name, value):
    RESULT['checks'].append({'name': name, 'evidence': value})
    save(PHASE + '.json', RESULT)
    print(name, flush=True)


def request(screen, path, body=None, timeout=45):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(f"http://127.0.0.1:{config(screen)['aiod']}" + path,
                                 data, {'Content-Type': 'application/json'})
    with urllib.request.urlopen(req, timeout=timeout) as response:
        return json.load(response)


def rpc(screen, method, params=None):
    body = {'jsonrpc': '2.0', 'id': secrets.token_hex(8), 'method': method}
    if params is not None:
        body['params'] = params
    response = request(screen, '/mcp', body)
    assert 'error' not in response, response
    return response['result']


def call(screen, name, args=None):
    assert name in LOCK['selected_names'], name
    started = time.monotonic()
    result = rpc(screen, 'tools/call', {'name': name, 'arguments': args or {}})
    record = {'screen': screen, 'name': name, 'arguments': args or {},
              'started_monotonic': started, 'seconds': round(time.monotonic() - started, 3),
              'result': json.loads(json.dumps(result))}
    for index, block in enumerate(record['result'].get('content', [])):
        if block.get('type') == 'image':
            data = base64.b64decode(block.pop('data'), validate=True)
            block.update(bytes=len(data), sha256=hashlib.sha256(data).hexdigest())
            if name == 'browser_gui_screenshot':
                filename = f'{PHASE}-{screen}-desktop.png'
                (OUT / filename).write_bytes(data)
                block['artifact'] = filename
    RESULT['calls'].append(record)
    assert not result.get('isError'), record
    return result


def text(result):
    return '\n'.join(b['text'] for b in result['content'] if b['type'] == 'text')


def page(screen):
    return text(call(screen, 'browser_get_text'))


def gui(screen, kind, **args):
    return call(screen, 'browser_gui_execute_action', {'action': {'action_type': kind, **args}})


def click(screen, fragment):
    elements = text(call(screen, 'browser_get_clickable_elements'))
    matches = [line for line in elements.splitlines() if fragment in line]
    assert len(matches) == 1, (fragment, elements)
    index = int(re.match(r'\[(\d+)\]', matches[0])[1])
    return call(screen, 'browser_click', {'index': index})


def wait_for(fn, seconds=90):
    deadline = time.monotonic() + seconds
    last = None
    while time.monotonic() < deadline:
        try:
            value = fn()
            if value:
                return value
        except Exception as error:
            last = error
        time.sleep(0.25)
    raise TimeoutError(str(last))


def ready():
    since = time.monotonic()
    for screen in ('A', 'B'):
        wait_for(lambda: 'browser_navigate' in {t['name'] for t in rpc(screen, 'tools/list')['tools']})
        wait_for(lambda: f'TeamX screen {screen}' in page(screen))
        info = request(screen, '/v2/computer/info')['data']
        assert info['display'] == config(screen)['display'], info
        wait_for(lambda: all(name in '\n'.join(p['command'] for p in processes(screen)) for name in
                            ('Xvnc', 'xfce4-session', 'dbus-daemon', 'aiod start', 'computer-use', 'mcp-server-browser')))
    checkpoint('two_screens_ready', {'probe_seconds': round(time.monotonic() - since, 3), 'observed_at_unix': time.time()})


def discovery():
    tools = rpc('A', 'tools/list')['tools']
    peer = rpc('B', 'tools/list')['tools']
    canonical = lambda x: json.dumps(x, sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode()
    assert canonical(tools) == canonical(peer)
    selected = [tool for tool in tools if tool['name'] in LOCK['selected_names']]
    assert canonical(selected) == canonical(LOCK['selected_tools']), 'native contract drift'
    save('native-tools.json', tools)
    checkpoint('native_contract_locked', {'discovered': len(tools), 'selected': len(selected),
        'all_tools_sha256': hashlib.sha256(canonical(tools)).hexdigest(),
        'selected_sha256': hashlib.sha256(canonical(selected)).hexdigest(),
        'excluded_names': [t['name'] for t in tools if t['name'] not in LOCK['selected_names']]})


class RFB:
    def __init__(self, port):
        self.stream = socket.create_connection(('127.0.0.1', port), timeout=15)
        s = self.stream
        assert read(s, 12) == b'RFB 003.008\n'
        s.sendall(b'RFB 003.008\n')
        options = read(s, read(s, 1)[0])
        assert 1 in options
        s.sendall(b'\x01')
        assert read(s, 4) == b'\0\0\0\0'
        s.sendall(b'\x01')
        header = read(s, 24)
        self.width, self.height = struct.unpack('>HH', header[:4])
        self.name = read(s, struct.unpack('>I', header[20:24])[0]).decode()
        # RGB32 little-endian, raw rectangles only, no clipboard extensions.
        s.sendall(b'\0\0\0\0' + struct.pack('>BBBBHHHBBBxxx', 32, 24, 0, 1, 255, 255, 255, 16, 8, 0))
        s.sendall(struct.pack('>BBHi', 2, 0, 1, 0))

    def frame(self):
        s = self.stream
        s.sendall(struct.pack('>BBHHHH', 3, 0, 0, 0, self.width, self.height))
        while True:
            kind = read(s, 1)[0]
            if kind == 0:
                count = struct.unpack('>H', read(s, 3)[1:])[0]
                image = Image.new('RGB', (self.width, self.height))
                area = 0
                for _ in range(count):
                    x, y, width, height, encoding = struct.unpack('>HHHHi', read(s, 12))
                    assert encoding == 0, encoding
                    data = read(s, width * height * 4)
                    image.paste(Image.frombytes('RGB', (width, height), data, 'raw', 'BGRX'), (x, y))
                    area += width * height
                assert area == self.width * self.height
                return image
            elif kind == 2:  # Bell
                continue
            elif kind == 3:  # server clipboard
                size = struct.unpack('>I', read(s, 7)[3:])[0]
                read(s, size)
            else:
                raise AssertionError(kind)

    def close(self):
        self.stream.close()


def viewing():
    records = []
    for screen in ('A', 'B'):
        result = call(screen, 'browser_gui_screenshot')
        data = base64.b64decode(next(b['data'] for b in result['content'] if b['type'] == 'image'))
        shot = Image.open(io.BytesIO(data)).convert('RGB')
        viewer = RFB(config(screen)['view'])
        frame = viewer.frame()
        viewer.close()
        assert shot.size == frame.size == (1280, 900)
        expected_background = (241, 245, 255) if screen == 'A' else (255, 244, 232)
        assert shot.getpixel((1100, 800)) == frame.getpixel((1100, 800)) == expected_background
        # Most of the page must agree even if the pointer/caret moves between captures.
        pixels = [(x, y) for x in range(40, 1200, 40) for y in range(150, 850, 40)]
        fraction = sum(shot.getpixel(p) == frame.getpixel(p) for p in pixels) / len(pixels)
        assert fraction > 0.95, fraction
        frame.save(OUT / f'{PHASE}-{screen}-vnc.png')
        records.append({'screen': screen, 'matched_sample_fraction': fraction, 'vnc_name': viewer.name,
                        'size': list(frame.size)})
    checkpoint('gui_image_blocks_and_corresponding_vnc_frames', records)


def parallel():
    markers = {s: s + '-' + secrets.token_hex(10) for s in ('A', 'B')}
    barrier = threading.Barrier(2)

    def task(screen):
        call(screen, 'browser_navigate', {'url': f'http://127.0.0.1:18765/?screen={screen}'})
        click(screen, 'id="marker"')
        barrier.wait(timeout=30)
        since = time.monotonic()
        # A's GUI and B's page operations overlap, then swap routes.
        if screen == 'A':
            gui(screen, 'TYPING', text=markers[screen], use_clipboard=False)
        else:
            call(screen, 'browser_form_input_fill', {'selector': '#marker', 'value': markers[screen], 'clear': True})
            click(screen, 'id="count"')
        return {'screen': screen, 'start': since, 'end': time.monotonic(), 'text': page(screen)}

    with ThreadPoolExecutor(max_workers=2) as pool:
        results = list(pool.map(task, ('A', 'B')))
    assert max(r['start'] for r in results) < min(r['end'] for r in results), results
    for record in results:
        screen = record['screen']
        assert markers[screen] in record['text'], record
        assert markers['B' if screen == 'A' else 'A'] not in record['text']
    assert 'Clicked 1' in results[1]['text']
    # Different tab URLs, private download destinations and native result blocks.
    for screen in ('A', 'B'):
        call(screen, 'browser_new_tab', {'url': f'http://127.0.0.1:18765/?screen={screen}&tab=private-{screen}'})
        tabs = text(call(screen, 'browser_tab_list'))
        assert f'private-{screen}' in tabs and f'private-{"B" if screen == "A" else "A"}' not in tabs
        call(screen, 'browser_switch_tab', {'index': 0})
        click(screen, f'href="/download?screen={screen}"')
        path = Path(f'/workspace/agents/{screen}/downloads/screen-{screen}.txt')
        wait_for(lambda: path.exists())
        assert path.read_bytes() == f'download-{screen}\n'.encode()
        assert not (path.parent / f'screen-{"B" if screen == "A" else "A"}.txt').exists()
        checkpoint('private_tabs_and_download_' + screen, {'tabs': tabs, 'file': str(path),
            'sha256': hashlib.sha256(path.read_bytes()).hexdigest(),
            'download_result': call(screen, 'browser_get_download_list')})
    # Collaboration is explicitly through shared, not through browser profiles.
    a, b = config('A'), config('B')
    subprocess.run(['python3', '-c', "from pathlib import Path; Path('/workspace/shared/exchange.txt').write_text('from-A\\n')"],
                   user=a['uid'], group=a['gid'], extra_groups=[], umask=0o007, check=True)
    subprocess.run(['python3', '-c', "from pathlib import Path; p=Path('/workspace/shared/exchange.txt'); assert p.read_text()=='from-A\\n'; p.write_text(p.read_text()+'from-B\\n')"],
                   user=b['uid'], group=b['gid'], extra_groups=[], check=True)
    assert Path('/workspace/shared/exchange.txt').read_text() == 'from-A\nfrom-B\n'
    checkpoint('parallel_input_focus_tabs_downloads_and_shared_files', {'markers': markers, 'tasks': results})


def readonly():
    click('A', 'id="marker"')
    peer_pointer = request('B', '/v2/computer/cursor')['data']
    gui('A', 'MOVE_TO', x=300, y=344)
    gui('A', 'CLICK', x=300, y=344)
    assert request('A', '/v2/computer/cursor')['data'] == {'x': 300, 'y': 344}
    assert request('B', '/v2/computer/cursor')['data'] == peer_pointer
    # Restore input focus using the discovered element; browser banners can move it.
    click('A', 'id="marker"')
    before = page('A')
    pointer = request('A', '/v2/computer/cursor')['data']
    rejected = []
    messages = {
        'keyboard': struct.pack('>BBHI', 4, 1, 0, ord('Q')),
        'mouse': struct.pack('>BBHH', 5, 1, 12, 12),
        'paste': struct.pack('>BxxxI', 6, 9) + b'forbidden',
        'unknown_extension': b'\xfb',
    }
    for name, message in messages.items():
        viewer = RFB(config('A')['view'])
        viewer.frame()
        # Deliberately fragment the message: enforcing read-only is not a UI flag.
        viewer.stream.sendall(message[:1])
        try:
            viewer.stream.sendall(message[1:])
            closed = viewer.stream.recv(1) == b''
        except ConnectionResetError:
            closed = True
        assert closed, name
        viewer.close()
        assert page('A') == before
        assert request('A', '/v2/computer/cursor')['data'] == pointer
        rejected.append(name)
    # Positive control over actual RFB input, while B continues using page MCP.
    def human():
        viewer = RFB(config('A')['vnc'])
        viewer.stream.sendall(struct.pack('>BBHI', 4, 1, 0, ord('Z')) + struct.pack('>BBHI', 4, 0, 0, ord('Z')))
        viewer.close()
        return wait_for(lambda: 'Z' in page('A'))
    def agent():
        click('B', 'id="count"')
        return page('B')
    with ThreadPoolExecutor(max_workers=2) as pool:
        fa, fb = pool.submit(human), pool.submit(agent)
        assert fa.result()
        assert 'Clicked' in fb.result()
    checkpoint('server_readonly_rejects_input_and_human_A_does_not_block_B', {'rejected': rejected,
        'A_after': page('A'), 'B_after': page('B')})


def login():
    for screen in ('A', 'B'):
        assert 'Account: anonymous' in page(screen), 'fresh profile was not anonymous'
        if screen == 'B':
            assert 'fictional-A' not in page(screen), 'B inherited A login'
        call(screen, 'browser_form_input_fill', {'selector': '[name=account]', 'value': 'fictional-' + screen})
        click(screen, '<button>Sign in</button>')
        wait_for(lambda: 'Account: fictional-' + screen in page(screen))
    checkpoint('fictional_logins_are_private', {'A': page('A'), 'B': page('B')})


def page_tool_smoke():
    # Cover every selected native tool; the main acceptance already proves the
    # concurrent input, login, download, screenshot and control behavior.
    call('A', 'browser_navigate', {'url': 'http://127.0.0.1:18765/?screen=A&smoke=1'})
    call('A', 'browser_go_back')
    def active_url():
        result = call('A', 'browser_tab_list')
        return next(tab['url'] for tab in result['structuredContent']['tabList'] if tab['active'])
    assert 'smoke=1' not in active_url()
    call('A', 'browser_go_forward')
    assert 'smoke=1' in active_url()
    assert 'TeamX screen A' in text(call('A', 'browser_get_markdown'))
    assert 'download?screen=A' in text(call('A', 'browser_read_links'))
    call('A', 'browser_hover', {'selector': '#count'})
    call('A', 'browser_select', {'selector': '#choice', 'value': 'two'})
    assert 'Selected: two' in page('A')
    call('A', 'browser_press_key', {'key': 'Tab'})
    call('A', 'browser_scroll', {'amount': 200})
    call('A', 'browser_scroll', {'amount': -200})
    result = call('A', 'browser_screenshot', {'name': 'native-page-A', 'fullPage': False})
    images = [b for b in result['content'] if b['type'] == 'image']
    assert images, result
    Image.open(io.BytesIO(base64.b64decode(images[0]['data']))).verify()
    call('A', 'browser_switch_tab', {'index': 1})
    call('A', 'browser_close_tab')
    remaining = call('A', 'browser_tab_list')['structuredContent']['tabList']
    assert len(remaining) == 1 and 'smoke=1' in remaining[0]['url']
    names = {record['name'] for record in RESULT['calls']}
    assert names == set(LOCK['selected_names']), set(LOCK['selected_names']) - names
    checkpoint('all_selected_native_tools_called_successfully', {'count': len(names), 'names': sorted(names)})


def background_and_stop():
    cfg = config('A')
    env = dict(os.environ, HOME=cfg['home'], USER='screen-a')
    env.update(json.loads((RUN / 'A/environment.json').read_text()))
    subprocess.run(['xfconf-query', '-c', 'xfce4-keyboard-shortcuts', '-p', '/commands/custom/F8',
                    '-n', '-t', 'string', '-s', 'python3 ' + str(HERE / 'background.py')],
                   env=env, user=cfg['uid'], group=cfg['gid'], extra_groups=[], check=True)
    gui('A', 'PRESS', key='F8')
    pidfile = Path(cfg['home']) / 'gui-background.pid'
    wait_for(lambda: pidfile.exists(), seconds=20)
    background_pid = int(pidfile.read_text())
    background = next(p for p in processes('A') if p['pid'] == background_pid)
    assert 'background.py' in background['command']
    launcher = next(p for p in processes('A') if 'dbus-run-session --' in p['command'])
    assert background['ppid'] == 1 and background['sid'] != launcher['sid']
    before_b = {p['pid'] for p in processes('B')}
    # B repeatedly completes real native calls while A is being stopped.
    def working():
        samples = []
        for _ in range(12):
            samples.append(page('B'))
            time.sleep(0.5)
        return samples
    with ThreadPoolExecutor(max_workers=1) as pool:
        peer = pool.submit(working)
        stopped = stop('A')
        continued = peer.result()
    assert stopped['stopped'] and not Path(f'/proc/{background_pid}').exists()
    assert before_b <= {p['pid'] for p in processes('B')}, 'stopping A killed B processes'
    assert all('Account: fictional-B' in value for value in continued)
    for port in ('cdp', 'vnc', 'view', 'mcp', 'aiod', 'worker'):
        with socket.socket() as stream:
            assert stream.connect_ex(('127.0.0.1', cfg[port])) != 0, port
    checkpoint('entire_A_uid_stopped_including_detached_GUI_child_B_continues', {
        'background': background, 'stop': stopped, 'B_successful_calls_during_stop': len(continued)})
    live_peer_sockets = {str(p) for p in Path('/tmp/.ICE-unix').iterdir() if p.lstat().st_uid == config('B')['uid']}
    stale = Path('/tmp/.ICE-unix/stale-A-proof')
    with socket.socket(socket.AF_UNIX) as stream:
        stream.bind(str(stale))
    os.chown(stale, cfg['uid'], cfg['gid'])
    started = time.monotonic()
    start('A')
    assert not stale.exists()
    assert live_peer_sockets <= {str(p) for p in Path('/tmp/.ICE-unix').iterdir()}
    wait_for(lambda: 'Account: fictional-A' in page('A'))
    assert 'Account: fictional-B' in page('B')
    checkpoint('desktop_reconstruction_preserves_private_login', {'seconds': time.monotonic() - started,
        'A': page('A'), 'B': page('B'), 'stale_A_socket_removed': True,
        'B_live_ICE_sockets_preserved': sorted(live_peer_sockets)})


def inventory():
    result = {}
    for screen in ('A', 'B'):
        procs = processes(screen)
        commands = '\n'.join(p['command'] for p in procs)
        for required in ('Xvnc', 'xfce4-session', 'dbus-daemon', '--remote-debugging-port=', 'aiod start', 'computer-use', 'mcp-server-browser'):
            assert required in commands, (screen, required)
        result[screen] = {'config': config(screen), 'environment': json.loads((RUN / screen / 'environment.json').read_text()),
                          'processes': procs, 'computer': request(screen, '/v2/computer/info')['data']}
        with urllib.request.urlopen(f"http://127.0.0.1:{config(screen)['cdp']}/json/list") as response:
            tabs = json.load(response)
        urls = [tab['url'] for tab in tabs if tab['type'] == 'page']
        assert urls and all('screen=' + screen in url for url in urls), urls
        result[screen]['cdp_page_urls'] = urls
    assert result['A']['environment']['DBUS_SESSION_BUS_ADDRESS'] != result['B']['environment']['DBUS_SESSION_BUS_ADDRESS']
    checkpoint('dedicated_uid_display_dbus_cdp_mcp_worker_inventory', result)


def metrics(stop_event):
    samples = []
    while not stop_event.is_set():
        try:
            cpu = dict(line.split() for line in Path('/sys/fs/cgroup/cpu.stat').read_text().splitlines())
            samples.append({'monotonic': time.monotonic(), 'usage_usec': int(cpu['usage_usec']),
                            'memory_bytes': int(Path('/sys/fs/cgroup/memory.current').read_text())})
        except OSError:
            pass
        stop_event.wait(0.25)
    if len(samples) > 1:
        seconds = samples[-1]['monotonic'] - samples[0]['monotonic']
        RESULT['resources'] = {'seconds': seconds, 'samples': len(samples),
            'cpu_core_average': (samples[-1]['usage_usec'] - samples[0]['usage_usec']) / seconds / 1e6,
            'peak_sampled_memory_bytes': max(s['memory_bytes'] for s in samples),
            'cgroup_memory_peak_bytes': int(Path('/sys/fs/cgroup/memory.peak').read_text()),
            'oom_events': Path('/sys/fs/cgroup/memory.events').read_text()}
        save(PHASE + '-resources.json', samples)


if __name__ == '__main__':
    event = threading.Event()
    sampler = threading.Thread(target=metrics, args=(event,))
    sampler.start()
    try:
        ready()
        discovery()
        if PHASE == 'exercise':
            inventory()
            parallel()
            readonly()
            login()
            viewing()
            page_tool_smoke()
            background_and_stop()
        elif PHASE == 'persistence':
            for screen in ('A', 'B'):
                assert 'Account: fictional-' + screen in page(screen)
                assert 'Account: fictional-' + ('B' if screen == 'A' else 'A') not in page(screen)
            checkpoint('container_restart_preserves_both_private_logins', {'A': page('A'), 'B': page('B')})
            inventory()
            viewing()
        else:
            raise ValueError(PHASE)
        RESULT['passed'] = True
    except Exception as error:
        RESULT['error'] = repr(error)
        raise
    finally:
        event.set()
        sampler.join()
        save(PHASE + '.json', RESULT)
