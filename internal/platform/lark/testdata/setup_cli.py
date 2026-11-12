#!/usr/bin/env -S python3 -S
"""Synthetic pinned CLI for app-configuration and operator-session tests."""
import base64
import json
import os
import sys
import time

base = os.path.dirname(os.path.realpath(sys.argv[0]))
args = sys.argv[1:]
profile = ""
if args[:1] == ["--profile"]:
    profile, args = args[1], args[2:]
mode = open(base + "/mode").read().strip()
assert not any(k.startswith(("OPENCLAW_", "HERMES_")) for k in os.environ)
assert "LARKSUITE_CLI_PROFILE" not in os.environ
assert "LARKSUITE_CLI_WORKSPACE" not in os.environ
with open(base + "/calls", "a") as f:
    f.write(json.dumps(args) + "\n")


def load():
    with open(base + "/profiles.json") as f:
        return json.load(f)


def save(items):
    with open(base + "/profiles.json", "w") as f:
        json.dump(items, f)


def mark(name):
    with open(base + "/" + name, "w") as f:
        f.write("yes")


if args == ["--version"]:
    print("lark-cli version " + ("0.0.0" if mode == "version" else "1.0.99"))
elif args == ["profile", "list"]:
    count_file = base + "/list-count"
    count = int(open(count_file).read()) + 1 if os.path.exists(count_file) else 1
    with open(count_file, "w") as f:
        f.write(str(count))
    items = load()
    if mode == "pre-conflict" and count == 3:
        items.append({"name": "foreign", "appId": "cli_foreign", "brand": "feishu"})
        save(items)
    print(json.dumps(items))
elif args[:3] == ["config", "init", "--new"]:
    assert profile == ""
    name = args[args.index("--name") + 1]
    assert name.startswith("teamx-feishu-")
    assert args[args.index("--brand") + 1] == "feishu"
    assert args[-2:] == ["--lang", "zh"]
    mark("setup-started")
    link = "https://open.feishu.cn/page/cli?user_code=TEST-CODE&lpv=1.0.99&ocv=1.0.99&from=cli"
    if mode == "unsafe-url":
        link = link.replace("open.feishu.cn/", "open.feishu.cn.evil.invalid/")
    if mode == "overflow":
        print("X" * 70000, file=sys.stderr, flush=True)
        time.sleep(60)
    if mode != "no-link":
        print("private-device-code synthetic-secret-never-expose", file=sys.stderr)
        print("  " + link, file=sys.stderr, flush=True)
    if mode == "unsafe-url":
        time.sleep(60)
    if mode == "second-url":
        print(link.replace("TEST-CODE", "OTHER-CODE"), file=sys.stderr, flush=True)
        time.sleep(60)
    if mode == "expired":
        print(json.dumps({"error": {"type": "authentication", "subtype": "token_expired"}}), file=sys.stderr)
        sys.exit(1)
    items = load()
    new = {"name": name, "appId": "cli_selected", "brand": "lark" if mode == "brand" else "feishu", "active": not items}
    if mode == "saved-block":
        save(items + [new])
    if mode in ("block", "saved-block"):
        while not os.path.exists(base + "/app-release"):
            time.sleep(0.01)
    if mode == "overwrite":
        items[0]["appId"] = "cli_replaced"
    if mode == "foreign-change":
        items.append({"name": "foreign", "appId": "cli_foreign", "brand": "feishu"})
    save(items + [new])
    print(json.dumps({"appId": "cli_wrong" if mode == "receipt" else "cli_selected", "brand": new["brand"], "appSecret": "synthetic-secret-never-expose"}), flush=True)
    if mode == "exit-after-save":
        print(json.dumps({"error": {"type": "config", "subtype": "invalid_client", "message": "synthetic-secret-never-expose"}}), file=sys.stderr, flush=True)
        sys.exit(1)
elif args == ["config", "show"]:
    item = next((i for i in load() if i["name"] == profile), None)
    if item is None:
        sys.exit(1)
    print(json.dumps({"profile": profile, "appId": item["appId"], "brand": item["brand"], "appSecret": "synthetic-secret-never-expose"}))
elif args[:2] == ["auth", "qrcode"]:
    open("code.png", "wb").write(base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+j5X8AAAAASUVORK5CYII="))
    print("{}")
elif args[:3] == ["api", "GET", "/open-apis/bot/v3/info"]:
    assert args[-4:] == ["--as", "bot", "--format", "json"]
    print(json.dumps({"ok": True, "identity": "bot", "data": {"app_name": "Selected app"}}))
elif args == ["auth", "scopes", "--json"]:
    print(json.dumps({"appId":"cli_selected","tokenType":"user","userScopes":"im:chat:read im:message:readonly im:message.p2p_msg:get_as_user im:message.group_msg:get_as_user contact:user.basic_profile:readonly im:message.send_as_user im:message im:chat.members:read contact:user:search".split()}))
elif args[:2] == ["auth", "check"]:
    print(json.dumps({"ok":True,"granted":args[args.index("--scope")+1].split(),"missing":[]}))
elif args == ["auth", "status", "--json"]:
    print(json.dumps({"appId":"cli_selected","identities":{"user":{"openId":"ou_reader","tokenStatus":"valid","refreshExpiresAt":"2027-12-01T00:00:00Z"}}}))
elif args[:2] == ["auth", "login"] and "--no-wait" in args:
    assert profile.startswith("teamx-feishu-")
    scopes = args[args.index("--scope") + 1]
    assert "im:message.send_as_user" in scopes and "im:chat.members:read" in scopes and "contact:user:search" in scopes and "--domain" not in args
    open(base + "/requested-scopes", "w").write(scopes)
    mark("auth-started")
    if mode == "login-fail":
        print(json.dumps({"error": {"type": "network"}}), file=sys.stderr)
        sys.exit(1)
    print(json.dumps({"verification_url": "https://accounts.feishu.cn/oauth?user_code=USER-CODE", "device_code": "private-device-code", "expires_in": 600}))
elif args[:3] == ["auth", "login", "--device-code"]:
    assert args[3] == "private-device-code"
    mark("auth-polling")
    while not os.path.exists(base + "/auth-release"):
        time.sleep(0.01)
    print(json.dumps({"event": "authorization_complete", "user_open_id": "ou_reader", "missing": [], "refresh_token_issued": True, "expires_at": "2027-01-01T00:00:00Z"}))
elif args == ["whoami", "--as", "user"]:
    print(json.dumps({"profile": profile, "appId": "cli_selected", "brand": "feishu", "identity": "user", "available": True, "onBehalfOf": {"openId": "ou_reader"}}))
elif args[:3] == ["api", "GET", "/open-apis/authen/v1/user_info"]:
    assert args[-4:] == ["--as", "user", "--format", "json"]
    print(json.dumps({"ok": True, "identity": "user", "data": {"open_id": "ou_reader", "tenant_key": "tenant", "name": "Verified user"}}))
else:
    raise RuntimeError("unexpected fixture command")
