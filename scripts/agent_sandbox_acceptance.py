#!/usr/bin/env python3
"""Explicit real Docker + packaged DSH + model browser acceptance; no Room changes."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import secrets
import shutil
import signal
import socket
import subprocess
import sys
import struct
import threading
import time
import tomllib
import urllib.error
import urllib.request
from urllib.parse import urlparse
import zlib

ROOT = Path(__file__).resolve().parents[1]
IMAGE = "enterprise-public-cn-beijing.cr.volces.com/vefaas-public/all-in-one-sandbox@sha256:5ca2cd5619ee1e18c5479301e740c1e35307ce85d4142a145aec65d459655eee"
INGRESS_IMAGE = "nginx@sha256:901e593c95dc85f5a729f5765516321366ef4ac401821431d408897e95287d0a"


def pixel_code(code):
    """PNG contains only pixels, with no text metadata or DOM answer."""
    glyphs = ["01110100011000110001100011000101110", "00100011000010000100001000010001110",
              "01110100010000100010001000100011111", "11110000010000101110000010000111110",
              "00010001100101010010111110001000010", "11111100001000011110000010000111110",
              "01110100001000011110100011000101110", "11111000010001000100010000100001000",
              "01110100011000101110100011000101110", "01110100011000101111000010000101110"]
    width, height, scale = 600, 130, 10
    rows = [bytearray([255] * width * 3) for _ in range(height)]
    for i, digit in enumerate(code):
        for j, bit in enumerate(glyphs[int(digit)]):
            if bit == "1":
                for y in range(25 + (j // 5) * scale, 25 + (j // 5 + 1) * scale):
                    x = 45 + i * 6 * scale + (j % 5) * scale
                    rows[y][x * 3:(x + scale) * 3] = bytes(scale * 3)
    def chunk(name, data):
        return struct.pack(">I", len(data)) + name + data + struct.pack(">I", zlib.crc32(name + data))
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)) + chunk(b"IDAT", zlib.compress(b"".join(b"\0" + row for row in rows))) + chunk(b"IEND", b"")


def command(*args, timeout=45, env=None, input=None):
    return subprocess.run(args, cwd=ROOT, env=env, input=input, capture_output=True,
                          timeout=timeout, check=True).stdout


def save(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
    path.chmod(0o600)


def settle_container(cid, state_file):
    """Diagnostic failures must never prevent stop and independent inspection."""
    result = {"container_id": cid, "settled": False, "removed": False}
    try:
        command("docker", "stop", "-t", "10", cid, timeout=30)
    except (subprocess.SubprocessError, OSError):
        result["stop_command_failed"] = True
    try:
        state = json.loads(command("docker", "inspect", "--format", "{{json .State}}", cid))
        save(state_file, state)
        result.update(running=state["Running"], pid=state["Pid"], settled=not state["Running"] and state["Pid"] == 0)
        if result["settled"]:
            command("docker", "rm", cid)
            result["removed"] = not command("docker", "ps", "-aq", "--filter", "id=" + cid).strip()
    except (subprocess.SubprocessError, OSError, ValueError, KeyError) as error:
        result["error"] = type(error).__name__
    return result


def request(base, path, key=None, body=None, headers=None, timeout=15):
    hdr = {"Authorization": "Bearer " + key} if key else {}
    hdr.update(headers or {})
    data = None if body is None else json.dumps(body).encode()
    if data is not None:
        hdr["Content-Type"] = "application/json"
    req = urllib.request.Request(base + path, data=data, headers=hdr)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as response:
            return response.status, dict(response.headers), response.read()
    except urllib.error.HTTPError as error:
        return error.code, dict(error.headers), error.read()


def websocket_status(base, path, key=None):
    endpoint = urlparse(base)
    headers = [f"GET {path} HTTP/1.1", f"Host: {endpoint.netloc}", "Upgrade: websocket", "Connection: Upgrade",
               "Sec-WebSocket-Version: 13", "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ=="]
    if key:
        headers.append("Authorization: Bearer " + key)
    with socket.create_connection((endpoint.hostname, endpoint.port), timeout=5) as stream:
        stream.sendall(("\r\n".join(headers) + "\r\n\r\n").encode())
        return int(stream.recv(4096).split(b"\r\n", 1)[0].split()[1])


class MCP:
    def __init__(self, base, key):
        self.base, self.key, self.session, self.seq = base, key, None, 0
        self.call("initialize", {"protocolVersion": "2025-03-26", "capabilities": {},
                                 "clientInfo": {"name": "teamx-browser-acceptance", "version": "1"}})
        self.call("notifications/initialized", {}, notification=True)

    def call(self, method, params, notification=False):
        self.seq += 1
        body = {"jsonrpc": "2.0", "method": method, "params": params}
        if not notification:
            body["id"] = self.seq
        headers = {"Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2025-03-26"}
        if self.session:
            headers["Mcp-Session-Id"] = self.session
        status, hdr, data = request(self.base, "/mcp", self.key, body, headers, timeout=60)
        self.session = next((v for k, v in hdr.items() if k.lower() == "mcp-session-id"), self.session)
        if status not in (200, 202, 204):
            raise RuntimeError(f"MCP {method} HTTP {status}")
        if notification:
            return None
        content = data.decode()
        messages = [json.loads(line[5:]) for line in content.splitlines() if line.startswith("data:")]
        if not messages:
            messages = [json.loads(content)]
        message = next(m for m in messages if m.get("id") == self.seq)
        if "error" in message:
            raise RuntimeError(f"MCP {method} rejected: {message['error'].get('code')}")
        return message["result"]


def verify_trials(expected, receipts, dsh, downloaded):
    """Model prose is checked alongside independent server and filesystem evidence."""
    turns = {turn["name"]: turn for turn in dsh.get("turns", [])}
    results = []
    for run, item in expected.items():
        turn = turns.get("trial-" + run, {})
        text = turn.get("result", {}).get("text", "")
        events = [e for e in receipts if e.get("run") == run]
        checks = {
            "dynamic_value": item["reading"] in text and any(e["kind"] == "dynamic" for e in events),
            "submission": any(e["kind"] == "submission" and e.get("matched") and e.get("answer") == item["challenge"] for e in events),
            "redirect": item["receipt"] in text and any(e["kind"] == "complete" for e in events),
            "download_request": any(e["kind"] == "download" and e.get("sha256") == item["sha256"] for e in events),
            "download_file": any(f["name"] == f"trial-{run}.txt" and f["bytes"] == item["bytes"] and f["sha256"] == item["sha256"] for f in downloaded),
            "tools_observed": turn.get("tool_calls", 0) > 0,
            "turn_finished": not turn.get("error") and turn.get("result", {}).get("stopReason") == "end_turn",
        }
        results.append({"run": run, "passed": all(checks.values()), "checks": checks,
                        "seconds": turn.get("seconds"), "tool_calls": turn.get("tool_calls")})
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, required=True, help="existing TeamX TOML; read only")
    parser.add_argument("--model", required=True, help="explicit model catalog entry ID")
    parser.add_argument("--dsh-node", type=Path, required=True)
    parser.add_argument("--dsh-cli", type=Path, required=True, help="fixed installed DSH JS launcher")
    parser.add_argument("--output", type=Path, required=True, help="new private evidence directory")
    parser.add_argument("--image", default=IMAGE, help="digest-pinned AIO image (default linux/arm64)")
    parser.add_argument("--ingress-image", default=INGRESS_IMAGE, help="digest-pinned nginx ingress image")
    parser.add_argument("--seccomp-unconfined", action="store_true", help="explicit AIO Chromium namespace requirement")
    parser.add_argument("--task-interval", type=int, default=65, help="cooldown seconds between tasks; no failed call replay")
    args = parser.parse_args()
    if not 0 <= args.task_interval <= 120:
        parser.error("--task-interval must be between 0 and 120 seconds")
    if any(not re.fullmatch(r"[^\s]+@sha256:[0-9a-f]{64}", image) for image in (args.image, args.ingress_image)):
        parser.error("images must pin a digest")
    out = args.output.resolve()
    out.mkdir(mode=0o700)  # refuse evidence/session reuse
    started = time.monotonic()
    report = {"status": "failed", "recorded_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
              "host": platform.platform(), "host_arch": platform.machine(), "image": args.image,
              "visual": "not_verified", "s2_recommendation": "defer", "usage": "unavailable via ACP",
              "cleanup": {"settled": False}, "scope": "S1 local DSH; no production Room or configuration writes"}
    cid, ingress, network, monitor, name = None, None, None, None, None
    stop_monitor = threading.Event()
    interrupted = threading.Event()
    def interrupt(*_):
        interrupted.set()
        (out / "cancel").touch()
    signal.signal(signal.SIGTERM, interrupt)
    signal.signal(signal.SIGINT, interrupt)
    key = secrets.token_hex(32)
    credential = ""
    try:
        report["teamx_commit"] = command("git", "rev-parse", "HEAD").decode().strip()
        info = json.loads(command("docker", "info", "--format", "{{json .}}"))
        report["docker"] = {k: info[k] for k in ("Architecture", "NCPU", "MemTotal", "ServerVersion")}
        report["disk_free_bytes"] = shutil.disk_usage(out).free
        if report["disk_free_bytes"] < 8 * 1024**3:
            raise RuntimeError("at least 8 GiB free disk required")
        image = json.loads(command("docker", "image", "inspect", args.image))[0]
        command("docker", "image", "inspect", args.ingress_image)
        report["ingress_image"] = args.ingress_image
        report["image_identity"] = {k: image[k] for k in ("Id", "Architecture", "Os", "Size", "RepoDigests")}
        host_arch = {"aarch64": "arm64", "x86_64": "amd64"}.get(info["Architecture"], info["Architecture"])
        if image["Architecture"] != host_arch:
            raise RuntimeError("native image required; emulation needs a separate measured configuration")
        config = tomllib.loads(args.config.read_text())
        model = next(m for m in config["models"]["entries"] if m["id"] == args.model)
        ref = model["credential_ref"]
        if not re.fullmatch(r"cred-[0-9a-f]+", ref):
            raise RuntimeError("invalid managed credential reference")
        credential = (args.config.resolve().parent / "model-credentials" / ref).read_text().strip()
        model = {k: v for k, v in model.items() if k in {"protocol", "base_url", "model_id", "context_window", "max_tokens", "reasoning_effort", "input_modalities", "compat"}}
        model["api_key_env"] = "TEAMX_SANDBOX_MODEL_KEY"
        report["model"] = model
        name = "teamx-browser-" + secrets.token_hex(6)
        network = command("docker", "network", "create", "--internal", "--label", "teamx.acceptance=browser", name).decode().strip()
        create = ["docker", "create", "--name", name, "--label", "teamx.acceptance=browser", "--network", network,
                  "--cpus", "2", "--memory", "3g", "--memory-swap", "3g", "--pids-limit", "512",
                  "--shm-size", "512m", "-e", "SANDBOX_API_KEY"]
        # Docker Desktop overlayfs does not support per-container storage quota.
        # Bound run time and require free disk; record this deployment limitation.
        report["disk_quota"] = "not supported by Docker Desktop overlayfs; dedicated disposable container, bounded runtime, free disk preflight"
        if args.seccomp_unconfined:
            create += ["--security-opt", "seccomp=unconfined"]
        report["seccomp"] = "unconfined" if args.seccomp_unconfined else "default"
        cid = command(*(create + [args.image]), env={**os.environ, "SANDBOX_API_KEY": key}).decode().strip()
        report["container_id"] = cid
        command("docker", "start", cid)
        inspect = json.loads(command("docker", "inspect", cid))[0]
        address = next(n["IPAddress"] for n in inspect["NetworkSettings"]["Networks"].values() if n["NetworkID"] == network)
        # Internal networks do not publish ports. A dedicated HTTP ingress has
        # an external interface; the browser container has no default route.
        proxy = out / "ingress.conf"
        proxy.write_text("events {}\nhttp { server { listen 8080; client_max_body_size 16m; location / { "
                         f"proxy_pass http://{address}:8080; "
                         "proxy_http_version 1.1; proxy_set_header Host $http_host; "
                         "proxy_set_header Upgrade $http_upgrade; proxy_set_header Connection upgrade; "
                         "proxy_buffering off; proxy_read_timeout 180s; } } }\n")
        ingress = command("docker", "create", "--name", name + "-ingress", "--label", "teamx.acceptance=browser",
                          "--network", "bridge", "--cpus", "0.25", "--memory", "64m", "--pids-limit", "64",
                          "-p", "127.0.0.1::8080", "--mount", f"type=bind,src={proxy},dst=/etc/nginx/nginx.conf,readonly",
                          args.ingress_image).decode().strip()
        command("docker", "network", "connect", network, ingress)
        command("docker", "start", ingress)
        proxy_inspect = json.loads(command("docker", "inspect", ingress))[0]
        port = proxy_inspect["NetworkSettings"]["Ports"]["8080/tcp"][0]["HostPort"]
        report["network"] = {"aio_internal": True, "host_bind": "127.0.0.1", "ingress_container_id": ingress,
                             "mounts": "ingress config only; AIO has no host mounts", "public_sample_expected": "egress denied"}
        base = "http://127.0.0.1:" + port
        report["endpoint"] = base + "/mcp"
        ready_deadline = time.monotonic() + 120
        while time.monotonic() < ready_deadline:
            if interrupted.is_set():
                raise InterruptedError("cancelled during AIO startup")
            try:
                if request(base, "/v1/browser/info", key, timeout=2)[0] == 200:
                    break
            except (OSError, TimeoutError):
                pass
            time.sleep(1)
        else:
            raise RuntimeError("AIO browser not ready within 120 seconds; see container logs")
        report["cold_start_seconds"] = time.monotonic() - started
        auth = {}
        for path in ("/v1/sandbox", "/mcp", "/cdp/json/version", "/vnc/index.html", "/jupyter/"):
            status = request(base, path, timeout=3)[0]
            auth[path] = status
            if status not in (401, 403):
                raise RuntimeError("unauthenticated endpoint not protected: " + path)
        report["unauthenticated_status"] = auth
        status, _, data = request(base, "/v1/browser/info", key)
        report["browser_info"] = json.loads(data)
        cdp_path = urlparse(report["browser_info"]["data"]["cdp_url"]).path
        report["websocket_auth"] = {}
        for path in (cdp_path, "/websockify"):
            denied, allowed = websocket_status(base, path), websocket_status(base, path, key)
            report["websocket_auth"][path] = {"unauthenticated": denied, "authenticated": allowed}
            if denied not in (401, 403) or allowed != 101:
                raise RuntimeError("WebSocket authentication handshake failed: " + path)
        hub_tools = MCP(base, key).call("tools/list", {})
        save(out / "hub-tools.json", hub_tools)
        # The hub also publishes built-in shell/code/file tools, independent of
        # --filter-mcp-servers. Route this disposable authenticated MCP ingress
        # directly to the shipped browser server. No schema/result rewriting.
        route_browser = "from pathlib import Path; p=Path('/opt/gem/nginx/mcp_hub.conf'); s=p.read_text(); assert 'http://127.0.0.1:8091' in s; p.write_text(s.replace('http://127.0.0.1:8091','http://127.0.0.1:8100'))"
        command("docker", "exec", cid, "python3", "-c", route_browser)
        command("docker", "exec", cid, "nginx", "-s", "reload")
        report["mcp_scope"] = "AIO authenticated nginx /mcp -> shipped browser server :8100/mcp; upstream schema/results unchanged"
        for _ in range(20):
            try:
                mcp = MCP(base, key)
                tools = mcp.call("tools/list", {})
                if tools.get("tools") and all("browser" in tool["name"].lower() for tool in tools["tools"]):
                    break
                time.sleep(1)  # nginx's old worker may still serve during reload
            except (OSError, RuntimeError, ValueError):
                time.sleep(1)
        else:
            raise RuntimeError("MCP browser discovery unavailable after restart")
        if request(base, "/mcp")[0] not in (401, 403) or request(base, "/mcp", "wrong-key")[0] not in (401, 403):
            raise RuntimeError("scoped MCP authentication failed")
        save(out / "tools.json", tools)
        names = [tool["name"] for tool in tools["tools"]]
        report["tools"] = {"names": names, "count": len(names), "schema_bytes": len(json.dumps(tools).encode()),
                           "schema_sha256": hashlib.sha256(json.dumps(tools, sort_keys=True).encode()).hexdigest()}
        if not names or any("browser" not in name.lower() for name in names):
            raise RuntimeError("MCP must expose browser tools only; inspect tools.json and configure the image")
        expected, fixture = {}, out / "fixture"
        fixture.mkdir(mode=0o700)
        shutil.copy(ROOT / "scripts/agent_sandbox_fixture.py", fixture / "server.py")
        for run in ("1", "2", "3"):
            payload = ("TeamX browser download\n" + secrets.token_hex(48) + "\n").encode()
            (fixture / f"trial-{run}.txt").write_bytes(payload)
            expected[run] = {"reading": secrets.token_hex(10), "challenge": secrets.token_hex(8),
                             "receipt": secrets.token_hex(10), "sha256": hashlib.sha256(payload).hexdigest(), "bytes": len(payload),
                             "visual_code": str(secrets.randbelow(90000000) + 10000000)}
            (fixture / f"pixels-{run}.png").write_bytes(pixel_code(expected[run]["visual_code"]))
        save(fixture / "expected.json", expected)
        (fixture / "receipts.jsonl").touch()
        command("docker", "cp", str(fixture) + "/.", cid + ":/tmp/teamx-browser-fixture")
        command("docker", "exec", "-d", cid, "python3", "/tmp/teamx-browser-fixture/server.py")
        prompts = []
        for run in ("1", "2", "3"):
            visual = " Read the eight digit visual code from the screenshot image and include it in your reply; do not decode image bytes using code, canvas, OCR or scripts." if "image" in model.get("input_modalities", []) else " Do not claim to read the image; this model is text only."
            prompts.append({"Name": "trial-" + run, "Text": f"Using only sandbox browser MCP tools, open http://127.0.0.1:18765/{run}/start . Wait for the dynamic reading and remember it. Click Continue to form. Read the displayed challenge, type it into the Challenge input and click Submit challenge. Read the final receipt and click Download trial file; save it in the browser's remote downloads directory. Take a screenshot of the completion page. Reply with the exact dynamic reading, final receipt, and remote download path.{visual} Do not fetch the download using scripts or HTTP clients; use the browser download interaction. Never read container paths with host tools."})
        prompts += [{"Name": "failure", "Text": "Open http://127.0.0.1:18765/unavailable using the browser. Report the observed service unavailable error explicitly; do not claim success or retry indefinitely."},
                    {"Name": "public", "Text": "Try to open https://example.com with browser navigation and report the page title or the actual network error. This environment has restricted egress; do not bypass it or retry. This is a separate public sample."}]
        run_cfg = {"output": str(out), "endpoint": base + "/mcp", "model": model, "prompts": prompts,
                   "interval_seconds": args.task_interval}
        report["task_interval_seconds"] = args.task_interval
        save(out / "run.json", run_cfg)
        samples = []
        def sample():
            while not stop_monitor.is_set():
                try:
                    samples.append(json.loads(command("docker", "stats", "--no-stream", "--format", "{{json .}}", cid, timeout=8)))
                except (subprocess.SubprocessError, ValueError):
                    pass
                stop_monitor.wait(3)
        monitor = threading.Thread(target=sample, daemon=True)
        monitor.start()
        env = {**os.environ, "TEAMX_SANDBOX_REAL": "1", "TEAMX_SANDBOX_RUN_CONFIG": str(out / "run.json"),
               "TEAMX_SANDBOX_MODEL_KEY": credential, "TEAMX_SANDBOX_KEY": key,
               "TEAMX_DSH_BINARY_PATH": str(args.dsh_node.resolve()), "TEAMX_DSH_BINARY_ARGS": json.dumps([str(args.dsh_cli.resolve())])}
        if interrupted.is_set():
            raise InterruptedError("cancelled before model execution")
        with (out / "go-test.log").open("wb") as stream:
            test = subprocess.run(["go", "test", "-tags=e2e", "./internal/agent/dsh", "-run", "^TestRealDSHSandboxBrowser$", "-count=1", "-v", "-timeout=28m"], cwd=ROOT, env=env, stdout=stream, stderr=subprocess.STDOUT, timeout=29*60)
        report["go_test_exit"] = test.returncode
        stop_monitor.set(); monitor.join(timeout=10)
        save(out / "resource-samples.json", samples)
        report["resource_samples"] = len(samples)
        command("docker", "cp", cid + ":/tmp/teamx-browser-fixture/receipts.jsonl", str(out / "receipts.jsonl"))
        receipts = [json.loads(line) for line in (out / "receipts.jsonl").read_text().splitlines()]
        # Search only the member home and browser temporary downloads, excluding
        # verifier inputs under /tmp/teamx-browser-fixture.
        probe = "import pathlib,json,hashlib; rows=[]\nfor root in ['/home/gem','/tmp/playwright-artifacts','/tmp/agent-browser']:\n for p in pathlib.Path(root).rglob('trial-*.txt'):\n  if p.is_file() and not p.is_symlink():\n   b=p.read_bytes(); rows.append(dict(path=str(p),name=p.name,bytes=len(b),sha256=hashlib.sha256(b).hexdigest()))\nprint(json.dumps(rows))"
        downloaded = json.loads(command("docker", "exec", cid, "python3", "-c", probe))
        save(out / "downloads.json", downloaded)
        for item in downloaded:
            command("docker", "cp", cid + ":" + item["path"], str(out / item["name"]))
        dsh = json.loads((out / "dsh-report.json").read_text())
        report["trials"] = verify_trials(expected, receipts, dsh, downloaded)
        report["screenshots"] = [{"file": f"trial-{run}.png", "bytes": (out / f"trial-{run}.png").stat().st_size} for run in expected if (out / f"trial-{run}.png").is_file()]
        report["screenshot_generated"] = len(report["screenshots"]) == 3
        visual_checks = []
        for run, item in expected.items():
            turn = next((t for t in dsh.get("turns", []) if t["name"] == "trial-" + run), {})
            result = turn.get("result", {})
            visual_checks.append(item["visual_code"] in result.get("text", "") and any("screenshot" in t.get("title", "").lower() for t in result.get("tools", [])) and all(item["visual_code"] not in t.get("output", "") for t in result.get("tools", [])))
        report["visual"] = {"status": "passed_behavioral" if all(visual_checks) else "not_verified", "matches": visual_checks,
                            "basis": "pixel-only random code absent from prompt/DOM/tool text; screenshot tool observed; no provider wire capture"}
        failure = next((turn for turn in dsh.get("turns", []) if turn["name"] == "failure"), {})
        report["failure_reported"] = any(e["kind"] == "unavailable" for e in receipts) and bool(re.search(r"unavailable|503|error|fail", failure.get("result", {}).get("text", ""), re.I))
        report["public_sample"] = next((turn for turn in dsh.get("turns", []) if turn["name"] == "public"), {"status": "not_run"})
        report["local_cleanup_settled"] = dsh.get("local_cleanup_settled", False)
        report["controlled_passed"] = len(report["trials"]) == 3 and all(r["passed"] for r in report["trials"])
        report["status"] = "passed" if test.returncode == 0 and report["controlled_passed"] and report["screenshot_generated"] and report["failure_reported"] and report["local_cleanup_settled"] else "failed"
    except Exception as error:
        # Do not include response bodies, subprocess environment or credentials.
        text = f"{type(error).__name__}: {str(error)[:300]}".replace(key, "[REDACTED]")
        report["error"] = text.replace(credential, "[REDACTED]") if credential else text
    finally:
        stop_monitor.set()
        if monitor:
            monitor.join(timeout=10)
        # Resolve an ambiguous create timeout using the unique, preselected name
        # and our ownership label. Never stop unrelated containers.
        if name:
            for suffix, identity in (("", cid), ("-ingress", ingress)):
                if not identity:
                    try:
                        found = command("docker", "ps", "-aq", "--filter", "name=^/" + name + suffix + "$", "--filter", "label=teamx.acceptance=browser").decode().strip()
                        if suffix:
                            ingress = found or None
                        else:
                            cid = found or None
                    except Exception:
                        report["cleanup"]["ownership_lookup_failed"] = True
        if cid:
            try:
                logs = command("docker", "logs", cid).decode(errors="replace").replace(key, "[REDACTED]")
                if credential:
                    logs = logs.replace(credential, "[REDACTED]")
                (out / "container.log").write_text(logs)
            except Exception:
                report["container_logs_unavailable"] = True
            report["cleanup"].update(settle_container(cid, out / "stopped-state.json"))
        if ingress:
            stopped = settle_container(ingress, out / "ingress-stopped-state.json")
            report["cleanup"]["ingress_stopped"] = stopped["settled"]
            report["cleanup"]["ingress_removed"] = stopped["removed"]
            report["cleanup"]["settled"] = report["cleanup"]["settled"] and stopped["settled"]
        if network:
            try:
                command("docker", "network", "rm", network)
                report["cleanup"]["network_removed"] = True
            except Exception:
                report["cleanup"]["network_removed"] = False
        if not report["cleanup"]["settled"] or not report["cleanup"].get("removed") or not report["cleanup"].get("ingress_removed") or not report["cleanup"].get("network_removed") or report["cleanup"].get("ownership_lookup_failed"):
            report["status"] = "failed"
        report["seconds"] = time.monotonic() - started
        report["s2_recommendation"] = "continue" if report["status"] == "passed" else "defer"
        save(out / "report.json", report)
        print(json.dumps({"status": report["status"], "report": str(out / "report.json"), "s2_recommendation": report["s2_recommendation"]}))
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
