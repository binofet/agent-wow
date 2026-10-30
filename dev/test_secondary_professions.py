"""Mock HTTP only: no live RPC, game packets, actors, processes or body leases."""
import copy
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import types
import unittest
from unittest import mock

loader = importlib.machinery.SourceFileLoader("secondary_professions_test", str(Path(__file__).with_name("secondary-professions")))
spec = importlib.util.spec_from_loader(loader.name, loader)
m = importlib.util.module_from_spec(spec)
loader.exec_module(m)
URL = "http://127.0.0.1:8192/rpc"


def initial_state():
    return {"sessionId": "body1", "selfGuid": "7", "revision": "5", "skillsKnown": True,
            "skillsRevision": "8", "skills": [{"skillId": skill, "rank": 1, "maximum": 75} for skill in (129, 185, 356)],
            "spellsKnown": True, "backpackKnown": True, "backpackRevision": "11", "recipes": [
                {"skillId": skill, "spellId": spell, "known": True, "ready": True, "requirementsVerified": True}
                for spell, skill in m.CATALOG.items()]}


class Clock:
    def __init__(self):
        self.value, self.sleeps = 100.0, []

    def now(self):
        return self.value

    def sleep(self, delay):
        self.sleeps.append(delay)
        self.value += delay


class MockHTTP:
    def __init__(self):
        self.state, self.calls, self.timeouts, self.read_bounds = initial_state(), [], [], []
        self.on_state, self.on_attempt, self.on_cancel, self.raw_reply = None, None, None, None

    def operation(self, request, status="COMPLETED"):
        return {"operationId": "op-" + request["requestId"], "requestId": request["requestId"],
                "skillId": request["skillId"], "spellId": request["spellId"], "status": status,
                "ownGoObserved": True, "outputInventoryObserved": True, "reagentsConsumedObserved": True,
                "beforeRank": 1, "afterRank": 1, "skillIncreased": False, "leaseReleased": status in m.TERMINAL}

    def open(self, request, timeout):
        payload = json.loads(request.data)
        self.calls.append(copy.deepcopy(payload)); self.timeouts.append(timeout)
        method, params = payload["method"], payload["params"]
        if self.raw_reply is not None:
            raw = self.raw_reply(payload)
        else:
            if method == "buddy-professions.state":
                if self.on_state:
                    self.on_state(self)
            elif method == "buddy-professions.attempt":
                self.state["operation"] = self.operation(params)
                self.state["active"] = False
                if self.on_attempt:
                    self.on_attempt(self)
            elif method == "buddy-professions.cancel":
                if self.on_cancel:
                    self.on_cancel(self)
                else:
                    self.state["operation"]["status"] = "CANCELLED"
                    self.state["operation"]["leaseReleased"] = True
                    self.state["active"] = False
            else:
                raise AssertionError(method)
            raw = json.dumps({"jsonrpc": "2.0", "id": payload["id"], "result": self.state}).encode()
        owner = self

        class Response:
            def __enter__(self):
                return self

            def __exit__(self, *args):
                pass

            def read(self, bound):
                owner.read_bounds.append(bound)
                return raw[:bound]

        return Response()

    def methods(self):
        return [request["method"] for request in self.calls]


class SecondaryProfessionTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.path = Path(temporary.name) / "receipts" / "practice.json"
        self.clock, self.http = Clock(), MockHTTP()

    def rpc(self, seconds=30):
        return m.RPC(URL, self.clock.now() + seconds, now=self.clock.now, opener=self.http)

    def practice(self, maximum=1, seconds=30, skills=(129, 185, 356)):
        with m.Journal(self.path, URL, 7) as journal:
            return m.Worker(self.rpc(seconds), 7, journal, skills, sleep=self.clock.sleep).practice(maximum)

    def stop(self, operation_id=None, seconds=30):
        with m.Journal(self.path, URL, 7) as journal:
            return m.Worker(self.rpc(seconds), 7, journal, sleep=self.clock.sleep).stop(operation_id)

    def pending(self, http):
        http.state["operation"]["status"], http.state["active"] = "WAITING_CAST", True
        http.state["operation"]["leaseReleased"] = False

    def test_cli_requires_explicit_endpoint_character_and_stop_identity(self):
        for args in (["status"], ["status", "--rpc-url", URL], ["status", "--character-guid", "7"]):
            with self.subTest(args=args), mock.patch("sys.stderr"):
                with self.assertRaises(SystemExit):
                    m.main(args)
        with self.assertRaisesRegex(m.Error, "stop_requires_explicit"):
            m.main(["stop", "--rpc-url", URL, "--character-guid", "7"])
        for value in ("0", "4", "-1"):
            with self.assertRaisesRegex(m.Error, "bound_invalid"):
                m.main(["practice", "--rpc-url", URL, "--character-guid", "7", "--max-casts", value])
        for seconds in ("nan", "inf", "0", "90.1"):
            with self.assertRaisesRegex(m.Error, "bound_invalid"):
                m.main(["practice", "--rpc-url", URL, "--character-guid", "7", "--seconds", seconds])

    def test_pinned_loopback_url_and_fixed_method_allowlist(self):
        for url in ("http://localhost:8192/rpc", "http://127.0.0.2:8192/rpc", "https://127.0.0.1:8192/rpc",
                    "http://127.0.0.1/rpc", "http://127.0.0.1:8192/rpc/", "http://127.0.0.1:8192/rpc?x=1",
                    "http://user@127.0.0.1:8192/rpc", "http://127.0.0.1:8192/rpc#x", "http://[::1]:8192/rpc"):
            with self.subTest(url=url), self.assertRaises(m.Error):
                m.rpc_url(url)
        with self.assertRaisesRegex(m.Error, "method_not_allowed"):
            self.rpc().call("buddy-training.buy", {})
        self.assertEqual(self.http.calls, [])

    def test_transport_has_no_proxy_cookie_or_redirect_authority(self):
        with mock.patch.object(m.urllib.request, "build_opener", return_value=self.http) as build:
            rpc = m.RPC(URL, self.clock.now() + 1, now=self.clock.now)
            rpc.call("buddy-professions.state", {})
        handlers = build.call_args.args
        self.assertIsInstance(handlers[0], m.urllib.request.ProxyHandler)
        self.assertEqual(handlers[0].proxies, {})
        self.assertIsInstance(handlers[1], m.NoRedirect)
        with self.assertRaisesRegex(m.Error, "redirect_refused"):
            handlers[1].redirect_request(None, None, 302, "", {}, "http://other/rpc")
        self.assertLessEqual(self.http.timeouts[0], 1)
        self.assertEqual(self.http.read_bounds, [m.MAX_RESPONSE + 1])

    def test_response_shape_id_size_and_json_ambiguity_fail_closed(self):
        for reply in (b'[]', b'{', b'NaN', b'{"jsonrpc":"2.0","id":true,"result":{}}',
                      b'{"jsonrpc":"2.0","id":2,"result":{}}', b'{"jsonrpc":"2.0","id":1,"result":{},"error":{}}',
                      b'{"jsonrpc":"2.0","id":1,"id":1,"result":{}}', b'x' * (m.MAX_RESPONSE + 1)):
            with self.subTest(reply=reply[:80]):
                self.http.raw_reply = lambda request, reply=reply: reply
                with self.assertRaises(m.Error):
                    self.rpc().call("buddy-professions.state", {})

    def test_exact_fresh_attempt_binding_uuid_and_private_receipt(self):
        result = self.practice()
        self.assertEqual(result["phase"], "bounded_practice_finished")
        self.assertEqual(self.http.methods(), ["buddy-professions.state", "buddy-professions.state", "buddy-professions.attempt"])
        request = self.http.calls[-1]["params"]
        self.assertEqual(request, {"sessionId": "body1", "requestId": request["requestId"], "expectedRevision": "5",
                                  "skillId": 129, "spellId": 3275, "expectedSkillsRevision": "8", "expectedBackpackRevision": "11"})
        self.assertEqual(str(m.uuid.UUID(request["requestId"])), request["requestId"])
        self.assertEqual(self.path.stat().st_mode & 0o777, 0o600)
        receipt = json.loads(self.path.read_text())["attempts"][0]
        self.assertEqual(receipt["request"], request)
        self.assertEqual(receipt["operation"]["status"], "COMPLETED")
        self.assertFalse(receipt["operation"]["skillIncreased"])

    def test_three_cast_limit_uses_unique_ids_and_current_selected_skill(self):
        result = self.practice(maximum=3, skills=(185,))
        self.assertEqual(result["phase"], "bounded_practice_finished")
        attempts = [row["params"] for row in self.http.calls if row["method"] == "buddy-professions.attempt"]
        self.assertEqual(len(attempts), 3)
        self.assertEqual(len({row["requestId"] for row in attempts}), 3)
        self.assertTrue(all(row["skillId"] == 185 for row in attempts))

    def test_actual_material_focus_and_learning_blockers_are_retained(self):
        for recipe in self.http.state["recipes"]:
            recipe.update(ready=False, reason="own_material_missing" if recipe["skillId"] == 129 else "observed_focus_missing")
        result = self.practice()
        self.assertEqual(result["phase"], "blocked")
        self.assertEqual({row["reason"] for row in result["blockers"]}, {"own_material_missing", "observed_focus_missing"})
        self.assertNotIn("buddy-professions.attempt", self.http.methods())
        self.assertFalse(self.path.exists())

    def test_bad_identity_session_revision_or_recipe_change_forbids_write(self):
        for change in (lambda state: state.update(selfGuid="8"), lambda state: state.update(sessionId="body2"),
                       lambda state: state.update(revision="4"), lambda state: state["recipes"][0].update(ready=False)):
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                self.path, self.http = Path(directory) / "receipt.json", MockHTTP()
                reads = [0]

                def mutate(http):
                    reads[0] += 1
                    if reads[0] == 2:
                        change(http.state)

                self.http.on_state = mutate
                result = self.practice()
                self.assertIn(result["phase"], ("outcome_unconfirmed", "blocked"))
                self.assertNotIn("buddy-professions.attempt", self.http.methods())

    def test_save_failure_prevents_attempt(self):
        with mock.patch.object(m.Journal, "save", side_effect=OSError("disk full")):
            with self.assertRaises(OSError):
                self.practice()
        self.assertNotIn("buddy-professions.attempt", self.http.methods())

    def test_reservation_directory_barrier_precedes_every_attempt_and_cancel(self):
        events = []
        original_replace, original_fsync = m.os.replace, m.os.fsync

        def replace(*args):
            events.append("replace")
            return original_replace(*args)

        def fsync(descriptor):
            events.append("directory_sync" if m.stat.S_ISDIR(os.fstat(descriptor).st_mode) else "file_sync")
            return original_fsync(descriptor)

        def verify_barrier(http):
            self.assertEqual(events[-2:], ["replace", "directory_sync"])
            self.pending(http)
            events.append("attempt")

        def verify_cancel_barrier(http):
            self.assertEqual(events[-2:], ["replace", "directory_sync"])
            events.append("cancel")
            http.state["operation"]["status"], http.state["active"] = "CANCELLED", False

        self.http.on_attempt, self.http.on_cancel = verify_barrier, verify_cancel_barrier
        with mock.patch.object(m.os, "replace", side_effect=replace), mock.patch.object(m.os, "fsync", side_effect=fsync):
            self.practice(seconds=3)
        self.assertEqual(events.count("attempt"), 1)
        self.assertEqual(events.count("cancel"), 1)

    def test_failed_directory_barrier_forbids_mutation(self):
        self.path.parent.mkdir()
        original_fsync = m.os.fsync

        def fail_directory(descriptor):
            if m.stat.S_ISDIR(os.fstat(descriptor).st_mode):
                raise OSError("directory barrier failed")
            return original_fsync(descriptor)

        with mock.patch.object(m.os, "fsync", side_effect=fail_directory):
            with self.assertRaisesRegex(OSError, "directory barrier failed"):
                self.practice()
        self.assertNotIn("buddy-professions.attempt", self.http.methods())

    def test_uncertain_attempt_is_not_replayed_on_next_invocation(self):
        def ambiguous(http):
            self.pending(http)
            raise OSError("ambiguous transport")

        self.http.on_attempt = ambiguous
        self.assertEqual(self.practice()["phase"], "outcome_unconfirmed")
        self.http.on_attempt = None
        self.http.state["active"] = False
        self.http.state["operation"]["status"] = "COMPLETED"
        result = self.practice(maximum=3)
        self.assertEqual(result["phase"], "prior_attempt_reconciled_no_new_cast")
        self.assertEqual(self.http.methods().count("buddy-professions.attempt"), 1)

    def test_duplicate_outcome_stops_without_third_fresh_id(self):
        prior = [None]

        def repeated(http):
            if prior[0] is None:
                prior[0] = copy.deepcopy(http.state["operation"])
            else:
                http.state["operation"] = copy.deepcopy(prior[0])

        self.http.on_attempt = repeated
        result = self.practice(maximum=3)
        self.assertEqual(result["phase"], "stopped_after_attempt")
        self.assertEqual(self.http.methods().count("buddy-professions.attempt"), 2)
        self.assertNotIn("buddy-professions.cancel", self.http.methods())

    def test_same_request_with_other_catalog_recipe_cannot_complete_or_be_cancelled(self):
        for status in ("COMPLETED", "WAITING_CAST"):
            with self.subTest(status=status), tempfile.TemporaryDirectory() as directory:
                self.path, self.http = Path(directory) / "receipt.json", MockHTTP()

                def wrong_recipe(http):
                    http.state["operation"].update(skillId=185, spellId=2538, status=status)
                    http.state["active"] = status == "WAITING_CAST"

                self.http.on_attempt = wrong_recipe
                result = self.practice(maximum=3)
                self.assertEqual(result["phase"], "stopped_after_attempt")
                self.assertEqual(result["reason"], "owned_operation_identity_unconfirmed")
                self.assertEqual(self.http.methods().count("buddy-professions.attempt"), 1)
                self.assertNotIn("buddy-professions.cancel", self.http.methods())

    def test_unconfirmed_terminal_or_cleanup_never_continues_cycle(self):
        for change in (lambda operation: operation.update(status="OUTCOME_UNCONFIRMED"),
                       lambda operation: operation.update(cleanupUnconfirmed=True)):
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                self.path, self.http = Path(directory) / "receipt.json", MockHTTP()
                self.http.on_attempt = lambda http: change(http.state["operation"])
                self.practice(maximum=3)
                self.assertEqual(self.http.methods().count("buddy-professions.attempt"), 1)
                self.assertNotIn("buddy-professions.cancel", self.http.methods())

    def test_cleanup_reserves_once_and_cancels_only_own_fresh_operation_inside_budget(self):
        self.http.on_attempt = self.pending
        result = self.practice(maximum=3, seconds=3)
        self.assertEqual(result["phase"], "stopped_after_attempt")
        self.assertEqual(self.http.methods().count("buddy-professions.cancel"), 1)
        request = next(row["params"] for row in self.http.calls if row["method"] == "buddy-professions.attempt")
        cancel = next(row["params"] for row in self.http.calls if row["method"] == "buddy-professions.cancel")
        self.assertEqual(cancel, {"sessionId": "body1", "operationId": "op-" + request["requestId"]})
        self.assertLessEqual(self.clock.value, 103)
        receipt = json.loads(self.path.read_text())["attempts"][0]
        self.assertTrue(receipt["cancelReserved"])
        self.assertEqual(receipt["operation"]["status"], "CANCELLED")

    def test_uncertain_cancel_is_never_automatically_retried(self):
        self.http.on_attempt = self.pending
        self.http.on_cancel = lambda http: (_ for _ in ()).throw(OSError("uncertain cancel"))
        self.practice(seconds=3)
        operation_id = self.http.state["operation"]["operationId"]
        result = self.stop(operation_id)
        self.assertEqual(result["phase"], "stop_unconfirmed")
        self.assertEqual(self.http.methods().count("buddy-professions.cancel"), 1)

    def test_foreign_operation_or_expired_budget_cannot_receive_cleanup(self):
        for late in (False, True):
            with self.subTest(late=late), tempfile.TemporaryDirectory() as directory:
                self.path, self.http, self.clock = Path(directory) / "receipt.json", MockHTTP(), Clock()
                self.http.on_attempt = self.pending

                def changed(http):
                    if self.clock.value >= 101 and http.state.get("active"):
                        if late:
                            self.clock.value = 103
                        else:
                            http.state["operation"]["requestId"] = "foreign-request"
                            http.state["operation"]["operationId"] = "foreign-operation"

                self.http.on_state = changed
                self.practice(seconds=3)
                self.assertNotIn("buddy-professions.cancel", self.http.methods())

    def test_explicit_stop_refuses_other_id_and_uses_exact_owned_id(self):
        request = {"requestId": "external-request", "skillId": 129, "spellId": 3275}
        self.http.state.update(active=True, operation=self.http.operation(request, "WAITING_CAST"))
        with self.assertRaisesRegex(m.Error, "explicit_operation_not_current"):
            self.stop("wrong-operation")
        self.assertNotIn("buddy-professions.cancel", self.http.methods())
        result = self.stop(self.http.state["operation"]["operationId"])
        self.assertEqual(result["phase"], "stop_observed")
        self.assertEqual(self.http.methods().count("buddy-professions.cancel"), 1)

    def test_readonly_call_limit_is_finite_and_no_late_dispatch_occurs(self):
        self.http.on_attempt = self.pending
        with mock.patch.object(m, "MAX_CALLS", 5):
            result = self.practice(seconds=30)
        self.assertEqual(result["rpcCalls"], 5)
        self.assertNotIn("buddy-professions.cancel", self.http.methods())
        rpc = self.rpc(seconds=1)
        self.clock.value += 1
        before = len(self.http.calls)
        with self.assertRaisesRegex(m.Error, "deadline_expired"):
            rpc.call("buddy-professions.attempt", {})
        self.assertEqual(len(self.http.calls), before)

    def test_private_journal_cannot_follow_symlink_or_change_actor_identity(self):
        self.practice()
        original = self.path.read_bytes()
        self.path.chmod(0o644)
        with self.assertRaisesRegex(m.Error, "mode0600"):
            self.practice()
        self.path.chmod(0o600)
        with self.assertRaisesRegex(m.Error, "identity_or_bound"):
            with m.Journal(self.path, URL, 8):
                pass
        target = self.path.with_name("target.json")
        target.write_bytes(original); target.chmod(0o600)
        self.path.unlink(); self.path.symlink_to(target)
        with self.assertRaises(OSError):
            self.practice()


if __name__ == "__main__":
    unittest.main(verbosity=2)
