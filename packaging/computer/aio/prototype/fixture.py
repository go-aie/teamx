"""Fictional local login/form/download site; no real accounts or remote writes."""

import html
import json
from http.cookies import SimpleCookie
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse


class Site(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def reply(self, body, kind='text/html; charset=utf-8', headers=None, code=200):
        body = body.encode() if isinstance(body, str) else body
        self.send_response(code)
        self.send_header('Content-Type', kind)
        self.send_header('Content-Length', str(len(body)))
        self.send_header('Cache-Control', 'no-store')
        for name, value in (headers or {}).items():
            self.send_header(name, value)
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        query = parse_qs(urlparse(self.path).query)
        screen = query.get('screen', ['A'])[0]
        if screen not in ('A', 'B'):
            self.reply('invalid screen', code=400)
            return
        cookies = SimpleCookie(self.headers.get('Cookie', ''))
        account = cookies.get('account')
        account = account.value if account else 'anonymous'
        if urlparse(self.path).path == '/download':
            self.reply(('download-' + screen + '\n').encode(), 'application/octet-stream',
                       {'Content-Disposition': f'attachment; filename="screen-{screen}.txt"'})
            return
        if urlparse(self.path).path == '/identity':
            self.reply(json.dumps({'account': account}), 'application/json')
            return
        color = '#f1f5ff' if screen == 'A' else '#fff4e8'
        self.reply(f'''<!doctype html><html><head><title>TeamX screen {screen}</title>
<style>body{{font:24px sans-serif;margin:36px;background:{color}}}input,button,a{{font:24px sans-serif;padding:8px;margin:8px}}input{{width:380px}}</style></head>
<body><h1>TeamX screen {screen}</h1><p id="account">Account: {html.escape(account)}</p>
<label>Marker <input id="marker" aria-label="Marker" autofocus></label><button id="count" onclick="this.textContent='Clicked '+(++window.count)">Click counter</button><p id="echo"></p>
<form method="post" action="/login?screen={screen}"><label>Fictional account <input name="account" aria-label="Fictional account"></label><button>Sign in</button></form>
<a href="/download?screen={screen}" download>Download {screen}</a>
<label>Choice <select id="choice" aria-label="Choice" onchange="document.querySelector('#choice-echo').textContent='Selected: '+this.value"><option value="one">One</option><option value="two">Two</option></select></label><p id="choice-echo">Selected: one</p>
<script>window.count=0;let field=document.querySelector('#marker');function echo(){{document.querySelector('#echo').textContent='Marker value: '+field.value}}field.addEventListener('input',()=>{{localStorage.marker=field.value;echo()}});field.value=localStorage.marker||'';echo();</script>
</body></html>''')

    def do_POST(self):
        if urlparse(self.path).path != '/login':
            self.reply('missing', code=404)
            return
        size = int(self.headers.get('Content-Length', '0'))
        if size > 4096:
            self.reply('too large', code=413)
            return
        account = parse_qs(self.rfile.read(size).decode()).get('account', [''])[0]
        if account not in ('fictional-A', 'fictional-B'):
            self.reply('invalid fictional account', code=400)
            return
        self.reply('', headers={'Location': '/?' + urlparse(self.path).query,
                                'Set-Cookie': 'account=' + account + '; Path=/; Max-Age=604800; HttpOnly; SameSite=Lax'}, code=303)


if __name__ == '__main__':
    ThreadingHTTPServer(('127.0.0.1', 18765), Site).serve_forever()
