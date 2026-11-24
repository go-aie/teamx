"""Disposable login fixture. Only fictional credentials, no outside requests."""
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs

class Site(BaseHTTPRequestHandler):
    logins = 0
    form_loads = 0
    observation = {}
    def log_message(self, *_):
        pass
    def reply(self, body, kind='text/html; charset=utf-8'):
        body = body.encode()
        self.send_response(200)
        self.send_header('Content-Type', kind)
        self.send_header('Content-Length', str(len(body)))
        self.send_header('Cache-Control', 'no-store')
        self.end_headers()
        self.wfile.write(body)
    def do_GET(self):
        if self.path == '/status':
            return self.reply(json.dumps({'logins': Site.logins, 'form_loads': Site.form_loads, **Site.observation}), 'application/json')
        if self.path == '/private':
            signed = 'demo_session=fictional' in self.headers.get('Cookie', '')
            return self.reply('<h1>' + ('authenticated' if signed else 'anonymous') + '</h1>')
        if self.path == '/form':
            Site.form_loads += 1
        self.reply(('''<!doctype html><title>Computer input test</title><style>
body{font:24px sans-serif;background:#eef2f8;margin:80px}form{background:white;padding:32px;width:480px}input,button{display:block;font:24px sans-serif;margin:20px 0;padding:12px;width:400px}
</style><form action="/login" method="post"><h1>Fictional sign in</h1>
<input id="username" name="username" placeholder="Username" autofocus autocomplete="off">
<input id="password" name="password" type="password" placeholder="Password" autocomplete="off">
<button>Sign in</button></form><script>
// Observe only the disposable form, without writing input values or bypassing
// VNC. Status exposes lengths and equality flags, never password text.
if(location.pathname==='/form') {
 const generation=__FORM_GENERATION__;
 const observe=()=>fetch('/observe',{method:'POST',body:JSON.stringify({
  generation, focus:document.activeElement.id,
  username_ok:document.getElementById('username').value==='demo-user',
  password_ok:document.getElementById('password').value==='demo-pass-42',
  password_length:document.getElementById('password').value.length
 })}).catch(()=>{});
 addEventListener('input',observe);addEventListener('focusin',observe);
 setTimeout(observe,100);
}
</script>''').replace('__FORM_GENERATION__', str(Site.form_loads)))
    def do_POST(self):
        if self.path == '/observe' and int(self.headers.get('Content-Length', '0')) <= 4096:
            observation = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            if observation.get('generation') == Site.form_loads:
                Site.observation = {k: observation.get(k) for k in ['generation', 'focus', 'username_ok', 'password_ok', 'password_length']}
            return self.reply('{}', 'application/json')
        if self.path != '/login' or int(self.headers.get('Content-Length', '0')) > 4096:
            self.send_error(400)
            return
        form = parse_qs(self.rfile.read(int(self.headers['Content-Length'])).decode())
        if form.get('username') != ['demo-user'] or form.get('password') != ['demo-pass-42']:
            self.send_error(403)
            return
        Site.logins += 1
        self.send_response(303)
        self.send_header('Set-Cookie', 'demo_session=fictional; Path=/; Max-Age=604800; HttpOnly; SameSite=Lax')
        self.send_header('Location', '/private')
        self.send_header('Content-Length', '0')
        self.end_headers()

ThreadingHTTPServer(('0.0.0.0', 18765), Site).serve_forever()
