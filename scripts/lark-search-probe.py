#!/usr/bin/env python3
"""Bounded, read-only Lark search contract probe; never opens TeamX state.

See docs/implementation/lark-search-capabilities.zh.md for the private input
contract. API payloads stay in a new, private output directory; stdout contains
counts only. This is acceptance tooling, not the S2 production reader.
"""

import argparse
import datetime as dt
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import tempfile
import time


MAX_OUTPUT = 4 << 20
NATIVE = re.compile(r"(?:om|omt|oc|ou)_[A-Za-z0-9_-]+\Z")


class ContractError(Exception):
    pass


def require(condition, reason):
    if not condition:
        raise ContractError(reason)


def native(value, prefix):
    return isinstance(value, str) and len(value) <= 256 and value.startswith(prefix) and bool(NATIVE.fullmatch(value))


def parse_page(data, token="", search=False, size=50):
    require(isinstance(data, dict), "missing data")
    require(isinstance(data.get("items"), list), "missing items")
    require(type(data.get("has_more")) is bool, "missing has_more")
    items, more, next_token = data["items"], data["has_more"], data.get("page_token", "")
    require(len(items) <= size, "oversize page")
    require(isinstance(next_token, str) and len(next_token) <= 8192 and not any(c in next_token for c in "\r\n\0"), "invalid token")
    require(not more or bool(next_token) and next_token != token, "nonadvancing token")
    # Search notice may explicitly indicate incomplete results. Never discard it.
    require(not search or not data.get("notice"), "search notice requires review")
    ids = []
    for item in items:
        require(isinstance(item, dict), "invalid item")
        mid = item.get("id") if search else item.get("message_id")
        require(native(mid, "om_"), "invalid message id")
        if search:
            meta = item.get("meta_data")
            require(isinstance(meta, dict) and meta.get("message_id") == mid and native(meta.get("chat_id"), "oc_"), "missing native locator")
        require(mid not in ids, "duplicate id within page")
        ids.append(mid)
    return items, more, next_token


def validate_message(m):
    require(isinstance(m, dict), "invalid message")
    require(native(m.get("message_id"), "om_") and native(m.get("chat_id"), "oc_"), "invalid message locator")
    require(isinstance(m.get("msg_type"), str) and bool(m["msg_type"]), "missing msg_type")
    created, updated = m.get("create_time"), m.get("update_time", "")
    require(isinstance(created, str) and created.isdigit() and int(created) > 0, "invalid millisecond creation time")
    require(updated == "" or isinstance(updated, str) and updated.isdigit() and int(updated) >= int(created), "invalid update time")
    for field, prefix in (("root_id", "om_"), ("parent_id", "om_"), ("thread_id", "omt_")):
        require(not m.get(field) or native(m[field], prefix), "invalid reply relation")
    body = m.get("body")
    require(isinstance(body, dict) and isinstance(body.get("content"), str) and len(body["content"].encode()) <= 1 << 20, "missing native body")
    require(isinstance(m.get("sender"), dict), "missing sender")
    require(isinstance(m.get("mentions", []), list), "invalid mentions")
    return m


def batch_result(data, requested):
    require(isinstance(data, dict) and isinstance(data.get("items"), list), "missing batch items")
    result = {}
    for m in data["items"]:
        validate_message(m)
        mid = m["message_id"]
        require(mid in requested and mid not in result, "unexpected or duplicate batch item")
        result[mid] = m
    # A successful envelope can still omit an item. Preserve the missing IDs.
    return result, sorted(set(requested) - result.keys())


