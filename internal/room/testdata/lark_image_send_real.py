#!/usr/bin/env -S python3 -I
"""Transparent, bounded native CLI for the opt-in real image-send acceptance.

The generated executable substitutes CONFIG_PATH; responses reach TeamX unchanged.
An exclusive record is fsynced BEFORE every native upload or message dispatch.
Keep that directory across failed runs: an uncertain write is never retried.
"""
import glob
import hashlib
import json
import os
import subprocess
import sys

CONFIG_PATH = "__CONFIG_PATH__"


def load(path):
    with open(path, encoding="utf-8") as stream:
        return json.load(stream)


def save_once(path, value):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as stream:
        json.dump(value, stream, ensure_ascii=False)
        stream.flush()
        os.fsync(stream.fileno())
    fd = os.open(os.path.dirname(path), os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def active_case(cfg):
    records = [load(p)["action"] for p in glob.glob(cfg["ledger"] + "/records/*.json")]
    active = [r for r in records if r["status"] == "unknown" and r["attempts"] == 1]
    assert len(active) == 1, "one durable attempted action required before upload"
    action = active[0]
    case = next(c for c in cfg["cases"] if c["key"] == action["idempotency_key"])
    assert action["request"]["id"] == case["id"]
    assert action["request"]["operation"] == cfg["operation"]
    assert action["request"]["input"] == case["input"]
    assert action["access"]["account"]["id"]["value"] == cfg["user_id"]
    assert len(action["inspection"]["content_digest"]) == 64
    return case


def content(case, keys):
    body = case["input"].get("body", "")
    if len(keys) == 1 and not body:
        return "image", {"image_key": keys[0]}
    rows = [[{"tag": "text", "text": line}] for line in body.split("\n")] if body else []
    rows += [[{"tag": "img", "image_key": key}] for key in keys]
    return "post", {"zh_cn": {"title": "", "content": rows}}


def authorize(cfg, args):
    assert args[:2] == ["--profile", cfg["profile"]], "fixed profile required"
    args = args[2:]
    record = None
    kind = None
    if args[:3] == ["im", "images", "create"]:
        assert len(args) == 11 and args[3:5] == ["--data", '{"image_type":"message"}']
        assert args[5] == "--file" and args[7:] == ["--as", "user", "--format", "json"]
        assert os.environ.get("LARKSUITE_CLI_CONFIG_DIR"), "pinned account required"
        case = active_case(cfg)
        rel = args[6]
        names = ["image=./image-%d.png" % (i + 1) for i in range(len(case["hashes"]))]
        assert rel in names, "only the selected original PNG bytes may be uploaded"
        index = names.index(rel)
        with open(rel[len("image=./"):], "rb") as stream:
            digest = hashlib.sha256(stream.read()).hexdigest()
        assert digest == case["hashes"][index], "source bytes changed"
        record = os.path.join(cfg["dispatch_dir"], case["key"] + ".upload-%d" % (index + 1))
        kind = "upload"
        save_once(record, {"status": "unknown", "sha256": digest, "index": index + 1})
    elif args[:3] == ["api", "POST", "/open-apis/im/v1/messages"]:
        assert len(args) == 11 and args[3] == "--params" and args[5] == "--data"
        assert args[7:] == ["--as", "user", "--format", "json"]
        assert os.environ.get("LARKSUITE_CLI_CONFIG_DIR"), "pinned account required"
        case = active_case(cfg)
        keys = [load(os.path.join(cfg["dispatch_dir"], case["key"] + ".upload-%d.response" % (i + 1)))["image_key"] for i in range(len(case["hashes"]))]
        assert all(isinstance(key, str) and key.startswith("img_") for key in keys)
        msg_type, expected = content(case, keys)
        payload = json.loads(args[6])
        assert json.loads(args[4]) == {"receive_id_type": cfg["receive_id_type"]}
        assert set(payload) == {"receive_id", "msg_type", "content", "uuid"}
        assert payload["receive_id"] == cfg["receive_id"] and payload["uuid"] == case["key"]
        assert payload["msg_type"] == msg_type and json.loads(payload["content"]) == expected
        record = os.path.join(cfg["dispatch_dir"], case["key"] + ".message")
        kind = "message"
        save_once(record, {"status": "unknown", "payload": payload})
    elif args[:3] == ["api", "POST", "/open-apis/im/v1/chat_p2p/batch_query"]:
        # Native relationship verification is a read despite its HTTP verb.
        assert len(args) == 11 and args[3] == "--params" and args[5] == "--data"
        assert json.loads(args[4]) == {"chatter_id_type": "open_id"}
        assert json.loads(args[6]) == {"chatter_ids": [cfg["user_id"]]}
        assert args[7:] == ["--as", "user", "--format", "json"]
    elif args[:2] == ["api", "GET"]:
        allowed = {"/open-apis/authen/v1/user_info"}
        for chat in [cfg["self_chat_id"], cfg["group_id"]]:
            allowed.update({"/open-apis/im/v1/chats/" + chat, "/open-apis/im/v1/chats/" + chat + "/members"})
        for path in glob.glob(cfg["dispatch_dir"] + "/*.message.response"):
            mid = load(path).get("message_id")
            if mid:
                allowed.add("/open-apis/im/v1/messages/" + mid)
        assert args[2] in allowed, "read outside acceptance targets"
        assert args[-4:] == ["--as", "user", "--format", "json"]
    elif args[:2] == ["im", "+chat-search"]:
        assert args == ["im", "+chat-search", "--query", cfg["group_name"], "--disable-search-by-user", "--search-types", "private,public_joined", "--chat-modes", "group", "--page-size", "20", "--as", "user", "--format", "json"]
    else:
        assert args in [["--version"], ["config", "show"], ["whoami", "--as", "user"], ["auth", "scopes", "--json"], ["auth", "status", "--json"]] or (args[:2] == ["auth", "check"] and len(args) == 5 and args[2] == "--scope" and args[4] == "--json"), "operation outside acceptance"
    return kind, record


def main():
    cfg = load(CONFIG_PATH)
    args = sys.argv[1:]
    kind, record = authorize(cfg, args)
    result = subprocess.run([cfg["binary"]] + args, capture_output=True)
    if record:
        summary = {"exit_code": result.returncode}
        try:
            envelope = json.loads(result.stdout if result.returncode == 0 else result.stderr)
            summary.update({"ok": envelope.get("ok"), "identity": envelope.get("identity")})
            field = "image_key" if kind == "upload" else "message_id"
            summary[field] = envelope.get("data", {}).get(field)
        except (ValueError, AttributeError):
            summary["response"] = "unparseable"
        save_once(record + ".response", summary)
    sys.stdout.buffer.write(result.stdout)
    sys.stderr.buffer.write(result.stderr)
    sys.exit(result.returncode)


if __name__ == "__main__":
    main()
