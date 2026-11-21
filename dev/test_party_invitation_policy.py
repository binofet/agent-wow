"""Invitation consent through the real bounded Lua evaluator; no game RPCs."""
import copy
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import tempfile
import unittest


ROOT = Path(__file__).resolve().parent.parent
SOURCE = ROOT / "behaviors/party-invitation.lua"
loader = importlib.machinery.SourceFileLoader("invitation_policy_runtime", str(ROOT / "dev/task-runtime"))
spec = importlib.util.spec_from_loader(loader.name, loader)
runtime = importlib.util.module_from_spec(spec)
loader.exec_module(runtime)


class PartyInvitationPolicyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        binary = Path(os.environ.get("BUDDY_LUAPOLICY_TEST_BINARY", ROOT / "data/bin/luapolicy"))
        if not binary.is_file() or not os.access(binary, os.X_OK):
            raise RuntimeError("actual Lua evaluator required: set BUDDY_LUAPOLICY_TEST_BINARY or build data/bin/luapolicy")
        cls.bridge = runtime.Bridge(binary, ROOT / "modules")

    def frame(self):
        # Party's actual schema has no own GUID field. The host supplies its
        # configured body separately; the observed session still must match.
        return dict(sessionId="invitation-session", selfGuid="4", ownerName="Joel", inviteDecisionType="absent",
                    party=dict(sessionId="invitation-session",
                               pendingInvite=dict(sequence="1", inviterName="Joel")))

    def set_decision(self, frame, decision):
        frame["party"]["pendingInvite"]["decision"] = decision
        frame["inviteDecisionType"] = ("null" if decision is None else
            "boolean" if type(decision) is bool else "string" if type(decision) is str else
            "integer" if type(decision) is int else "other")

    def evaluate(self, frame, *, source=None):
        result = self.bridge.evaluate(SOURCE.read_text() if source is None else source, frame)
        self.assertIsInstance(result, dict)
        if result.get("kind") == "wait":
            self.assertEqual(set(result), {"kind", "reason"})
            self.assertIsInstance(result["reason"], str)
            self.assertTrue(1 <= len(result["reason"]) <= 128)
        else:
            self.assertEqual(set(result), {"kind", "sessionId", "inviteSequence", "inviterName"})
            self.assertEqual(result["kind"], "accept")
        return result

    def assert_wait(self, frame):
        self.assertEqual(self.evaluate(frame)["kind"], "wait")

    def test_owner_pending_invitation_preserves_exact_native_request(self):
        for decision in ("omitted", "PENDING", 0):
            with self.subTest(decision=decision):
                frame = self.frame()
                if decision != "omitted":
                    self.set_decision(frame, decision)
                original = copy.deepcopy(frame)
                self.assertEqual(self.evaluate(frame), dict(kind="accept", sessionId="invitation-session",
                    inviteSequence="1", inviterName="Joel"))
                self.assertEqual(frame, original)

    def test_other_names_and_case_variants_wait(self):
        for name in ("Otherplayer", "joel", "JOEL", "Joel ", "Joël"):
            with self.subTest(name=name):
                frame = self.frame()
                frame["party"]["pendingInvite"]["inviterName"] = name
                self.assert_wait(frame)
        frame = self.frame()
        frame["ownerName"] = frame["party"]["pendingInvite"]["inviterName"] = "Configuredowner"
        self.assertEqual(self.evaluate(frame)["inviterName"], "Configuredowner")

    def test_reserved_or_unknown_invitation_status_never_reconsents(self):
        for decision in ("ACCEPT_REQUESTED", "DECLINE_REQUESTED", "UNKNOWN", "0", 1, 2, -1, True, False, None, 0.0, [], {}):
            with self.subTest(decision=decision):
                frame = self.frame()
                self.set_decision(frame, decision)
                self.assert_wait(frame)

    def test_missing_or_contradictory_host_shape_facts_wait(self):
        for shape in (None, False, 0, "PENDING", "string", "integer", "boolean", "null", "other"):
            with self.subTest(shape=shape):
                frame = self.frame()
                frame["inviteDecisionType"] = shape
                self.assert_wait(frame)
        for decision in ("PENDING", 0, True):
            frame = self.frame()
            frame["party"]["pendingInvite"]["decision"] = decision
            self.assert_wait(frame)

    def test_uint64_sequence_is_never_rounded_or_converted_to_a_number(self):
        for sequence in ("1", "9007199254740991", "9007199254740992", "9007199254740993", "18446744073709551615"):
            with self.subTest(sequence=sequence):
                frame = self.frame()
                frame["party"]["pendingInvite"]["sequence"] = sequence
                result = self.evaluate(frame)
                self.assertEqual(result["kind"], "accept")
                self.assertIsInstance(result["inviteSequence"], str)
                self.assertEqual(result["inviteSequence"], sequence)

    def test_invalid_sequence_or_configured_body_identifiers_wait(self):
        invalid = (None, "", "0", "00", "01", "-1", "+1", "1.0", "1e3", " 1", "1 ",
                   "18446744073709551616", "184467440737095516150", "not-an-id", 0, 1, 1.0,
                   True, False, [], {})
        for key in ("sequence", "selfGuid"):
            for value in invalid:
                with self.subTest(key=key, value=value):
                    frame = self.frame()
                    target = frame["party"]["pendingInvite"] if key == "sequence" else frame
                    target[key] = value
                    self.assert_wait(frame)
        frame = self.frame()
        frame["selfGuid"] = "18446744073709551615"
        self.assertEqual(self.evaluate(frame)["kind"], "accept")

    def test_large_numeric_identifiers_are_rejected_before_lua_can_round_them(self):
        for key in ("sequence", "selfGuid"):
            for value in (9007199254740993, float(9007199254740993)):
                with self.subTest(key=key, value=value):
                    frame = self.frame()
                    target = frame["party"]["pendingInvite"] if key == "sequence" else frame
                    target[key] = value
                    with self.assertRaisesRegex(runtime.BridgeFailure, "outside exact JSON integer range"):
                        self.evaluate(frame)

    def test_unavailable_malformed_or_foreign_party_context_waits(self):
        for value in (None, False, "unavailable", [], {}, {"sessionId": "other-session"}):
            with self.subTest(party=value):
                frame = self.frame()
                frame["party"] = value
                self.assert_wait(frame)
        for value in (None, False, "unavailable", [], {}, {"sequence": "1"}, {"inviterName": "Joel"}):
            with self.subTest(invite=value):
                frame = self.frame()
                frame["party"]["pendingInvite"] = value
                self.assert_wait(frame)
        frame = self.frame()
        del frame["party"]["pendingInvite"]
        self.assert_wait(frame)

    def test_context_names_and_session_bounds(self):
        for key in ("sessionId", "ownerName"):
            for value in (None, "", False, 1, [], {}, "x" * 129):
                with self.subTest(key=key, value=value):
                    frame = self.frame()
                    frame[key] = value
                    self.assert_wait(frame)
        for size in (1, 128):
            frame = self.frame()
            frame["sessionId"] = frame["party"]["sessionId"] = "s" * size
            frame["ownerName"] = frame["party"]["pendingInvite"]["inviterName"] = "n" * size
            self.assertEqual(self.evaluate(frame)["kind"], "accept")
        frame = self.frame()
        frame["party"]["pendingInvite"]["inviterName"] = "n" * 129
        self.assert_wait(frame)

    def test_missing_binding_and_extra_action_input_cannot_choose_an_rpc(self):
        for key in ("sessionId", "selfGuid", "ownerName", "party", "inviteDecisionType"):
            with self.subTest(missing=key):
                frame = self.frame()
                del frame[key]
                self.assert_wait(frame)
        frame = self.frame()
        frame["action"] = {"method": "buddy-party.accept", "eligible": True}
        self.assert_wait(frame)

    def test_same_evaluator_reads_edited_current_policy_and_disables_fresh_invites(self):
        source = SOURCE.read_text()
        self.assertEqual(source.count("accept_owner_invites=true"), 1)
        with tempfile.TemporaryDirectory(prefix="invitation-policy-") as folder:
            path = Path(folder) / "party-invitation.lua"
            path.write_text(source)
            self.assertEqual(self.evaluate(self.frame(), source=path.read_text())["kind"], "accept")
            path.write_text(source.replace("accept_owner_invites=true", "accept_owner_invites=false", 1))
            frame = self.frame()
            frame["party"]["pendingInvite"]["sequence"] = "2"
            self.assertEqual(self.evaluate(frame, source=path.read_text()),
                dict(kind="wait", reason="owner_invitation_acceptance_disabled"))

    def test_default_evaluator_budgets_are_unchanged(self):
        self.assertIsNone(self.bridge.execution_ms)
        self.assertEqual(self.bridge.value_bytes, 32 * 1024)


if __name__ == "__main__":
    unittest.main()