def mentions(m, own):
    ats = m.get("mentions", [])
    me = any(a.get("id_type") == "open_id" and a.get("id") == own for a in ats)
    all_users = any(a.get("id") == "all" for a in ats)
    if m["msg_type"] not in ("text", "post"):
        return me, all_users
    try:
        body = json.loads(m["body"]["content"])
    except ValueError:
        return me, all_users

    def has_at(node, target):
        if isinstance(node, dict):
            return node.get("tag") == "at" and node.get("user_id") == target or any(has_at(v, target) for v in node.values())
        if isinstance(node, list):
            return any(has_at(v, target) for v in node)
        return False

    if m["msg_type"] == "text":
        text = body.get("text", "") if isinstance(body, dict) else ""
        all_users |= bool(re.search(r"(^|[^A-Za-z0-9_])@_all($|[^A-Za-z0-9_])", text)) or '<at user_id="all">' in text
    else:
        me |= has_at(body, own)
        all_users |= has_at(body, "all")
    return me, all_users


def error_kind(error):
    typ, sub = error.get("type"), error.get("subtype")
    if error.get("code") == 1020:
        return "cursor_expired"
    if error.get("code") == 99991400 or sub == "rate_limit" or typ == "rate_limit":
        return "rate_limited"
    if typ == "authentication" and sub == "refresh_server_error":
        return "network_error"
    if typ == "config":
        return "credential_rejected" if sub == "invalid_client" else "credential_unavailable"
    if typ in ("authorization", "permission"):
        return "permission_denied"
    if typ == "authentication":
        return "credential_rejected"
    if typ == "network":
        return "timeout" if sub == "timeout" else "network_error"
    if typ == "validation":
        return "invalid_request"
    return "query_failed"


