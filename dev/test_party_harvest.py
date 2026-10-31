"""Independent harvest coordinator tests: in-memory RPC only, never a realm."""
import copy
import fcntl
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import tempfile
import threading
import types
import unittest
from unittest.mock import patch


loader = importlib.machinery.SourceFileLoader("tested_party_harvest", str(Path(__file__).with_name("party-harvest")))
spec = importlib.util.spec_from_loader(loader.name, loader)
m = importlib.util.module_from_spec(spec)
loader.exec_module(m)


class Clock:
    def __init__(self): self.value = 100.
    def now(self): return self.value
    def sleep(self, seconds): self.value += seconds


def player(guid, x=0.):
    return dict(guid=str(guid), present=True, mapKnown=True, mapId=0, positionKnown=True,
                positionRevision="7", x=x, y=0., z=0., aliveKnown=True, alive=True,
                healthKnown=True, health=100, maxHealth=100, unitFlagsKnown=True,
                movementFlagsKnown=True, channelSpellKnown=True, channelObjectKnown=True)


class FakeRPC:
    def __init__(self, guid=5, target_x=2.):
        self.guid, self.skill, self.spell = guid, m.ROLES[guid][2], m.ROLES[guid][3]
        self.url = f"http://127.0.0.1:{m.ROLES[guid][1]}/rpc"
        self.session, self.calls, self.saved = "body1", [], []
        self.inventory, self.skills, self.revision = 23, 8, 5
        self.own, self.owner = player(guid), player(3, 1.)
        self.actions = dict(following=True, healing=True, assisting=True,
                            position=dict(known=True, mapKnown=True, mapId=0, x=0., y=0., z=0.))
        self.loot = dict(enabled=True, phase="idle")
        self.trade, self.quests = dict(phase="IDLE"), {}
        self.inventory_changes = {}
        self.group = dict(known=True, inGroup=True, members=[
            dict(guid=str(guid), self=True, online=True), dict(guid="3", name="Joel", online=True)])
        self.bags = [dict(bag=b, slotKnown=True, contentsKnown=True, slotCount=16 if b == 0 else 0,
                          slots=[dict(bag=0, slot=i) for i in range(16)] if b == 0 else []) for b in range(5)]
        self.bags[0]["slots"][0].update(itemGuid=str(0x4000000000000011), entry=7005,
                                         stackCount=1, metadataKnown=True)
        self.target = dict(guid=str((0xf130 if guid == 5 else 0xf110) << 48 | 30 << 24 | 17),
                           entry=30, present=True, mapKnown=True, mapId=0, positionKnown=True,
                           positionRevision="9", x=target_x, y=0., z=0., unitFlagsKnown=True,
                           dynamicFlagsKnown=True, aliveKnown=True, alive=False, healthKnown=True,
                           health=0, maxHealth=100, levelKnown=True, level=5, unitFlags=0x04000000,
                           typeKnown=True, gameObjectType=3, flagsKnown=True, stateKnown=True,
                           state=1, createdByKnown=True)
        self.approachable, self.fully_looted, self.metadata = True, True, True
        self.inventory_known, self.spells_known, self.skills_known = True, True, True
        self.nearby_players, self.creatures, self.operation = [], [], None
        self.active, self.closing = False, False
        self.fail_attempt, self.attempt_status = False, "COMPLETED"
        self.initial_reserved, self.after_attempt = False, None
        self.before_call, self.after_travel = None, None
        self.route_block, self.route_started = None, threading.Event()
        self.route_reason, self.fail_release, self.token = "exploration_within_distance", False, None
        self.age = 0

    def target_row(self):
        target = copy.deepcopy(self.target)
        target["positionAgeMs"] = self.age  # Changing ages must not invalidate stable identity.
        ready = m.distance(target, self.actions["position"]) <= 3 and not self.token
        return {"creature" if self.guid == 5 else "object": target,
                "approachable": self.approachable and not self.token and self.metadata,
                "ready": ready and self.approachable and self.metadata,
                "fullyLootedObserved": self.fully_looted, "requiredSkill": 0, "lockId": 29}

    def state(self):
        state = dict(sessionId=self.session, selfGuid=str(self.guid), revision=str(self.revision),
                     inventoryKnown=self.inventory_known, inventoryRevision=str(self.inventory),
                     skillsKnown=self.skills_known, skillsRevision=str(self.skills), spellsKnown=self.spells_known,
                     skills=[dict(skillId=self.skill, rank=75, maximum=75)],
                     recipes=[dict(skillId=self.skill, spellId=self.spell, known=self.spells_known)],
                     active=self.active, closing=self.closing)
        state["skinningTargets" if self.guid == 5 else "herbTargets"] = [self.target_row()]
        if self.operation is not None: state["operation"] = copy.deepcopy(self.operation)
        return state

    def world(self):
        self.age += 1
        return dict(self=copy.deepcopy(self.own), owner=copy.deepcopy(self.owner),
                    inventoryKnown=self.inventory_known, inventoryRevision=str(self.inventory),
                    inventoryBags=copy.deepcopy(self.bags), nearbyPlayers=copy.deepcopy(self.nearby_players),
                    nearbyCreatures=copy.deepcopy(self.creatures), nearbyGatheringObjects=[copy.deepcopy(self.target)])

    def complete(self, request):
        baseline = int(request["expectedInventoryRevision"])
        return dict(operationId="owned-harvest-operation", requestId=request["requestId"],
                    targetGuid=request["targetGuid"], targetEntry=self.target["entry"],
                    targetPositionRevision=request["targetPositionRevision"], skillId=self.skill, spellId=self.spell,
                    baselineInventoryRevision=str(baseline), observedInventoryRevision=str(baseline+1),
                    baselineSkillsRevision=request["expectedSkillsRevision"], status=self.attempt_status,
                    ownGoObserved=True, lootWindowObserved=True, lootReleaseObserved=True,
                    outputInventoryObserved=True, ownLootedItemsObserved=2, lootSlotsRemoved=1,
                    leaseReleased=True, fullyLootedObserved=self.guid == 5)

    def call(self, method, params):
        self.calls.append((method, copy.deepcopy(params)))
        if self.before_call: self.before_call(method, params)
        if method == "buddy-professions.state": return self.state()
        if method == "buddy-professions.inspect_herb":
            assert self.guid == 1 and params == dict(sessionId=self.session, targetGuid=self.target["guid"])
            return self.state()
        if method == "buddy-world.snapshot": return self.world()
        if method == "buddy-actions.status": return copy.deepcopy(self.actions)
        if method == "buddy-party.state": return dict(sessionId=self.session, group=copy.deepcopy(self.group))
        if method == "buddy-loot.state": return dict(sessionId=self.session, **copy.deepcopy(self.loot))
        if method == "buddy-trade.state": return dict(sessionId=self.session, **copy.deepcopy(self.trade))
        if method == "buddy-quests.state": return dict(sessionId=self.session, **copy.deepcopy(self.quests))
        if method == "buddy-inventory.state":
            result = dict(sessionId=self.session, selfGuid=str(self.guid), inventoryKnown=self.inventory_known,
                          inventoryRevision=str(self.inventory))
            result.update(copy.deepcopy(self.inventory_changes))
            return result
        if method == "buddy-actions.acquire_interaction":
            assert params == dict(owner="exploration", leaseMs=8000, harvest=True) and self.token is None
            self.token = "owned-harvest-route-token"
            self.actions.update(interactionOwner="exploration", interactionHarvest=True)
            return dict(token=self.token, state=copy.deepcopy(self.actions))
        if method == "buddy-actions.travel_to":
            assert params == dict(token=self.token, mapId=0, x=self.target["x"], y=0., z=0., arrivalDistance=2.5)
            self.route_started.set()
            if self.route_block: self.route_block.wait(2)
            else: self.actions["position"]["x"] = self.target["x"]-2
            if self.after_travel: self.after_travel()
            return dict(reason=self.route_reason, moving=False, motionUnconfirmed=False)
        if method == "buddy-actions.release_interaction":
            assert params == dict(owner="exploration", token=self.token)
            if self.fail_release: raise m.Error("ambiguous route stop")
            self.token = None
            self.actions.update(interactionOwner="", interactionHarvest=False, moving=False, traveling=False)
            if self.route_block: self.route_block.set()
            return copy.deepcopy(self.actions)
        if method == "buddy-professions.attempt":
            assert self.token is None, "profession must acquire its own lease after route release"
            assert self.saved and self.saved[-1]["attempts"][-1]["phase"] == "attempt_reserved"
            assert self.saved[-1]["attempts"][-1]["request"] == params["requestId"]
            self.operation = self.complete(params)
            if self.initial_reserved:
                self.operation.update(targetEntry=0, status="RESERVED", ownGoObserved=False,
                                      lootWindowObserved=False, lootReleaseObserved=False,
                                      outputInventoryObserved=False, ownLootedItemsObserved=0,
                                      lootSlotsRemoved=0, leaseReleased=False)
                self.active = True
            elif self.attempt_status == "COMPLETED": self.inventory += 1
            else: self.operation.update(cleanupUnconfirmed=True, leaseReleased=False)
            if self.after_attempt: self.after_attempt()
            if self.fail_attempt: raise m.Error("attempt reply lost")
            return self.state()
        if method == "buddy-professions.cancel":
            assert params == dict(sessionId=self.session, operationId=self.operation["operationId"])
            return self.state()
        raise AssertionError("unexpected or intent-changing RPC: " + method)


class HarvestTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root_patch = patch.object(m, "ROOT", Path(self.temp.name))
        self.root_patch.start(); self.addCleanup(self.root_patch.stop)

    def rig(self, guid=5, x=2., stored=None, priority=None):
        rpc, clock = FakeRPC(guid, x), Clock()
        worker = m.Worker(guid, rpc, save=lambda value: rpc.saved.append(copy.deepcopy(value)),
                          stored=stored, now=clock.now, sleep=clock.sleep, priority=priority or (lambda: False))
        return worker, rpc, clock

    def pre_attempt(self,guid=5):
        worker,rpc,clock=self.rig(guid)
        row=dict(session='prior-body',guid=int(rpc.target['guid']),target=copy.deepcopy(rpc.target),
            request='harvest_prior_unattempted',phase='unconfirmed',attempted=False)
        worker.data['attempts'].append(row);rpc.session='new-body'
        return worker,rpc,clock,row

    def test_roles_endpoint_and_malformed_ledger_fail_closed(self):
        rpc = FakeRPC()
        for guid in (3, 4, 6):
            with self.assertRaises(m.Error): m.Worker(guid, rpc)
        rpc.url = "http://127.0.0.1:8086/rpc"
        with self.assertRaises(m.Error): m.Worker(5, rpc)
        worker, rpc, _ = self.rig()
        for stored in ({"identity": {}, "attempts": []}, dict(identity=worker.identity, attempts="bad"),
                       dict(identity=worker.identity, attempts=[{}]),
                       dict(identity=worker.identity, attempts=[{}]*129)):
            with self.assertRaises(m.Error): m.Worker(5, rpc, stored=stored)
        self.assertEqual(rpc.calls, [])

    def test_maintenance_only_explicit_idle_phases_without_reserved_work(self):
        actor = types.SimpleNamespace(status={}, data=dict(training=[], mail=None))
        for phase in m.IDLE_PHASES | {"waiting_for_training_confirmation", "stipend_reserved", "service_request_or_observation_unavailable"}:
            actor.status["phase"] = phase
            self.assertEqual(m.maintenance_idle(actor), phase in m.IDLE_PHASES)
        actor.status["phase"] = "training_budget_reserved"
        actor.data["training"] = [dict(status="reserved")]
        self.assertFalse(m.maintenance_idle(actor))
        actor.data.update(training=[], mail=dict(status="reserved"))
        self.assertFalse(m.maintenance_idle(actor))

    def test_passive_standing_quests_do_not_block_but_concrete_work_does(self):
        base = [("quest_task_snapshot", dict(active=False)), ("area_quest_snapshot", dict(active=False)),
                ("party_town_snapshot", dict(active=True, busy=False))]
        with patch.object(m, "routine_locked", return_value=False):
            with patch.object(m.c, base[0][0], return_value=base[0][1]), patch.object(m.c, base[1][0], return_value=base[1][1]), patch.object(m.c, base[2][0], return_value=base[2][1]):
                for phase in m.PASSIVE_QUEST_PHASES | {"approaching_joels_observed_questgiver", "waiting_for_mutation_cooldown"}:
                    with patch.object(m.c, "party_quest_sync_snapshot", return_value=dict(active=True, updatedAt=m.time.time(), own=dict(phase=phase))):
                        self.assertEqual(m.higher_priority(5), phase not in m.PASSIVE_QUEST_PHASES)
                with patch.object(m.c, "party_quest_sync_snapshot", return_value=dict(active=True, updatedAt=0, own=dict(phase="following_joels_quest_changes"))):
                    self.assertTrue(m.higher_priority(5))
        worker, rpc, _ = self.rig(priority=lambda: True)
        worker.tick(); self.assertEqual(rpc.calls, [])

    def test_one_fixed_attempt_is_durable_and_never_changes_intent(self):
        for guid in (1, 5):
            with self.subTest(guid=guid):
                worker, rpc, clock = self.rig(guid)
                worker.tick()
                attempts = [p for method, p in rpc.calls if method == "buddy-professions.attempt"]
                self.assertEqual(len(attempts), 1)
                request = attempts[0]
                self.assertEqual((request["skillId"], request["spellId"]), m.ROLES[guid][2:])
                self.assertEqual(request["targetGuid"], rpc.target["guid"])
                self.assertEqual(request["targetPositionRevision"], "9")
                self.assertEqual((request["expectedInventoryRevision"], request["expectedSkillsRevision"]), ("23", "8"))
                self.assertEqual(worker.data["attempts"][0]["phase"], "completed")
                self.assertTrue(rpc.actions["following"] and rpc.actions["healing"] and rpc.actions["assisting"] and rpc.loot["enabled"])
                clock.sleep(3); worker.tick()
                self.assertEqual(sum(method == "buddy-professions.attempt" for method, _ in rpc.calls), 1)

    def test_idle_followup_quest_wait_allows_harvest_but_current_safety_still_blocks(self):
        phase = "waiting_for_joel_at_followup_giver_or_server_offer"
        with patch.object(m, "routine_locked", return_value=False), \
                patch.object(m.c, "quest_task_snapshot", return_value=dict(active=False)), \
                patch.object(m.c, "area_quest_snapshot", return_value=dict(active=False)), \
                patch.object(m.c, "party_town_snapshot", return_value=dict(active=True, busy=False)), \
                patch.object(m.c, "party_quest_sync_snapshot") as snapshot:
            for guid in (1, 5):
                for mode in ("idle", "party_alive_wait", "own_dead", "party_combat", "unknown_alive",
                             "unknown_party", "unknown_sync", "stale_sync", "active_sync", "quest_action"):
                    with self.subTest(guid=guid, mode=mode):
                        sync = dict(active=True, updatedAt=m.time.time(), own=dict(phase=phase))
                        worker, rpc, _ = self.rig(guid, priority=lambda: m.higher_priority(guid))
                        if mode == "party_alive_wait": sync["own"]["phase"] = "waiting_for_alive_known_party_members"
                        elif mode == "own_dead": rpc.own["alive"] = False
                        elif mode == "party_combat": rpc.owner["unitFlags"] = 0x80000
                        elif mode == "unknown_alive": rpc.own["aliveKnown"] = False
                        elif mode == "unknown_party": rpc.group["known"] = False
                        elif mode == "unknown_sync": sync["own"].pop("phase")
                        elif mode == "stale_sync": sync["updatedAt"] = m.time.time()-16
                        elif mode == "active_sync": sync["own"]["phase"] = "approaching_joels_observed_questgiver"
                        elif mode == "quest_action": rpc.quests["actionInFlight"] = True
                        snapshot.return_value = sync
                        if mode == "idle":
                            worker.tick()
                            self.assertEqual(worker.data["attempts"][0]["phase"], "completed")
                            self.assertEqual(sum(method == "buddy-professions.attempt" for method, _ in rpc.calls), 1)
                        else:
                            worker.deadline = 120
                            with self.assertRaises((m.Error, m.prof.Error)): worker.read()
                            self.assertEqual(worker.data["attempts"], [])
                            self.assertFalse(any(method in ("buddy-professions.attempt", "buddy-actions.acquire_interaction")
                                                 for method, _ in rpc.calls))

    def test_read_blocks_actual_danger_missing_learning_knife_and_inventory(self):
        for mode in ("combat", "owner_combat", "healing", "attacker", "quest", "loot", "trade", "skill", "spell", "knife", "unknown_bag", "unknown_item", "partial_bag", "no_capacity", "other_profession", "owner_moving"):
            with self.subTest(mode=mode):
                worker, rpc, _ = self.rig()
                if mode == "combat": rpc.own["unitFlags"] = 0x80000
                elif mode == "owner_combat": rpc.owner["unitFlags"] = 0x80000
                elif mode == "healing": rpc.owner["health"] = 50
                elif mode == "attacker": rpc.creatures = [dict(present=True, alive=True, attackVictimKnown=True, attackVictimGuid="5", attackAgeMs=0)]
                elif mode == "quest": rpc.quests["actionInFlight"] = True
                elif mode == "loot": rpc.loot["pendingCorpseGuid"] = rpc.target["guid"]
                elif mode == "trade": rpc.trade["phase"] = "OPEN"
                elif mode == "skill": rpc.skills_known = False
                elif mode == "spell": rpc.spells_known = False
                elif mode == "knife": rpc.bags[0]["slots"][0]["entry"] = 1
                elif mode == "unknown_bag": rpc.inventory_known = False
                elif mode == "unknown_item": rpc.bags[0]["slots"][0]["metadataKnown"] = False
                elif mode == "partial_bag": rpc.bags[0]["slots"].pop()
                elif mode == "no_capacity":
                    for i, row in enumerate(rpc.bags[0]["slots"]): row.update(itemGuid=str(0x4000000000000100+i), entry=7005 if i == 0 else 7070, stackCount=1, metadataKnown=True)
                elif mode == "other_profession": rpc.active = True
                elif mode == "owner_moving": rpc.owner["movementFlags"] = 1
                worker.deadline = 120
                with self.assertRaises((m.Error, m.prof.Error)): worker.read()
                self.assertFalse(any(method in ("buddy-professions.attempt", "buddy-actions.acquire_interaction") for method, _ in rpc.calls))

    def test_only_metadata_qualified_fully_looted_current_target_can_be_selected(self):
        for mode in ("not_approachable", "not_fully_looted", "metadata", "far", "vertical", "unknown_pose"):
            with self.subTest(mode=mode):
                worker, rpc, _ = self.rig()
                if mode == "not_approachable": rpc.approachable = False
                elif mode == "not_fully_looted": rpc.fully_looted = False
                elif mode == "metadata": rpc.metadata = False
                elif mode == "far": rpc.target["x"] = 20.01
                elif mode == "vertical": rpc.target["z"] = 1.51
                elif mode == "unknown_pose": rpc.target["positionKnown"] = False
                worker.tick()
                self.assertEqual(worker.data["attempts"], [])
                self.assertFalse(any(method == "buddy-professions.attempt" for method, _ in rpc.calls))

    def test_stable_age_changes_and_guarded_short_route_release_before_cast(self):
        worker, rpc, _ = self.rig(x=10.)
        worker.tick()
        names = [method for method, _ in rpc.calls]
        self.assertEqual(names.count("buddy-actions.travel_to"), 1)
        self.assertLess(names.index("buddy-actions.release_interaction"), names.index("buddy-professions.attempt"))
        self.assertEqual(worker.data["attempts"][0]["phase"], "completed")
        self.assertIsNone(rpc.token)

    def test_route_yields_to_combat_owner_control_target_or_priority_changes(self):
        for mode in ("combat", "owner", "control", "target", "priority"):
            with self.subTest(mode=mode):
                priority = [False]
                worker, rpc, _ = self.rig(x=10., priority=lambda: priority[0])
                rpc.route_block = threading.Event()
                def change(method, params):
                    if not rpc.route_started.is_set() or method != "buddy-actions.status": return
                    if mode == "combat": rpc.own["unitFlags"] = 0x80000
                    elif mode == "owner": rpc.owner["x"] += 1
                    elif mode == "control": rpc.actions["following"] = False
                    elif mode == "target": rpc.target["positionRevision"] = "10"
                    else: priority[0] = True
                rpc.before_call = change
                worker.tick()
                self.assertEqual(sum(method == "buddy-actions.release_interaction" for method, _ in rpc.calls), 1)
                self.assertFalse(any(method == "buddy-professions.attempt" for method, _ in rpc.calls))
                self.assertIsNone(rpc.token)
                if mode == "control": self.assertFalse(rpc.actions["following"])
                rpc.route_block.set()

    def test_uncertain_route_cleanup_and_lost_attempt_never_replay_after_restart(self):
        for mode in ("route", "attempt"):
            with self.subTest(mode=mode):
                worker, rpc, clock = self.rig(x=10. if mode == "route" else 2.)
                if mode == "route": rpc.fail_release = True
                else: rpc.fail_attempt = True; rpc.attempt_status = "OUTCOME_UNCONFIRMED"
                worker.tick()
                self.assertEqual(worker.data["attempts"][0]["phase"], "unconfirmed")
                count = sum(method == "buddy-professions.attempt" for method, _ in rpc.calls)
                restarted = m.Worker(5, rpc, stored=copy.deepcopy(worker.data), now=clock.now, sleep=clock.sleep, priority=lambda: False)
                rpc.session = "new-body"
                restarted.tick()
                self.assertEqual(sum(method == "buddy-professions.attempt" for method, _ in rpc.calls), count)
                self.assertEqual(restarted.data["attempts"][0]["phase"], "unconfirmed")

    def test_late_exact_receipt_reconciles_without_new_cast(self):
        worker, rpc, _ = self.rig()
        rpc.fail_attempt = True
        worker.tick()
        self.assertEqual(worker.data["attempts"][0]["phase"], "unconfirmed")
        worker.tick()
        self.assertEqual(worker.data["attempts"][0]["phase"], "completed")
        self.assertEqual(sum(method == "buddy-professions.attempt" for method, _ in rpc.calls), 1)

    def test_old_pre_attempt_retires_only_as_unknown_and_next_tick_can_harvest_new_target(self):
        for guid in (1,5):
            with self.subTest(guid=guid):
                worker,rpc,clock,row=self.pre_attempt(guid);prior=copy.deepcopy(row)
                if guid==1:rpc.operation=dict(status='COMPLETED',leaseReleased=True)
                status=worker.tick()
                self.assertEqual(status['phase'],'harvest_blocked')
                self.assertEqual(status['reason'],'attempt_blocked')
                self.assertEqual(row['retirement'],dict(reason='prior_body_pre_attempt_retired',priorRow=prior,
                    newSession='new-body',outcome='UNKNOWN'))
                self.assertEqual({method for method,_ in rpc.calls},
                    {'buddy-professions.state','buddy-actions.status','buddy-world.snapshot'})
                self.assertEqual(len(rpc.saved),1)
                stored=copy.deepcopy(worker.data)
                restarted=m.Worker(guid,rpc,stored=stored,save=lambda value:rpc.saved.append(copy.deepcopy(value)),
                    now=clock.now,sleep=clock.sleep,priority=lambda:False)
                rpc.target['guid']=str(int(rpc.target['guid'])+1);rpc.target['positionRevision']='10'
                restarted.tick()
                attempts=[req for method,req in rpc.calls if method=='buddy-professions.attempt']
                self.assertEqual(len(attempts),1)
                self.assertEqual(attempts[0]['sessionId'],'new-body')
                self.assertEqual(attempts[0]['targetGuid'],rpc.target['guid'])
                self.assertNotEqual(attempts[0]['requestId'],prior['request'])
                self.assertEqual(restarted.data['attempts'][0]['retirement']['priorRow'],prior)
                self.assertEqual(restarted.data['attempts'][-1]['phase'],'completed')

    def test_pre_attempt_retirement_keeps_same_body_issued_or_unsettled_cases_waiting(self):
        modes=('same_body','attempted','attempted_missing','active','closing','operation_unknown','operation_empty','operation_malformed','cleanup_unknown',
            'lease_release_unknown','operation_id','old_receipt','lease','moving','traveling','motion_unknown','cast',
            'autoattack','recovery','local_token','foreign_self','body_changes','stop','priority')
        for mode in modes:
            with self.subTest(mode=mode):
                worker,rpc,_,row=self.pre_attempt();prior=copy.deepcopy(row)
                if mode=='same_body':rpc.session=row['session']
                elif mode=='attempted':row['attempted']=True
                elif mode=='attempted_missing':del row['attempted']
                elif mode=='active':rpc.active=True
                elif mode=='closing':rpc.closing=True
                elif mode=='operation_unknown':rpc.operation=dict(status='OUTCOME_UNCONFIRMED',leaseReleased=True)
                elif mode=='operation_empty':rpc.operation={}
                elif mode=='operation_malformed':rpc.operation=[]
                elif mode=='cleanup_unknown':rpc.operation=dict(status='COMPLETED',cleanupUnconfirmed=True,leaseReleased=True)
                elif mode=='lease_release_unknown':rpc.operation=dict(status='COMPLETED')
                elif mode=='operation_id':row['operationId']='old-operation'
                elif mode=='old_receipt':row['receipt']=dict(cleanupUnconfirmed=True)
                elif mode=='lease':rpc.actions['interactionOwner']='exploration'
                elif mode=='moving':rpc.actions['moving']=True
                elif mode=='traveling':rpc.actions['traveling']=True
                elif mode=='motion_unknown':rpc.actions['motionUnconfirmed']=True
                elif mode=='cast':rpc.actions['castPending']=True
                elif mode=='autoattack':rpc.actions['autoAttackRequested']=True
                elif mode=='recovery':rpc.actions['recovery']=dict(active=True)
                elif mode=='local_token':worker.token='old-owned-token'
                elif mode=='foreign_self':rpc.own['guid']='4'
                elif mode=='body_changes':
                    reads=[0]
                    def change(method,params):
                        if method=='buddy-professions.state':
                            reads[0]+=1
                            if reads[0]==2:rpc.session='third-body'
                    rpc.before_call=change
                elif mode=='stop':worker.stopped=lambda:True
                elif mode=='priority':worker.priority=lambda:True
                prior=copy.deepcopy(row);worker.tick()
                self.assertEqual(row,prior)
                self.assertEqual(worker.status['phase'],'harvest_unconfirmed')
                self.assertFalse(rpc.saved)
                self.assertFalse(any(method in ('buddy-professions.attempt','buddy-professions.cancel',
                    'buddy-actions.acquire_interaction','buddy-actions.release_interaction','buddy-actions.travel_to') for method,_ in rpc.calls))

    def test_retired_pre_attempt_does_not_bypass_fresh_next_tick_danger_checks(self):
        worker,rpc,_,row=self.pre_attempt();worker.tick()
        self.assertEqual(row['phase'],'blocked')
        rpc.own['unitFlags']=0x80000
        with self.assertRaises(m.Error):worker.tick()
        self.assertEqual(len(worker.data['attempts']),1)
        self.assertFalse(any(method=='buddy-professions.attempt' for method,_ in rpc.calls))

    def test_prior_body_retirement_validation_and_failed_save_preserve_uncertainty(self):
        worker,rpc,clock,row=self.pre_attempt();prior=copy.deepcopy(row)
        def failure(_):raise OSError('local journal save failed')
        worker.save=failure
        with self.assertRaises(OSError):worker.tick()
        self.assertEqual(row,prior)
        worker.save=lambda value:rpc.saved.append(copy.deepcopy(value));worker.tick()
        valid=copy.deepcopy(worker.data)
        for mode in ('attempted','same_body','prior_changed','success_claim','original_phase','malformed'):
            with self.subTest(mode=mode):
                bad=copy.deepcopy(valid);entry=bad['attempts'][0];retirement=entry['retirement']
                if mode=='attempted':entry['attempted']=True
                elif mode=='same_body':retirement['newSession']=entry['session']
                elif mode=='prior_changed':retirement['priorRow']['request']='changed-prior-request'
                elif mode=='success_claim':retirement['outcome']='COMPLETED'
                elif mode=='original_phase':retirement['priorRow']['phase']='completed'
                else:entry['retirement']=[]
                with self.assertRaises(m.Error):m.Worker(5,rpc,stored=bad,now=clock.now,priority=lambda:False)

    def test_new_priority_stop_or_deadline_after_checkpoint_prevents_attempt(self):
        for mode in ("priority", "stop", "deadline"):
            with self.subTest(mode=mode):
                flags = dict(priority=False, stop=False)
                worker, rpc, clock = self.rig(priority=lambda: flags["priority"])
                worker.stopped = lambda: flags["stop"]
                def save(value):
                    rpc.saved.append(copy.deepcopy(value))
                    if value["attempts"][-1]["phase"] == "attempt_reserved":
                        if mode == "deadline": clock.value = worker.deadline
                        else: flags[mode] = True
                worker.save = save
                worker.tick()
                self.assertFalse(any(method == "buddy-professions.attempt" for method, _ in rpc.calls))

    def test_idle_trade_with_pending_acceptance_or_unknown_junk_still_blocks(self):
        for changes in (dict(partnerAccepted=True), dict(acceptTransportWritten=True),
                        dict(lastJunkDisposal=dict(status="UNCONFIRMED_NO_RETRY")),
                        dict(lastJunkDisposal=dict(status="TRANSPORT_UNCONFIRMED_NO_RETRY"))):
            with self.subTest(changes=changes):
                worker, rpc, _ = self.rig()
                rpc.trade.update(changes)
                worker.deadline = 120
                with self.assertRaises(m.Error): worker.read()
                self.assertFalse(any(method == "buddy-professions.attempt" for method, _ in rpc.calls))

    def test_actual_completed_trade_history_no_longer_blocks_skinning_but_incomplete_proof_does(self):
        complete=dict(phase='COMPLETE',partnerGuid='3',partnerAccepted=True,acceptTransportWritten=True,
            partnerItems=[dict(entry=2962,count=1)],ownRequestedItems=[dict(entry=7070,count=1)],
            lastServerStatus=8,reason='server_confirmed_trade_complete',completedTrades='1')
        worker,rpc,_=self.rig();rpc.trade.update(complete);worker.tick()
        self.assertEqual(sum(method=='buddy-professions.attempt' for method,_ in rpc.calls),1)
        for mode in ('missing_status','missing_reason','no_completed_counter','junk_pending'):
            with self.subTest(mode=mode):
                worker,rpc,_=self.rig();rpc.trade.update(copy.deepcopy(complete));worker.deadline=120
                if mode=='missing_status':rpc.trade.pop('lastServerStatus')
                elif mode=='missing_reason':rpc.trade.pop('reason')
                elif mode=='no_completed_counter':rpc.trade['completedTrades']='0'
                else:rpc.trade['lastJunkDisposal']={'status':'AWAITING_SERVER_SLOT_UPDATE'}
                with self.assertRaises(m.Error):worker.read()
                self.assertFalse(any(method=='buddy-professions.attempt' for method,_ in rpc.calls))

    def test_actual_inventory_identity_knownness_and_pending_operation_take_priority(self):
        for changes in (dict(sessionId="foreign"), dict(selfGuid="4"), dict(inventoryRevision="24"),
                        dict(inventoryKnown=False), dict(active=True), dict(closing=True),
                        dict(operation=dict(status="OUTCOME_UNCONFIRMED", leaseReleased=True)),
                        dict(operation=dict(status="REQUESTED", leaseReleased=False)),
                        dict(operation=dict(status="CONFIRMED", leaseReleased=False))):
            with self.subTest(changes=changes):
                worker, rpc, _ = self.rig()
                rpc.inventory_changes = changes
                worker.deadline = 120
                with self.assertRaises(m.Error): worker.read()
                self.assertFalse(any(method == "buddy-professions.attempt" for method, _ in rpc.calls))

    def test_initial_reserved_zero_entry_still_captures_and_cancels_only_owned_operation(self):
        for mode in ("stop", "priority"):
            with self.subTest(mode=mode):
                flags = dict(stop=False, priority=False)
                worker, rpc, _ = self.rig(priority=lambda: flags["priority"])
                worker.stopped = lambda: flags["stop"]
                rpc.initial_reserved = True
                rpc.after_attempt = lambda: flags.update({mode: True})
                worker.tick()
                row = worker.data["attempts"][0]
                self.assertEqual(row["phase"], "unconfirmed")
                self.assertEqual(row["operationId"], "owned-harvest-operation")
                cancels = [params for method, params in rpc.calls if method == "buddy-professions.cancel"]
                self.assertEqual(cancels, [dict(sessionId="body1", operationId="owned-harvest-operation")])
                self.assertEqual(sum(method == "buddy-professions.attempt" for method, _ in rpc.calls), 1)

    def test_cancel_does_not_target_replaced_session_request_or_operation(self):
        for mode in ("session", "request", "operation", "target", "skill", "spell"):
            with self.subTest(mode=mode):
                worker, rpc, _ = self.rig()
                worker.tick()
                row = worker.data["attempts"][0]
                rpc.active = True
                if mode == "session": rpc.session = "new-body"
                else:
                    key = dict(request="requestId", operation="operationId", target="targetGuid", skill="skillId", spell="spellId")[mode]
                    rpc.operation[key] = "other" if mode in ("request", "operation") else "999"
                worker.cancel_owned(row)
                self.assertFalse(any(method == "buddy-professions.cancel" for method, _ in rpc.calls))

    def test_ordinary_eighty_five_percent_healing_boundary_is_preserved(self):
        for health in (84, 85, 100):
            worker, rpc, _ = self.rig()
            worker.deadline = 120
            rpc.owner["health"] = health
            if health < 85:
                with self.assertRaises(m.Error): worker.read()
            else: self.assertEqual(worker.read()[0]["sessionId"], "body1")

    def test_receipt_requires_exact_request_target_baselines_and_materials(self):
        worker, rpc, _ = self.rig()
        worker.tick()
        row = copy.deepcopy(worker.data["attempts"][0])
        state = rpc.state()
        for key in ("requestId", "targetGuid", "targetEntry", "targetPositionRevision", "baselineInventoryRevision", "baselineSkillsRevision", "spellId", "skillId"):
            with self.subTest(key=key):
                bad = copy.deepcopy(state); bad["operation"][key] = "999" if key != "requestId" else "other-request"
                self.assertFalse(worker.receipt(bad, copy.deepcopy(row)))
        for key in ("ownGoObserved", "lootWindowObserved", "lootReleaseObserved", "outputInventoryObserved", "leaseReleased"):
            bad = copy.deepcopy(state); bad["operation"][key] = False
            self.assertFalse(worker.receipt(bad, copy.deepcopy(row)), key)
        for changes in (dict(cleanupUnconfirmed=True), dict(lootSlotsRemoved=0), dict(ownLootedItemsObserved=0), dict(observedInventoryRevision="23")):
            bad = copy.deepcopy(state); bad["operation"].update(changes)
            self.assertFalse(worker.receipt(bad, copy.deepcopy(row)), changes)

    def test_existing_profession_lock_prevents_any_harvest_reservation(self):
        worker, rpc, _ = self.rig()
        folder = m.c.ensure_state(m.ROOT/"data/professions/5")
        with os.fdopen(m.c.private_open(folder/"lock", os.O_RDWR|os.O_CREAT), "r+") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX|fcntl.LOCK_NB)
            worker.tick()
        self.assertEqual(worker.data["attempts"], [])
        self.assertFalse(any(method == "buddy-professions.attempt" for method, _ in rpc.calls))

    def test_enabled_idle_junk_intent_does_not_block_but_unsettled_work_and_trade_lease_do(self):
        worker,rpc,_=self.rig()
        rpc.trade.update(junkDisposalEnabled=True,lastJunkDisposal=dict(status='SERVER_SLOT_REMOVAL_CONFIRMED'))
        worker.tick()
        self.assertEqual(sum(method=='buddy-professions.attempt' for method,_ in rpc.calls),1)
        self.assertTrue(rpc.trade['junkDisposalEnabled'])
        self.assertEqual(worker.status['lastReceipt']['status'],'COMPLETED')
        for mode in ('pending','ambiguous','unknown','open_trade','trade_lease'):
            with self.subTest(mode=mode):
                worker,rpc,_=self.rig()
                rpc.trade['junkDisposalEnabled']=True
                if mode=='trade_lease':rpc.actions['interactionOwner']='trade'
                elif mode=='open_trade':rpc.trade['phase']='OPEN'
                else:rpc.trade['lastJunkDisposal']={'status':dict(pending='AWAITING_SERVER_SLOT_UPDATE',ambiguous='TRANSPORT_UNCONFIRMED_NO_RETRY',unknown='UNKNOWN')[mode]}
                worker.deadline=120
                with self.assertRaises(m.Error):worker.read()
                self.assertFalse(any(method in ('buddy-professions.attempt','buddy-actions.acquire_interaction') for method,_ in rpc.calls))

    def test_reported_waits_are_fixed_codes_and_only_exact_completed_proofs_publish_receipt(self):
        for message,reason in (('Harvest yields to observed party combat or healing','combat_or_healing'),
                ('Harvest needs actual known carried items and skinning knife','knife_or_item_metadata'),
                ('private unknown error with credentials','observation_or_cleanup_unavailable')):
            worker,rpc,_=self.rig()
            status=worker.report_wait(m.Error(message))
            self.assertEqual((status['phase'],status['reason']),('waiting_for_harvest',reason))
            self.assertNotIn('credentials',str(status))
            self.assertEqual((status['skillId'],status['spellId']),(393,8613))
        worker,rpc,_=self.rig();worker.tick()
        row=copy.deepcopy(worker.data['attempts'][0]);state=rpc.state()
        self.assertEqual(worker.status['bodySessionId'],'body1')
        receipt=worker.status.pop('lastReceipt')
        for mode in ('unmatched','unconfirmed','missing_inventory','release_pending'):
            with self.subTest(mode=mode):
                bad=copy.deepcopy(state)
                if mode=='unmatched':bad['operation']['requestId']='other'
                elif mode=='unconfirmed':bad['operation']['status']='OUTCOME_UNCONFIRMED'
                elif mode=='missing_inventory':bad['operation']['outputInventoryObserved']=False
                else:bad['operation']['leaseReleased']=False
                self.assertFalse(worker.receipt(bad,copy.deepcopy(row)))
                self.assertNotIn('lastReceipt',worker.status)
        self.assertTrue(worker.receipt(state,copy.deepcopy(row)))
        self.assertEqual(worker.status['lastReceipt']['targetGuid'],receipt['targetGuid'])

    def test_nearby_actual_skinning_wait_reason_is_projected_without_module_free_text(self):
        for raw,expected in list(m.TARGET_REASONS.items())+[('untrusted private error','no_verified_target')]:
            worker,rpc,_=self.rig();rpc.approachable=False
            original=rpc.target_row
            rpc.target_row=lambda:dict(original(),approachReason=raw)
            worker.tick()
            self.assertEqual(worker.status['reason'],expected)
            self.assertNotIn('private',str(worker.status))
            self.assertFalse(any(method=='buddy-professions.attempt' for method,_ in rpc.calls))


if __name__ == "__main__": unittest.main()
