"""Run the idle conversation driver through accepted Lua and fake game RPCs."""
import copy
import fcntl
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import threading
import unittest
from unittest.mock import patch

import test_party_mana_supply as supply_fixture


_loader = importlib.machinery.SourceFileLoader(
    "converse_mana_driver", str(Path(__file__).with_name("converse")))
_spec = importlib.util.spec_from_loader(_loader.name, _loader)
converse = importlib.util.module_from_spec(_spec)
_loader.exec_module(converse)


class ManaDriverTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        supply_fixture.SupplyTest.setUpClass()
        cls.binary = Path(os.environ.get("BUDDY_LUAPOLICY_TEST_BINARY",
            Path(__file__).resolve().parents[1] / "data/bin/luapolicy"))

    def setUp(self):
        self.fixture = supply_fixture.SupplyTest(methodName="runTest")
        self.fixture.setUp()
        self.supply = converse.mana_supply_module()
        self.requests, self.bridge_calls, self.statuses = [], [], []
        self.chat_requests = []
        self.owner_pending = False
        self.owner_text = "Mira, hello"
        self.owner_receiver_guid = "4"
        self.on_request = None
        self.refresh_rpc = None
        temporary = tempfile.TemporaryDirectory(dir=Path(tempfile.gettempdir()).resolve())
        self.addCleanup(temporary.cleanup)
        self.profile_dir = Path(temporary.name)
        self.profile_identity = dict(profile="mira", name="Mira", **{"class": "mage"},
            guid=4, owner_guid=3, rpc_url="http://127.0.0.1:8087/rpc")
        self.lock_profile()
        test = self

        class ChatRPC:
            url = "http://127.0.0.1:8087/rpc"

            def call(self, method, params, timeout=None):
                test.chat_requests.append((method, copy.deepcopy(params), timeout))
                if method == "buddy-chat.poll":
                    after = int(params["afterSequence"])
                    latest = 1 if test.owner_pending else 0
                    messages = []
                    if test.owner_pending and after < 1:
                        messages.append(dict(sequence="1", senderGuid="3", channel="WHISPER",
                            receiverGuid=test.owner_receiver_guid, text=test.owner_text, self=False))
                    return dict(sessionId="chat-mira", latestSequence=str(latest),
                        nextSequence=str(latest), messages=messages)
                if method == "buddy-chat.send":
                    return dict(transportWritten=True)
                if test.refresh_rpc is not None:
                    return test.refresh_rpc(method, params, timeout)
                raise AssertionError("Unexpected conversation RPC: " + method)

        class NativeClient:
            def __init__(self, port):
                self.port = port

            def call(self, method, params, timeout=None):
                test.requests.append((self.port, method, copy.deepcopy(params), timeout))
                view = test.fixture.observations[self.port]
                keys = {"buddy-world.snapshot": "world", "buddy-actions.status": "actions",
                    "buddy-inventory.state": "inventory", "buddy-trade.state": "trade",
                    "buddy-party.state": "party", "buddy-quests.journal": "journal"}
                if method in keys:
                    result = copy.deepcopy(view.get(keys[method], {}))
                elif method == "buddy-actions.spell_toolkit":
                    result = copy.deepcopy(test.fixture.toolkit)
                else:
                    result = test.fixture.rpc(self.port, method, params)
                if test.on_request is not None:
                    test.on_request(self.port, method, params)
                return result

        class Brain:
            def __init__(self):
                self.calls = []

            def generate(self, history, state):
                self.calls.append((copy.deepcopy(history), copy.deepcopy(state)))
                return dict(text="Hello, Joel.", action="NONE", emote="NONE")

        self.rpc, self.brain = ChatRPC(), Brain()
        self.clients = {
            port: NativeClient(port) for port in (8087, 8086, 8402)}
        self.new_listener()

        # Select the qualified actual helper without replacing its evaluator or
        # changing the explicit source/value/execution bounds used by the adapter.
        real_bridge = self.supply.runtime.Bridge

        def actual_bridge(_binary, modules, **bounds):
            self.bridge_calls.append(dict(bounds))
            return real_bridge(self.binary, modules, **bounds)

        bridge_patch = patch.object(self.supply.runtime, "Bridge", side_effect=actual_bridge)
        bridge_patch.start()
        self.addCleanup(bridge_patch.stop)
        self.loop.tick()  # Actual baseline tails chat; no background action yet.
        self.assertEqual(self.requests, [])

    def lock_profile(self):
        self.profile_lock = os.fdopen(converse.private_open(
            self.profile_dir / "lock", os.O_RDWR | os.O_CREAT), "r+")
        fcntl.flock(self.profile_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        self.addCleanup(self.profile_lock.close)

    def new_listener(self, *, storage=True):
        storage_args = dict(supply_state_dir=self.profile_dir,
            profile_identity=self.profile_identity) if storage else {}
        self.controls = converse.BuddyControls(self.rpc, character_class="mage", character_guid=4,
            **storage_args)
        self.controls.refresh = lambda: None
        self.controls.poll_combat = lambda: None
        self.controls.context = lambda: {}
        self.controls._water_supply_clients = self.clients
        self.loop = converse.Conversation(self.rpc, self.brain, controls=self.controls,
            character_name="Mira", character_class="mage", character_guid=4,
            status=lambda **fields: self.statuses.append(fields))
        self.loop.maybe_repair_mission = lambda: False
        self.loop.maybe_review_combat = lambda: False
        self.loop.maybe_review_learned_spells = lambda: False
        self.loop.maybe_initiate = lambda: None

    def assert_actual_policy_bounds(self):
        self.assertTrue(self.bridge_calls)
        for bounds in self.bridge_calls:
            self.assertEqual(bounds["value_bytes"], 8 * 1024 * 1024)
            self.assertEqual(bounds["execution_ms"], 1000)

    def restore_actual_refresh(self):
        self.controls.refresh = converse.BuddyControls.refresh.__get__(self.controls, converse.BuddyControls)

    def test_owner_ack_is_durable_before_actual_fact_refresh_can_block_or_fail(self):
        self.restore_actual_refresh()
        self.brain.state_dir = self.profile_dir
        self.owner_pending = True
        entered, release = threading.Event(), threading.Event()
        errors = []

        def failed_refresh(method, params, timeout):
            if method == "buddy-party.state":
                entered.set()
                self.assertTrue(release.wait(3))
            raise converse.ConversationError("injected fact read failure")

        def run():
            try:
                self.loop.tick()
            except BaseException as error:
                errors.append(error)

        self.refresh_rpc = failed_refresh
        worker = threading.Thread(target=run)
        worker.start()
        try:
            self.assertTrue(entered.wait(3))
            sends = [params for method, params, _ in self.chat_requests if method == "buddy-chat.send"]
            self.assertEqual(sends, [dict(channel="WHISPER", language="COMMON",
                text="Got it—give me a moment.", recipient="Joel")])
            record = json.loads((self.profile_dir / "owner-acknowledgements/state.json").read_text())
            self.assertEqual(record["streams"]["chat-mira"], dict(sequence=1, outcome="transport_written"))
            methods = [method for method, _, _ in self.chat_requests]
            self.assertLess(methods.index("buddy-chat.send"), methods.index("buddy-party.state"))
            self.assertEqual(self.brain.calls, [])
        finally:
            release.set()
            worker.join(3)
        self.assertFalse(worker.is_alive())
        self.assertEqual(errors, [])
        self.assertEqual(len(self.brain.calls), 1)
        self.assertEqual(self.requests, [])
        self.assertEqual(self.fixture.calls, [])

    def test_owner_arriving_during_first_actual_refresh_stops_further_fact_reads(self):
        self.restore_actual_refresh()
        facts = []

        def first_fact(method, params, timeout):
            facts.append(method)
            self.assertEqual(method, "buddy-party.state")
            self.owner_pending = True
            return copy.deepcopy(self.fixture.mira["party"])

        self.refresh_rpc = first_fact
        self.loop.tick()
        self.assertEqual(facts, ["buddy-party.state"])
        self.assertTrue(self.controls._refresh_yielded)
        self.assertEqual(self.loop.cursor, 0)
        self.assertEqual(self.brain.calls, [])
        self.assertEqual(self.requests, [])
        self.assertEqual(self.bridge_calls, [])
        self.assertEqual(self.fixture.calls, [])

    def test_invitation_is_reserved_only_after_owner_fence_and_entered_failure_stays_reserved(self):
        for entered_failure in (False, True):
            with self.subTest(entered_failure=entered_failure):
                self.owner_pending = False
                self.owner_text = "Arlen, hello"
                self.owner_receiver_guid = "1"
                self.controls = converse.BuddyControls(self.rpc, character_class="priest", character_guid=1)
                self.controls.poll_combat = lambda: None
                self.controls.context = lambda: {}
                self.loop = converse.Conversation(self.rpc, self.brain, controls=self.controls,
                    character_name="Arlen", character_class="priest", character_guid=1,
                    status=lambda **fields: self.statuses.append(fields))
                self.loop.maybe_repair_mission = lambda: False
                self.loop.maybe_review_combat = lambda: False
                self.loop.maybe_review_learned_spells = lambda: False
                self.loop.maybe_initiate = lambda: None
                self.loop.initialized = True
                self.loop.session_id = "chat-mira"
                facts, accepts = [], []
                invitation = dict(sessionId="aw-1", pendingInvite=dict(sequence="7", inviterName="Joel"))

                def game_fact(method, params, timeout):
                    facts.append(method)
                    if method == "buddy-party.state":
                        return copy.deepcopy(invitation)
                    if method == "buddy-party.accept":
                        accepts.append(copy.deepcopy(params))
                        if entered_failure:
                            raise converse.ConversationError("entered accept reply was lost")
                        return dict(transportWritten=True)
                    if method == "buddy-actions.status":
                        return dict(sessionId="aw-1", selfGuid="1")
                    raise converse.ConversationError("fixture fact unavailable")

                policy_calls = 0

                def invitation_choice(party, owner_name, character_guid):
                    nonlocal policy_calls
                    policy_calls += 1
                    self.assertEqual(character_guid, 1)
                    if policy_calls == 1:
                        self.owner_pending = True
                    return dict(kind="accept", sessionId="aw-1", inviteSequence="7", inviterName="Joel")

                self.refresh_rpc = game_fact
                with patch.object(converse, "evaluate_party_invitation", side_effect=invitation_choice):
                    self.loop.tick()
                    self.assertEqual(facts, ["buddy-party.state"])
                    self.assertEqual(accepts, [])
                    self.assertEqual(self.controls.attempted_invites, set())
                    self.assertEqual(self.loop.cursor, 0)
                    self.loop.tick()  # Owner selection precedes the next eligible normal refresh.
                    self.assertEqual(self.loop.cursor, 1)
                    self.assertEqual(accepts, [dict(sessionId="aw-1", inviteSequence="7", inviterName="Joel")])
                    self.assertEqual(self.controls.attempted_invites, {("aw-1", 7)})
                    self.loop.tick()
                    self.assertEqual(len(accepts), 1)
                    self.assertEqual(self.controls.attempted_invites, {("aw-1", 7)})
                self.assertEqual(self.requests, [])
                self.assertEqual(self.fixture.calls, [])

    def test_profile_and_live_rpc_binding_mismatches_preserve_storage_without_supply_reads(self):
        runtime = self.supply.runtime
        correct_profile = copy.deepcopy(self.profile_identity)
        record = dict(version=1, profile=correct_profile,
            state=dict(last=dict(id="retained", outcome="transport_unconfirmed")))
        raw = runtime.packed(record, runtime.MAX_STATE)
        runtime.Store(self.profile_dir).write("mana-supply.json", raw, runtime.MAX_STATE)
        for profile_url, live_url in (
                ("http://127.0.0.1:8086/rpc", "http://127.0.0.1:8087/rpc"),
                ("http://127.0.0.1:8087/rpc", "http://127.0.0.1:8086/rpc"),
                ("http://127.0.0.1:8086/rpc", "http://127.0.0.1:8086/rpc")):
            with self.subTest(profile_url=profile_url, live_url=live_url):
                self.profile_identity = dict(correct_profile, rpc_url=profile_url)
                self.rpc.url = live_url
                self.new_listener()
                self.loop.tick()
                self.loop.tick()
                self.assertEqual(self.controls.water_supply_phase, "accepted_water_policy_unavailable")
                self.assertEqual((self.profile_dir / "mana-supply.json").read_bytes(), raw)
                self.assertEqual(self.requests, [])
                self.assertEqual(self.bridge_calls, [])
                self.assertEqual(self.fixture.calls, [])

    def test_loaded_profile_rechecks_live_rpc_binding_before_further_supply_reads(self):
        self.fixture.fail_method = "buddy-actions.create_item"
        self.loop.tick()
        self.assertTrue(self.controls._water_supply_loaded)
        self.assertEqual(self.controls.lua_water_supply["last"]["outcome"], "transport_unconfirmed")
        reservation = copy.deepcopy(self.controls.lua_water_supply["memory"]["pending"])
        state_path = self.profile_dir / "mana-supply.json"
        stored = state_path.read_bytes()
        reads, evaluations, mutations = len(self.requests), len(self.bridge_calls), len(self.fixture.calls)
        self.rpc.url = "http://127.0.0.1:8086/rpc"
        self.loop.tick()
        self.assertEqual(self.controls.water_supply_phase, "accepted_water_policy_unavailable")
        self.assertEqual(len(self.requests), reads)
        self.assertEqual(len(self.bridge_calls), evaluations)
        self.assertEqual(len(self.fixture.calls), mutations)
        self.assertEqual(self.controls.lua_water_supply["last"]["outcome"], "transport_unconfirmed")
        self.assertEqual(self.controls.lua_water_supply["memory"]["pending"], reservation)
        self.assertEqual(state_path.read_bytes(), stored)
        self.assert_actual_policy_bounds()

    def test_idle_tick_conjures_without_a_model(self):
        self.loop.tick()
        self.assertEqual(self.controls.water_supply_phase, "conjure_requested")
        self.assertEqual(self.fixture.calls, [(8087, "buddy-actions.create_item",
            dict(sessionId="aw-4", selfGuid="4", spellId=5506))])
        self.assertEqual(self.brain.calls, [])
        self.assertEqual(self.loop.cursor, 0)
        self.assert_actual_policy_bounds()

    def test_idle_ticks_share_the_exact_lua_selected_stack(self):
        supply_fixture.carried(self.fixture.mira, 2136, 8)
        self.loop.tick()
        self.assertEqual(self.controls.water_supply_phase, "recipient_water_reservation_requested")
        self.loop.tick()
        self.assertEqual(self.controls.water_supply_phase, "water_gift_offer_requested")
        self.assertEqual([row[:2] for row in self.fixture.calls],
            [(8086, "buddy-trade.water_gift"), (8087, "buddy-trade.water_gift")])
        self.assertEqual(self.fixture.calls[-1][2]["partnerGuid"], "1")
        self.assertEqual(self.fixture.calls[-1][2]["expectedItemGuid"], str(0x4000000000000001))
        self.assertEqual(self.fixture.calls[-1][2]["backpackSlot"], 8)
        self.assertEqual(self.brain.calls, [])
        self.assert_actual_policy_bounds()

    def test_current_accepted_source_edit_changes_next_idle_choice(self):
        self.fixture.arlen["inventory"]["manaDrinkCount"] = 8
        self.fixture.source = self.fixture.source.replace(
            "peer_reorder_threshold=12", "peer_reorder_threshold=5")
        with patch.object(self.supply.time, "monotonic", return_value=100):
            self.loop.tick()
        self.assertEqual(self.controls.water_supply_phase, "water_stock_sufficient")
        self.assertEqual(self.fixture.calls, [])
        original_revision = self.fixture.revision()
        self.fixture.source = self.fixture.source.replace(
            "peer_reorder_threshold=5", "peer_reorder_threshold=12")
        self.fixture.generation += 1
        with patch.object(self.supply.time, "monotonic", return_value=105):
            self.loop.tick()
        self.assertNotEqual(self.fixture.revision(), original_revision)
        self.assertEqual(self.controls.water_supply_phase, "conjure_requested")
        self.assertEqual(self.fixture.calls[-1][2]["spellId"], 5506)
        self.assertEqual(self.brain.calls, [])
        self.assert_actual_policy_bounds()

    def test_selected_owner_request_bypasses_idle_driver(self):
        self.owner_pending = True
        self.loop.tick()
        self.assertEqual(self.requests, [])
        self.assertEqual(self.bridge_calls, [])
        self.assertEqual(self.fixture.calls, [])
        self.assertEqual(self.loop.cursor, 1)
        self.assertEqual(len(self.brain.calls), 1)
        self.assertEqual(self.controls.lua_water_supply, {})

    def test_owner_arriving_between_observations_sends_nothing_and_keeps_cursor(self):
        def owner_after_first_read(port, method, params):
            if port == 8087 and method == "buddy-world.snapshot":
                self.owner_pending = True
        self.on_request = owner_after_first_read
        self.loop.tick()
        self.assertEqual([(port, method) for port, method, _, _ in self.requests],
            [(8087, "buddy-world.snapshot")])
        self.assertEqual(self.controls.water_supply_phase, "yielded_to_owner")
        self.assertEqual(self.controls.lua_water_supply, {"phase": "waiting_for_current_mage"})
        self.assertEqual(self.fixture.calls, [])
        self.assertEqual(self.bridge_calls, [])
        self.assertEqual(self.loop.cursor, 0)
        self.assertEqual(self.brain.calls, [])
        self.loop.tick()
        self.assertEqual(self.loop.cursor, 1)
        self.assertEqual(len(self.brain.calls), 1)
        self.assertEqual(len(self.requests), 1)

    def test_owner_at_final_dispatch_persists_no_send_then_settles_on_next_normal_turn(self):
        attach_calls = 0
        mage_status_calls = 0

        def owner_after_final_post_save_body_check(port, method, params):
            nonlocal attach_calls, mage_status_calls
            if method == "session.attach":
                attach_calls += 1
            if port == 8087 and method == "buddy-actions.status":
                mage_status_calls += 1
                if mage_status_calls == 3:
                    self.assertEqual(attach_calls, 3)
                    stored = self.supply.runtime.decoded(self.supply.runtime.read_file(
                        self.profile_dir / "mana-supply.json", self.supply.runtime.MAX_STATE, True))
                    self.assertEqual(stored["state"]["last"]["outcome"], "transport_unconfirmed")
                    self.assertEqual(stored["state"]["memory"]["pending"]["kind"], "conjure")
                    self.owner_pending = True
        self.on_request = owner_after_final_post_save_body_check
        self.loop.tick()
        self.assertEqual(attach_calls, 3)
        self.assertEqual(mage_status_calls, 3)
        self.assertEqual(self.controls.water_supply_phase, "yielded_to_owner")
        state = self.controls.lua_water_supply
        self.assertEqual(state["last"]["outcome"], "not_dispatched")
        self.assertEqual(state["last"]["reason"], "owner_input_pending")
        self.assertEqual(state["memory"]["pending"]["kind"], "conjure")
        reserved = copy.deepcopy(state["memory"]["pending"])
        stored = self.supply.runtime.decoded(self.supply.runtime.read_file(
            self.profile_dir / "mana-supply.json", self.supply.runtime.MAX_STATE, True))
        self.assertEqual(stored["state"]["last"]["outcome"], "not_dispatched")
        self.assertEqual(stored["state"]["memory"]["pending"], reserved)
        self.assertEqual(self.fixture.calls, [])
        self.assertEqual(self.loop.cursor, 0)
        self.loop.tick()  # The actual owner selection wins; the reservation stays.
        self.assertEqual(self.loop.cursor, 1)
        self.assertEqual(state["memory"]["pending"], reserved)
        self.loop.tick()  # Only a subsequent normal Lua turn settles this refusal.
        self.assertEqual(self.controls.water_supply_phase, "owner_input_pending")
        self.assertEqual(state["last"]["outcome"], "not_dispatched")
        self.assertIsNone(state["memory"].get("pending"))
        self.assertEqual(self.fixture.calls, [])
        self.assertEqual(len(self.brain.calls), 1)
        self.assert_actual_policy_bounds()

    def test_restart_loads_durable_unknown_before_reading_edited_policy_without_replay(self):
        self.fixture.fail_method = "buddy-actions.create_item"
        self.loop.tick()
        self.assertEqual(self.controls.water_supply_phase, "water_supply_rpc_unconfirmed")
        self.assertEqual(len(self.fixture.calls), 1)
        state_path = self.profile_dir / "mana-supply.json"
        stored = self.supply.runtime.decoded(self.supply.runtime.read_file(
            state_path, self.supply.runtime.MAX_STATE, True))
        self.assertEqual(stored["profile"], self.profile_identity)
        self.assertEqual(stored["state"]["last"]["outcome"], "transport_unconfirmed")
        reservation = copy.deepcopy(stored["state"]["memory"]["pending"])
        self.fixture.source = self.fixture.source.replace("stock_minimum=5", "stock_minimum=99")
        self.fixture.generation += 1
        self.profile_lock.close()
        self.lock_profile()
        self.new_listener()
        self.assertEqual(self.controls.lua_water_supply, {})
        self.loop.tick()  # New listener's real chat baseline.
        self.loop.tick()
        self.assertEqual(self.controls.water_supply_phase, "conjure_outcome_unconfirmed")
        self.assertEqual(self.controls.lua_water_supply["memory"]["pending"], reservation)
        self.assertEqual(self.controls.lua_water_supply["last"]["outcome"], "transport_unconfirmed")
        self.assertEqual(len(self.fixture.calls), 1)
        self.assert_actual_policy_bounds()

    def test_persistence_failure_prevents_the_selected_native_write(self):
        with patch.object(self.supply.runtime.Store, "write", side_effect=OSError("injected fsync failure")):
            self.loop.tick()
        self.assertEqual(self.fixture.calls, [])
        self.assertFalse((self.profile_dir / "mana-supply.json").exists())
        self.assert_actual_policy_bounds()

    def test_missing_durable_profile_refuses_supply_before_any_game_rpc(self):
        self.new_listener(storage=False)
        self.loop.tick()
        self.loop.tick()
        self.assertEqual(self.controls.water_supply_phase, "accepted_water_policy_unavailable")
        self.assertEqual(self.requests, [])
        self.assertEqual(self.bridge_calls, [])
        self.assertEqual(self.fixture.calls, [])

    def test_foreign_profile_record_remains_untouched_and_sends_nothing(self):
        runtime = self.supply.runtime
        record = dict(version=1, profile=dict(self.profile_identity, guid=8),
            state=dict(last=dict(id="retained", outcome="transport_unconfirmed")))
        raw = runtime.packed(record, runtime.MAX_STATE)
        runtime.Store(self.profile_dir).write("mana-supply.json", raw, runtime.MAX_STATE)
        self.loop.tick()
        self.assertEqual(self.controls.water_supply_phase, "accepted_water_policy_unavailable")
        self.assertEqual((self.profile_dir / "mana-supply.json").read_bytes(), raw)
        self.assertEqual(self.requests, [])
        self.assertEqual(self.bridge_calls, [])
        self.assertEqual(self.fixture.calls, [])

    def test_malformed_private_state_remains_untouched_and_sends_nothing(self):
        runtime = self.supply.runtime
        raw = b'{"version":1,"profile":'
        runtime.Store(self.profile_dir).write("mana-supply.json", raw, runtime.MAX_STATE)
        self.loop.tick()
        self.assertEqual(self.controls.water_supply_phase, "accepted_water_policy_unavailable")
        self.assertEqual((self.profile_dir / "mana-supply.json").read_bytes(), raw)
        self.assertEqual(self.requests, [])
        self.assertEqual(self.bridge_calls, [])
        self.assertEqual(self.fixture.calls, [])


if __name__ == "__main__":
    unittest.main()