class Probe:
    def __init__(self, config, output):
        self.cfg, self.output = config, output
        self.start = dt.datetime.fromisoformat(config["start"])
        self.end = dt.datetime.fromisoformat(config["end"])
        require(self.start.tzinfo and self.end.tzinfo and 0 < (self.end - self.start).total_seconds() <= 86400, "window must have timezone and span at most 24 hours")
        require(self.end <= dt.datetime.now(dt.timezone.utc), "window must end in the past")
        require(config.get("brand") in ("feishu", "lark") and config.get("profile") and native(config.get("open_id"), "ou_"), "explicit profile, brand and account required")
        require(re.fullmatch(r"cli_[A-Za-z0-9_-]+", config.get("app_id", "")), "explicit application required")
        self.samples = config.get("samples", [])
        require(0 < len(self.samples) <= 8, "supply 1-8 known chat samples")
        for s in self.samples:
            require(s.get("kind") in ("peer", "bot", "self", "group", "topic") and native(s.get("chat_id"), "oc_"), "invalid sample")
        require(len({s["chat_id"] for s in self.samples}) == len(self.samples), "duplicate sample")
        self.max_pages = config.get("max_pages", 8)
        require(type(self.max_pages) is int and 1 <= self.max_pages <= 20, "max_pages must be 1-20")
        self.max_calls = config.get("max_calls", 200)
        require(type(self.max_calls) is int and 1 <= self.max_calls <= 300, "max_calls must be 1-300")
        self.observe_chat = config.get("observe_chat_id")
        require(not self.observe_chat or any(s["chat_id"] == self.observe_chat and s["kind"] in ("group", "topic") for s in self.samples), "observation chat must be a supplied group sample")
        self.calls, self.last_call = [], 0
        self.deadline = time.monotonic() + 900
        self.summary = {"window": {"start": config["start"], "end": config["end"]}, "searches": {}, "samples": [], "batches": [], "errors": {}, "gaps": [], "complete": False}

    def save(self, name, value):
        path = self.output / (name + ".json")
        with path.open("x", encoding="utf-8") as f:
            json.dump(value, f, ensure_ascii=False, indent=2)
        path.chmod(0o600)

    def cli(self, args, label, diagnostic=False):
        require(len(self.calls) < self.max_calls and time.monotonic() < self.deadline, "probe budget exhausted")
        time.sleep(max(0, .25 - (time.monotonic() - self.last_call)))
        self.last_call = time.monotonic()
        env = {**os.environ, "LARKSUITE_CLI_NO_UPDATE_NOTIFIER": "1", "LARKSUITE_CLI_NO_SKILLS_NOTIFIER": "1"}
        argv = [self.cfg.get("executable", "lark-cli"), "--profile", self.cfg["profile"], *args]
        # Spool to files, enforce a bound while running, and kill the process group
        # on timeout/cancellation so no refresh subprocess survives the probe.
        with tempfile.TemporaryFile() as out, tempfile.TemporaryFile() as err:
            proc = subprocess.Popen(argv, stdout=out, stderr=err, env=env, start_new_session=True)
            try:
                while proc.poll() is None:
                    require(time.monotonic() - self.last_call < 35 and time.monotonic() < self.deadline, "CLI timeout")
                    require(os.fstat(out.fileno()).st_size + os.fstat(err.fileno()).st_size <= MAX_OUTPUT, "CLI output limit")
                    time.sleep(.05)
            finally:
                if proc.poll() is None:
                    os.killpg(proc.pid, signal.SIGKILL)
                    proc.wait()
            require(os.fstat(out.fileno()).st_size + os.fstat(err.fileno()).st_size <= MAX_OUTPUT, "CLI output limit")
            out.seek(0)
            err.seek(0)
            stdout, stderr = out.read(), err.read()
        elapsed = round(time.monotonic() - self.last_call, 3)
        record = {"label": label, "seconds": elapsed, "exit": proc.returncode}
        self.calls.append(record)
        try:
            obj = json.loads(stdout if proc.returncode == 0 else stderr)
        except ValueError:
            raise ContractError("CLI returned a non-JSON envelope") from None
        if diagnostic:
            require(proc.returncode == 0, "identity diagnostic failed")
            return obj
        require(obj.get("identity") == "user", "CLI identity mismatch")
        if proc.returncode != 0 or obj.get("ok") is not True:
            e = obj.get("error", {})
            record["error"] = {k: e[k] for k in ("type", "subtype", "code", "missing_scopes") if k in e}
            record["kind"] = error_kind(e)
            return None
        require(isinstance(obj.get("data"), dict), "missing response data")
        self.save(f"{len(self.calls):03d}-{label}", {"data": obj["data"]})
        return obj["data"]

    def identity(self, label):
        x = self.cli(["whoami"], label, diagnostic=True)
        actual = (x.get("profile"), x.get("brand"), x.get("appId"), x.get("onBehalfOf", {}).get("openId"), x.get("identity"))
        expected = (self.cfg["profile"], self.cfg["brand"], self.cfg["app_id"], self.cfg["open_id"], "user")
        require(actual == expected, "profile/application/account changed")

    def api(self, method, path, label, params=None, body=None):
        require(method == "GET" and bool(re.fullmatch(r"/open-apis/im/v1/(messages(?:/mget|/om_[A-Za-z0-9_-]+)?|chats/oc_[A-Za-z0-9_-]+(?:/members)?)", path)) or method == "POST" and path in ("/open-apis/im/v1/messages/search", "/open-apis/im/v1/chat_p2p/batch_query"), "operation outside read allowlist")
        args = ["api", method, path, "--as", "user", "--format", "json"]
        if params is not None:
            args += ["--params", json.dumps(params)]
        if body is not None:
            args += ["--data", json.dumps(body)]
        return self.cli(args, label)

    def pages(self, label, fetch, search=False, size=50):
        token, seen_tokens, result, pages = "", set(), {}, 0
        while pages < self.max_pages:
            data = fetch(token, pages)
            if data is None:
                return result, False, pages
            items, more, next_token = parse_page(data, token, search, size)
            for item in items:
                mid = item["id"] if search else item["message_id"]
                require(mid not in result, "duplicate id across frozen-window pages")
                result[mid] = item
            pages += 1
            if not more:
                return result, True, pages
            require(next_token not in seen_tokens, "cyclic pagination")
            seen_tokens.add(next_token)
            token = next_token
        return result, False, pages

    def search(self, label, filters, size=50):
        filters = {**filters, "time_range": {"start_time": self.cfg["start"], "end_time": self.cfg["end"]}}

        def fetch(token, page):
            params = {"page_size": size}
            if token:
                params["page_token"] = token
            return self.api("POST", "/open-apis/im/v1/messages/search", label + f"-{page}", params, {"query": "", "filter": filters})

        items, complete, pages = self.pages(label, fetch, True, size)
        for item in items.values():
            meta = item["meta_data"]
            require(type(meta.get("is_p2p_chat")) is bool and meta["is_p2p_chat"] == (filters["chat_type"] == "p2p"), "chat type filter mismatch")
            require(not filters.get("chat_ids") or meta["chat_id"] in filters["chat_ids"], "chat range filter mismatch")
            require(not filters.get("from_ids") or meta.get("from_id") in filters["from_ids"], "sender filter mismatch")
            created = dt.datetime.fromisoformat(meta["create_time"].replace("Z", "+00:00"))
            require(self.start <= created <= self.end, "search escaped frozen window")
        self.summary["searches"][label] = {"count": len(items), "pages": pages, "exhausted": complete}
        if not complete:
            self.summary["gaps"].append(label + " search incomplete")
        return items, complete

    def history(self, label, container, kind="chat"):
        def fetch(token, page):
            params = {"container_id_type": kind, "container_id": container, "page_size": 50, "sort_type": "ByCreateTimeAsc"}
            if kind == "chat":
                params.update(start_time=str(int(self.start.timestamp())), end_time=str(int(self.end.timestamp())))
            if token:
                params["page_token"] = token
            return self.api("GET", "/open-apis/im/v1/messages", label + f"-{page}", params)
        items, complete, _ = self.pages(label, fetch)
        # Thread containers ignore time filters; filter locally, only after
        # exhausting their bounded pages. An unexhausted thread remains partial.
        return {k: validate_message(v) for k, v in items.items() if self.start.timestamp() * 1000 <= int(v["create_time"]) < self.end.timestamp() * 1000}, complete

    def batch(self, label, ids):
        require(0 < len(ids) <= 51, "invalid probe batch")
        before = time.monotonic()
        data = self.api("GET", "/open-apis/im/v1/messages/mget", label, {"message_ids": ids})
        if data is None:
            self.summary["batches"].append({"label": label, "requested": len(ids), "error": self.calls[-1].get("kind")})
            return {}, ids
        result, missing = batch_result(data, ids)
        self.summary["batches"].append({"label": label, "requested": len(ids), "returned": len(result), "missing": len(missing), "seconds": round(time.monotonic() - before, 3)})
        return result, missing

    def relation(self, sample, label):
        chat = self.api("GET", "/open-apis/im/v1/chats/" + sample["chat_id"], label + "-chat", {"user_id_type": "open_id"})
        if chat is None:
            return "unverified", None
        mode = chat.get("chat_mode")
        require(mode == ("p2p" if sample["kind"] in ("peer", "bot", "self") else sample["kind"]), "sample category mismatch")
        # Explicit member evidence, also for public groups; metadata alone is
        # not treated as proof that the current user has joined the group.
        def fetch(token, page):
            params = {"member_id_type": "open_id", "page_size": 100}
            if token:
                params["page_token"] = token
            return self.api("GET", "/open-apis/im/v1/chats/" + sample["chat_id"] + "/members", label + f"-members-{page}", params)
        token, members, seen = "", [], set()
        complete = False
        for page in range(self.max_pages):
            data = fetch(token, page)
            if data is None:
                return "unverified", mode
            require(isinstance(data.get("items"), list) and type(data.get("has_more")) is bool, "missing membership evidence")
            members.extend(data["items"])
            if not data["has_more"]:
                complete = True
                break
            nxt = data.get("page_token")
            require(isinstance(nxt, str) and nxt and nxt not in seen, "invalid member pagination")
            seen.add(nxt)
            token = nxt
        own = self.cfg["open_id"]
        own_member = [m for m in members if m.get("member_id") == own and m.get("member_id_type") == "open_id"]
        if not own_member:
            return "unverified", mode
        if mode != "p2p":
            return "joined", mode
        if not complete or len(members) not in (1, 2) or any(m.get("member_id_type") != "open_id" or not native(m.get("member_id"), "ou_") or not m.get("tenant_key") for m in members) or len({m["member_id"] for m in members}) != len(members):
            return "unverified", mode
        peers = [m["member_id"] for m in members if m["member_id"] != own]
        peer = peers[0] if peers else own
        data = self.api("POST", "/open-apis/im/v1/chat_p2p/batch_query", label + "-p2p", {"chatter_id_type": "open_id"}, {"chatter_ids": [peer]})
        mapped = (data or {}).get("p2p_chats", [])
        if len(mapped) != 1 or mapped[0].get("chat_id") != sample["chat_id"]:
            return "unverified", mode
        return "peer" if peers else "self", mode

    def run(self, observe_only=False, seed_output=None):
        version = subprocess.run([self.cfg.get("executable", "lark-cli"), "--version"], capture_output=True, text=True, timeout=5)
        require(version.returncode == 0 and version.stdout.strip() == "lark-cli version 1.0.99", "requires lark-cli 1.0.99")
        self.identity("identity-before")
        auth = self.cli(["auth", "status", "--json", "--verify"], "auth-before", diagnostic=True)
        require(auth.get("verified") is True and auth.get("identity") == "user" and auth.get("appId") == self.cfg["app_id"] and auth.get("brand") == self.cfg["brand"] and auth.get("identities", {}).get("user", {}).get("openId") == self.cfg["open_id"], "user authorization not verified")
        try:
            if observe_only:
                require(self.observe_chat, "observation chat required")
                self.observe(seed_output)
            else:
                self.measure()
        finally:
            self.identity("identity-after")

    def measure(self):
        own = self.cfg["open_id"]
        pool = {}
        for label, f in (("all-private", {"chat_type": "p2p"}), ("all-group", {"chat_type": "group"}), ("group-mentions", {"chat_type": "group", "is_at_me": True}), ("group-self", {"chat_type": "group", "from_ids": [own]})):
            items, _ = self.search(label, f)
            pool.update(items)
        ids = list(pool)
        messages = {}
        # Details are fetched once per current batch, never an unbounded mget loop.
        for i in range(0, min(len(ids), 200), 50):
            batch, missing = self.batch(f"batch-{i // 50}", ids[i:i + 50])
            messages.update(batch)
            if missing:
                self.summary["gaps"].append("discovered details missing")
        if len(ids) > 200:
            self.summary["gaps"].append("discovery detail budget reached")
        # Compare one native single read with mget, preserving exact wire fields.
        if messages:
            mid = next(iter(messages))
            single = self.api("GET", "/open-apis/im/v1/messages/" + mid, "single-equivalence", {})
            got, missing = batch_result(single, [mid]) if single else ({}, [mid])
            fields = ("message_id", "chat_id", "root_id", "parent_id", "thread_id", "body", "mentions", "sender", "create_time", "update_time", "updated", "deleted")
            equal = not missing and all(got[mid].get(k) == messages[mid].get(k) for k in fields)
            self.summary["single_equivalence"] = equal
            require(equal, "mget and single message contract differ")
        for i, sample in enumerate(self.samples):
            self.compare_sample(sample, i, messages)
        self.review_samples()
        for kind in ("peer", "bot", "self", "group", "topic"):
            if not any(s["kind"] == kind for s in self.samples):
                self.summary["gaps"].append(kind + " sample not supplied")
        range_ids = self.cfg.get("range_chat_ids", [])
        if len(range_ids) == 32 and len(set(range_ids)) == 32 and all(native(cid, "oc_") for cid in range_ids):
            for suffix, extra in (("all", {}), ("self", {"from_ids": [own]}), ("mentions", {"is_at_me": True}), ("combined", {"from_ids": [own], "is_at_me": True})):
                self.search("range-32-" + suffix, {"chat_type": "group", "chat_ids": range_ids, **extra})
        else:
            self.summary["gaps"].append("32 distinct chat filter not supplied")
        # Intentional read-only negative probes; retain native error categories.
        self.api("POST", "/open-apis/im/v1/messages/search", "invalid-cursor", {"page_size": 2, "page_token": "teamx-invalid-cursor"}, {"query": "", "filter": {"chat_type": "group", "time_range": {"start_time": self.cfg["start"], "end_time": self.cfg["end"]}}})
        self.api("POST", "/open-apis/im/v1/messages/search", "oversize-page", {"page_size": 51}, {"query": "", "filter": {"chat_type": "group"}})
        invalid = "om_00000000000000000000000000000000"
        if ids:
            self.batch("partial-batch", [ids[0], invalid])
        if len(ids) >= 51:
            self.batch("oversize-batch", ids[:51])
        if self.observe_chat:
            self.observe()
        else:
            self.summary["gaps"].append("live index delay not observed")

    def compare_sample(self, sample, i, messages=None):
        own = self.cfg["open_id"]
        messages = messages or {}
        label, cid = f"sample-{i}", sample["chat_id"]
        relation, mode = self.relation(sample, label)
        f = {"chat_type": "p2p" if mode == "p2p" else "group", "chat_ids": [cid]}
        found, search_complete = self.search(label + "-all", f)
        history, history_complete = self.history(label + "-history", cid)
        threads = {m["thread_id"] for m in history.values() if m.get("thread_id")}
        threads.update(m["thread_id"] for m in messages.values() if m["chat_id"] == cid and m.get("thread_id"))
        if len(threads) > 8:
            history_complete = False
        for n, tid in enumerate(sorted(threads)[:8]):
            replies, complete = self.history(label + f"-thread-{n}", tid, "thread")
            require(all(m["chat_id"] == cid for m in replies.values()), "thread escaped chat")
            history.update(replies)
            history_complete &= complete
        relevant = {mid: m for mid, m in history.items() if m["msg_type"] != "system" and not m.get("deleted")}
        row = {"kind": sample["kind"], "mode": mode, "relation": relation, "history": len(relevant), "threads": len(threads), "replies": sum(bool(m.get("parent_id")) for m in relevant.values()), "search": len(found), "search_exhausted": search_complete, "history_exhausted": history_complete, "missing_from_search": len(set(relevant) - found.keys()), "search_only": len(set(found) - history.keys())}
        self.save(label + "-comparison", {"history_ids": sorted(relevant), "search_ids": sorted(found), "missing_from_search": sorted(set(relevant) - found.keys()), "search_only": sorted(set(found) - history.keys())})
        if mode in ("group", "topic"):
            at, at_complete = self.search(label + "-mentions", {**f, "is_at_me": True})
            own_messages, own_complete = self.search(label + "-self", {**f, "from_ids": [own]})
            me_ids = {mid for mid, m in relevant.items() if mentions(m, own)[0]}
            all_ids = {mid for mid, m in relevant.items() if mentions(m, own)[1]}
            self_ids = {mid for mid, m in relevant.items() if m["sender"].get("sender_type") == "user" and m["sender"].get("id_type") == "open_id" and m["sender"].get("id") == own}
            row.update(at_me=len(me_ids), at_all=len(all_ids), both=len(me_ids & all_ids), rich_at_all=sum(relevant[mid]["msg_type"] == "post" for mid in all_ids), mention_candidates=len(at), mention_exhausted=at_complete, mention_missing=len((me_ids | all_ids) - at.keys()), self_messages=len(self_ids), self_exhausted=own_complete, self_missing=len(self_ids - own_messages.keys()), mention_types=sorted({m["msg_type"] for mid, m in relevant.items() if mid in me_ids | all_ids}))
        self.summary["samples"].append(row)
        expected_relation = {"group": "joined", "topic": "joined", "peer": "peer", "self": "self"}.get(sample["kind"])
        if expected_relation and relation != expected_relation:
            self.summary["gaps"].append(label + " relationship unverified")
        if not history_complete or row["missing_from_search"] or row["search_only"]:
            self.summary["gaps"].append(label + " coverage requires review")
        if row.get("mention_missing") or row.get("self_missing"):
            self.summary["gaps"].append(label + " filtered coverage requires review")

    def review_samples(self):
        # Successful API execution (complete=true) is deliberately independent
        # of positive sample coverage. Empty topic/mention intersections cannot
        # satisfy the cutover plan, even when every queried page was exhausted.
        rows = self.summary["samples"]
        checks = {
            "peer_messages": any(r["kind"] == "peer" and r["relation"] == "peer" and r["history"] > 0 for r in rows),
            "ordinary_group_replies": any(r["mode"] == "group" and r["relation"] == "joined" and r["replies"] > 0 for r in rows),
            "topic_group_replies": any(r["mode"] == "topic" and r["relation"] == "joined" and r["replies"] > 0 for r in rows),
            "at_me": any(r.get("at_me", 0) > 0 for r in rows),
            "at_all": any(r.get("at_all", 0) > 0 for r in rows),
            "both_mentions": any(r.get("both", 0) > 0 for r in rows),
            "rich_at_all": any(r.get("rich_at_all", 0) > 0 for r in rows),
            "own_context": any(r.get("self_messages", 0) > 0 for r in rows),
        }
        self.summary["positive_samples"] = checks
        for name, present in checks.items():
            if not present:
                self.summary["gaps"].append("positive sample missing: " + name)

    def observe(self, seed_output=None):
        # Bounded read-only snapshots, 30 seconds apart. No synthetic messages.
        # Messages already searchable in the first snapshot are left censored;
        # their age cannot be presented as an index-delay measurement.
        started = dt.datetime.now(dt.timezone.utc)
        saved = self.cfg["start"], self.cfg["end"], self.start, self.end
        seen_history, seen_search, created, rounds, last_missing = {}, {}, {}, [], {}
        if seed_output:
            previous = json.loads((seed_output / "summary.json").read_text())
            seed = json.loads((seed_output / "index-observations.json").read_text())
            require(previous["window"] == self.summary["window"], "seed window differs from input")
            started = dt.datetime.fromisoformat(previous["index_observation"]["started"])
            require(started.tzinfo and 0 <= (dt.datetime.now(dt.timezone.utc) - started).total_seconds() <= 900, "observation seed must be recent")
            created, seen_history, seen_search = seed["created"], seed["first_history"], seed["first_search"]
            for values in (created, seen_history, seen_search):
                require(isinstance(values, dict) and len(values) <= 200 and all(native(mid, "om_") and type(t) in (int, float) and 0 < t <= dt.datetime.now(dt.timezone.utc).timestamp() for mid, t in values.items()), "invalid observation seed")
            rounds = previous["index_observation"]["rounds"][:]
            last_missing = seed.get("last_missing", {})
            if not last_missing and rounds:
                # The previous window end is a conservative lower bound for
                # the time of its completed search request.
                last_missing = {mid: dt.datetime.fromisoformat(rounds[-1]["at"]).timestamp() for mid in set(seen_history) - seen_search.keys()}
            self.summary["seed_output"] = str(seed_output)
        round_limit = self.cfg.get("observe_rounds", 3)
        require(type(round_limit) is int and 1 <= round_limit <= 12, "observe_rounds must be 1-12")
        try:
            for n in range(round_limit):
                if n:
                    time.sleep(30)
                self.end = dt.datetime.now(dt.timezone.utc).replace(microsecond=0)
                self.start = started.replace(microsecond=0) - dt.timedelta(minutes=30)
                self.cfg["start"], self.cfg["end"] = self.start.isoformat(), self.end.isoformat()
                history, h_complete = self.history(f"observe-{n}-history", self.observe_chat)
                now = dt.datetime.now(dt.timezone.utc).timestamp()
                for mid, m in history.items():
                    if m["msg_type"] == "system" or m.get("deleted"):
                        continue
                    seen_history.setdefault(mid, now)
                    require(mid not in created or created[mid] == int(m["create_time"]) / 1000, "seed message timestamp mismatch")
                    created[mid] = int(m["create_time"]) / 1000
                found, s_complete = self.search(f"observe-{n}-search", {"chat_type": "group", "chat_ids": [self.observe_chat]})
                now = dt.datetime.now(dt.timezone.utc).timestamp()
                for mid in found:
                    seen_search.setdefault(mid, now)
                for mid in set(history) - found.keys():
                    last_missing[mid] = self.end.timestamp()
                rounds.append({"at": self.end.isoformat(), "history": len(history), "search": len(found), "exhausted": h_complete and s_complete, "history_not_searchable": len(set(history) - found.keys())})
            fresh = {mid for mid, t in created.items() if t >= started.timestamp()}
            recovered = {mid for mid in seen_history if mid in seen_search and seen_search[mid] > seen_history[mid] + 5}
            # Keep exact timestamps/IDs in private evidence; summary has counts.
            self.save("index-observations", {"created": created, "first_history": seen_history, "first_search": seen_search, "last_missing": last_missing})
            bounds = [{"lower": round(max(0, last_missing.get(mid, created[mid]) - created[mid]), 3), "upper": round(seen_search[mid] - created[mid], 3)} for mid in sorted(fresh) if mid in seen_search]
            self.summary["index_observation"] = {"started": started.isoformat(), "rounds": rounds, "fresh_messages": len(fresh), "delayed_then_found": len(recovered), "fresh_search_delay_upper_seconds": [b["upper"] for b in bounds], "fresh_search_delay_bounds_seconds": bounds, "unresolved": len(set(seen_history) - seen_search.keys())}
            if not fresh:
                self.summary["gaps"].append("no fresh message during index observation; 5-minute overlap unvalidated")
            if not all(r["exhausted"] for r in rounds) or set(seen_history) - seen_search.keys():
                self.summary["gaps"].append("index observation incomplete or missing messages")
        finally:
            self.cfg["start"], self.cfg["end"], self.start, self.end = saved

    def finish(self):
        self.summary["errors"] = {c["label"]: {"kind": c.get("kind"), **c.get("error", {})} for c in self.calls if "error" in c}
        self.summary["calls"] = self.calls
        self.summary["cli_calls"] = len(self.calls)
        self.save("summary", self.summary)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--observe-only", action="store_true", help="only observe native history versus search; no matrix rerun")
    parser.add_argument("--seed-output", type=Path, help="continue a recent observation using the same explicit account/chat input")
    args = parser.parse_args()
    if args.seed_output and not args.observe_only:
        parser.error("seed-output requires observe-only")
    os.umask(0o077)
    # Refuse an existing output: every run has an immutable, reviewable ledger.
    try:
        args.output.mkdir(mode=0o700, parents=True, exist_ok=False)
    except FileExistsError:
        parser.error("output directory already exists; use a new directory")
    probe = None
    try:
        probe = Probe(json.loads(args.input.read_text()), args.output)
        probe.run(args.observe_only, args.seed_output)
        # Execution completion is distinct from capability/coverage acceptance.
        probe.summary["complete"] = True
    except KeyboardInterrupt:
        if probe:
            probe.summary["gaps"].append("cancelled")
        print("Probe cancelled; progress retained.")
        return 130
    except (ContractError, OSError, ValueError, KeyError, subprocess.TimeoutExpired) as e:
        if probe:
            probe.summary["gaps"].append(str(e) if isinstance(e, ContractError) else type(e).__name__)
        print("Probe incomplete:", str(e) if isinstance(e, ContractError) else type(e).__name__)
        return 1
    finally:
        if probe:
            probe.finish()
    print("Read-only probe completed; CLI calls:", len(probe.calls))
    print("Review summary.json and coverage gaps before accepting search ingestion.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
