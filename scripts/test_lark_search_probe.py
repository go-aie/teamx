"""Offline checks for acceptance-tool failures that could falsely claim coverage."""

import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("probe", Path(__file__).with_name("lark-search-probe.py"))
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)


class SearchProbeTests(unittest.TestCase):
    def test_empty_page_with_continuation_is_not_exhaustion(self):
        items, more, token = probe.parse_page({"items": [], "has_more": True, "page_token": "next"}, search=True)
        self.assertEqual(items, [])
        self.assertTrue(more)
        self.assertEqual(token, "next")

    def test_incomplete_or_anomalous_pages_are_rejected(self):
        for data in ({"items": []}, {"has_more": False}, {"items": [], "has_more": True}, {"items": [], "has_more": True, "page_token": "same"}, {"items": [], "has_more": False, "notice": "incomplete"}):
            with self.subTest(data=data), self.assertRaises(probe.ContractError):
                probe.parse_page(data, "same", search=True)

    def test_summary_is_not_a_native_message(self):
        with self.assertRaises(probe.ContractError):
            probe.validate_message({"message_id": "om_sample", "chat_id": "oc_sample", "display_info": "summary"})

    def test_successful_batch_preserves_partial_failure(self):
        message = {"message_id": "om_sample", "chat_id": "oc_sample", "msg_type": "text", "create_time": "1000000", "body": {"content": '{"text":"hi"}'}, "sender": {}}
        items, missing = probe.batch_result({"items": [message]}, ["om_sample", "om_missing"])
        self.assertEqual(list(items), ["om_sample"])
        self.assertEqual(missing, ["om_missing"])
        with self.assertRaises(probe.ContractError):
            probe.batch_result({"items": [message, message]}, ["om_sample"])

    def test_native_mentions_do_not_match_display_text(self):
        message = {"msg_type": "text", "body": {"content": '{"text":"@所有人"}'}}
        self.assertEqual(probe.mentions(message, "ou_me"), (False, False))
        message["body"]["content"] = '{"text":"@_all"}'
        self.assertEqual(probe.mentions(message, "ou_me"), (False, True))
        message["mentions"] = [{"id_type": "open_id", "id": "ou_me"}]
        self.assertEqual(probe.mentions(message, "ou_me"), (True, True))
        message = {"msg_type": "post", "body": {"content": '{"zh_cn":{"content":[[{"tag":"at","user_id":"all"}]]}}'}}
        self.assertEqual(probe.mentions(message, "ou_me"), (False, True))
        message["body"]["content"] = '{"content":[[{"tag":"at","user_id":"all"},{"tag":"at","user_id":"ou_me"}]]}'
        self.assertEqual(probe.mentions(message, "ou_me"), (True, True))

    def test_empty_topic_and_ordinary_threads_do_not_satisfy_topic_coverage(self):
        p = object.__new__(probe.Probe)
        p.summary = {"samples": [
            {"kind": "group", "mode": "group", "relation": "joined", "history": 10, "replies": 8},
            {"kind": "topic", "mode": "topic", "relation": "joined", "history": 0, "replies": 0},
        ], "gaps": []}
        p.review_samples()
        self.assertTrue(p.summary["positive_samples"]["ordinary_group_replies"])
        self.assertFalse(p.summary["positive_samples"]["topic_group_replies"])
        self.assertIn("positive sample missing: topic_group_replies", p.summary["gaps"])
        self.assertIn("positive sample missing: both_mentions", p.summary["gaps"])
        self.assertIn("positive sample missing: rich_at_all", p.summary["gaps"])

    def test_positive_samples_are_recorded_separately_from_execution(self):
        p = object.__new__(probe.Probe)
        p.summary = {"samples": [
            {"kind": "peer", "mode": "p2p", "relation": "peer", "history": 1, "replies": 0},
            {"kind": "group", "mode": "group", "relation": "joined", "history": 3, "replies": 1, "at_me": 1, "at_all": 2, "both": 1, "rich_at_all": 1, "self_messages": 1},
            {"kind": "topic", "mode": "topic", "relation": "joined", "history": 2, "replies": 1},
        ], "gaps": [], "complete": False}
        p.review_samples()
        self.assertTrue(all(p.summary["positive_samples"].values()))
        self.assertEqual(p.summary["gaps"], [])
        self.assertFalse(p.summary["complete"])

    def test_allowlist_prevents_remote_writes(self):
        p = object.__new__(probe.Probe)
        with self.assertRaises(probe.ContractError):
            p.api("POST", "/open-apis/im/v1/messages", "send")
        with self.assertRaises(probe.ContractError):
            p.api("DELETE", "/open-apis/im/v1/messages/om_sample", "recall")

    def test_native_cursor_and_rate_limit_codes_survive_generic_cli_type(self):
        self.assertEqual(probe.error_kind({"type": "api", "subtype": "unknown", "code": 1020}), "cursor_expired")
        self.assertEqual(probe.error_kind({"type": "api", "code": 99991400}), "rate_limited")
        self.assertEqual(probe.error_kind({"type": "authentication", "subtype": "refresh_server_error"}), "network_error")
        self.assertEqual(probe.error_kind({"type": "api", "code": 99992354}), "query_failed")

    def test_page_budget_remains_partial_and_cycles_are_rejected(self):
        p = object.__new__(probe.Probe)
        p.max_pages = 1
        items, complete, pages = p.pages("bounded", lambda token, n: {"items": [], "has_more": True, "page_token": "next"})
        self.assertEqual((items, complete, pages), ({}, False, 1))
        p.max_pages = 4
        tokens = ["a", "b", "a"]
        with self.assertRaises(probe.ContractError):
            p.pages("cycle", lambda token, n: {"items": [], "has_more": True, "page_token": tokens[n]})


if __name__ == "__main__":
    unittest.main()
