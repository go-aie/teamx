"""Container-local, disposable browser acceptance site (Python stdlib only).

Expected answers and receipts belong to the verifier, outside the DSH workspace.
The browser must fetch the dynamic value, copy the displayed form challenge,
follow the redirect and download the per-run fixed payload.
"""

import hashlib
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlparse


class Fixture(BaseHTTPRequestHandler):
    root = Path("/tmp/teamx-browser-fixture")

    def log_message(self, *_):
        pass

    def reply(self, body, kind="text/html; charset=utf-8", code=200, headers=None):
        body = body.encode() if isinstance(body, str) else body
        self.send_response(code)
        self.send_header("Content-Type", kind)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        for key, value in (headers or {}).items():
            self.send_header(key, value)
        self.end_headers()
        self.wfile.write(body)

    def record(self, event):
        with (self.root / "receipts.jsonl").open("a") as stream:
            stream.write(json.dumps(event) + "\n")

    def do_GET(self):
        path = urlparse(self.path).path
        if path == "/unavailable":
            self.record({"kind": "unavailable", "status": 503})
            self.reply("<h1>Service unavailable</h1><p>Controlled HTTP 503 failure.</p>", code=503)
            return
        parts = path.strip("/").split("/")
        if len(parts) != 2 or parts[0] not in {"1", "2", "3"}:
            self.reply("Missing page", code=404)
            return
        run, action = parts
        expected = json.loads((self.root / "expected.json").read_text())[run]
        if action == "start":
            self.record({"kind": "navigation", "run": run})
            self.reply(f'''<!doctype html><html><head><title>TeamX browser trial {run}</title>
<style>body{{font:24px sans-serif;margin:48px;background:white}} input,button,a{{font:24px sans-serif;margin:12px;padding:12px}}</style></head>
<body><h1>Browser trial {run}</h1><p>Dynamic reading: <strong id="reading">Loading...</strong></p>
<a href="/{run}/form">Continue to form</a>
<script>fetch('/{run}/dynamic').then(r=>r.json()).then(v=>document.querySelector('#reading').textContent=v.reading)</script></body></html>''')
        elif action == "dynamic":
            self.record({"kind": "dynamic", "run": run})
            self.reply(json.dumps({"reading": expected["reading"]}), "application/json")
        elif action == "form":
            self.reply(f'''<html><head><title>Browser form</title></head><body style="font:24px sans-serif;margin:48px">
<h1>Copy challenge into the input</h1><p id="challenge">{expected['challenge']}</p>
<form method="post" action="/{run}/submit"><label>Challenge <input name="answer" aria-label="Challenge" required></label>
<button type="submit">Submit challenge</button></form></body></html>''')
        elif action == "complete":
            self.record({"kind": "complete", "run": run})
            self.reply(f'''<html><head><title>Browser completion</title></head><body style="font:24px sans-serif;margin:48px">
<h1>Submission accepted</h1><p>Receipt: <strong>{expected['receipt']}</strong></p>
<a href="/{run}/download" download="trial-{run}.txt">Download trial file</a>
<p>Visual code (read the image):</p><img alt="Visual challenge" src="/{run}/pixels.png"></body></html>''')
        elif action == "pixels.png":
            self.reply((self.root / f"pixels-{run}.png").read_bytes(), "image/png")
        elif action == "download":
            payload = (self.root / f"trial-{run}.txt").read_bytes()
            self.record({"kind": "download", "run": run, "sha256": hashlib.sha256(payload).hexdigest()})
            self.reply(payload, "application/octet-stream", headers={"Content-Disposition": f'attachment; filename="trial-{run}.txt"'})
        else:
            self.reply("Missing page", code=404)

    def do_POST(self):
        parts = urlparse(self.path).path.strip("/").split("/")
        if len(parts) != 2 or parts[0] not in {"1", "2", "3"} or parts[1] != "submit":
            self.reply("Missing page", code=404)
            return
        run = parts[0]
        size = int(self.headers.get("Content-Length", "0"))
        if size > 4096:
            self.reply("Too large", code=413)
            return
        answer = parse_qs(self.rfile.read(size).decode()).get("answer", [""])[0]
        expected = json.loads((self.root / "expected.json").read_text())[run]
        matched = answer == expected["challenge"]
        self.record({"kind": "submission", "run": run, "answer": answer, "matched": matched})
        if matched:
            self.reply("", code=303, headers={"Location": f"/{run}/complete"})
        else:
            self.reply("Challenge mismatch", code=422)


if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", 18765), Fixture).serve_forever()
