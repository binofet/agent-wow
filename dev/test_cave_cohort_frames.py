"""Complete cave-cohort frames through the accepted-source JSON consumer.

Full accepted actor-source queries use the maintained 1000ms query profile.
Separate checks exercise the SAME shared selector at ordinary 5ms/32KiB.
The coordinator does not use the Actions-only native structpb callback.
"""

import copy
import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

import test_cave_catchup as fixture
from test_starter_objective_policy import BINARY, runtime


ROOT = Path(__file__).resolve().parent.parent
UINT64_MAX = "18446744073709551615"


def creature(index):
    """A distinct, complete observation, including optional trajectory facts."""
    x, y, z = -9990. + index / 2., 60., 8.
    return dict(guid=str((0xf130 << 48) | (475 << 24) | (index + 1)), entry=475,
        present=True, mapKnown=True, mapId=0, positionKnown=True, x=x, y=y, z=z,
        aliveKnown=True, alive=True, healthKnown=True, health=50, maxHealth=50,
        levelKnown=True, level=5, unitFlagsKnown=True, unitFlags=0, unitFlags2=0,
        dynamicFlagsKnown=True, dynamicFlags=0, npcFlags=1, targetKnown=True,
        targetGuid="0", attackVictimKnown=True, attackVictimGuid="0", attackAgeMs="0",
        movementFlags=0, positionAgeMs="0", trajectory=dict(known=True, stopped=True,
            cyclic=False, transportRelative=False, destinationKnown=True, ageMs="0",
            durationMs=100, destinationX=x, destinationY=y, destinationZ=z))


def full_frame(mode="priority"):
    """Project all 96 distinct nearby rows, three requested rows, and 75 slots."""
    party = fixture.Party()
    worker = party.worker()
    scenes = worker.read()
    for actor_index, guid in enumerate(fixture.cave.ROSTER):
        scene = scenes[guid]
        world = scene["world"]
        world.update(visitRevision=UINT64_MAX, selfQuestLogRevision=UINT64_MAX,
                     inventoryRevision=UINT64_MAX)
        scene["questRevision"] = int(UINT64_MAX)
        scene["actions"]["position"]["revision"] = UINT64_MAX
        scene["party"]["group"]["groupGuid"] = UINT64_MAX
        world["nearbyCreatures"] = [creature(actor_index * 32 + i) for i in range(32)]
        world["requestedCreature"] = creature(96 + actor_index)
        for slot, quest in enumerate(world["selfQuestLog"]):
            quest.update(questId=(47, 60, 62)[slot] if slot < 3 else 10000 + actor_index * 25 + slot,
                         state=1 if slot == 24 else 0, complete=slot == 24, failed=False)

    # Preserve exact uint64 witnesses and explicit unknownness within a full
    # frame; these observations must not be rounded, dropped, or made fresh.
    first = scenes[1]["world"]["nearbyCreatures"][0]
    first.update(guid=UINT64_MAX, targetGuid=UINT64_MAX,
                 attackVictimGuid=UINT64_MAX, attackAgeMs=UINT64_MAX,
                 positionAgeMs=UINT64_MAX)
    first["trajectory"]["ageMs"] = UINT64_MAX
    scenes[4]["world"]["nearbyCreatures"][0].update(healthKnown=False, levelKnown=False)
    before = copy.deepcopy(scenes)
    raw_before = json.dumps(scenes, sort_keys=True, allow_nan=False)
    frame = worker.policy_facts(scenes, mode)
    if scenes != before or json.dumps(scenes, sort_keys=True, allow_nan=False) != raw_before:
        raise AssertionError("The actual projector mutated complete observed input")
    return party, worker, scenes, frame


def final_candidate(scenes):
    row = scenes[5]["world"]["nearbyCreatures"][-1]
    row.update(npcFlags=0, x=-9790., y=120., z=8.)
    row["trajectory"].update(destinationX=-9790., destinationY=120., destinationZ=8.)
    return row


class CaveCohortProjectionTests(unittest.TestCase):
    def test_complete_projector_retains_every_observer_slot_and_uint64_witness(self):
        _, worker, scenes, frame = full_frame()
        before = copy.deepcopy(scenes)
        raw_before = json.dumps(scenes, sort_keys=True, allow_nan=False)
        self.assertEqual(frame, worker.policy_facts(scenes, "priority"))
        self.assertEqual(scenes, before)
        self.assertEqual(json.dumps(scenes, sort_keys=True, allow_nan=False), raw_before)
        self.assertEqual(len(frame["creatures"]), 99)
        self.assertEqual(sum(len(a["c"]) for a in frame["actors"].values()), 96)
        self.assertEqual(sum(len(a["q"]) for a in frame["actors"].values()), 75)
        for guid in fixture.cave.ROSTER:
            actor, raw = frame["actors"][str(guid)], scenes[guid]["world"]
            observed = [frame["creatures"][ref - 1] for ref in actor["c"]]
            self.assertEqual(len(observed), 32)
            self.assertEqual([r[0] for r in observed], [r["guid"] for r in raw["nearbyCreatures"]])
            self.assertEqual([r[4:10] for r in observed],
                [[r[k] for k in ("x", "y", "z", "health", "maxHealth", "level")]
                 for r in raw["nearbyCreatures"]])
            self.assertTrue(all(len(r) == 20 and len(r[19]) == 6 for r in observed))
            self.assertEqual(frame["creatures"][actor["r"] - 1][0], raw["requestedCreature"]["guid"])
            self.assertEqual(actor["b"][1:], [UINT64_MAX] * 4)
            self.assertEqual(actor["p"][-1], UINT64_MAX)
            self.assertEqual([q[0] for q in actor["q"]], [q["questId"] for q in raw["selfQuestLog"]])
            self.assertEqual(actor["q"][-1], [raw["selfQuestLog"][-1]["questId"], 1, 1])
        first = frame["creatures"][frame["actors"]["1"]["c"][0] - 1]
        self.assertEqual([first[i] for i in (0, 14, 15, 16, 18)], [UINT64_MAX] * 5)
        self.assertEqual(first[19][1], UINT64_MAX)
        unknown = frame["creatures"][frame["actors"]["4"]["c"][0] - 1]
        self.assertEqual(unknown[2] & (32 | 64), 0)
        self.assertNotEqual(first[2] & (32 | 64), 0)
        self.assertLessEqual(len(runtime.packed(frame, runtime.MAX_INPUT)), 32768)

    def test_pooling_requires_the_entire_observation_not_only_the_guid(self):
        _, worker, scenes, _ = full_frame()
        original = copy.deepcopy(scenes[1]["world"]["nearbyCreatures"][1])
        scenes[4]["world"]["nearbyCreatures"][0] = copy.deepcopy(original)
        shared = worker.policy_facts(scenes, "priority")
        self.assertEqual(len(shared["creatures"]), 98)
        self.assertEqual(shared["actors"]["1"]["c"][1], shared["actors"]["4"]["c"][0])
        scenes[4]["world"]["nearbyCreatures"][0]["positionKnown"] = False
        distinct = worker.policy_facts(scenes, "priority")
        self.assertEqual(len(distinct["creatures"]), 99)
        self.assertNotEqual(distinct["actors"]["1"]["c"][1], distinct["actors"]["4"]["c"][0])
        self.assertEqual(sum(len(a["c"]) for a in distinct["actors"].values()), 96)

    def test_late_raw_malformed_facts_are_refused_instead_of_omitted(self):
        for key, bad in (("healthKnown", "false"), ("guid", str(2**64)), ("guid", "01")):
            with self.subTest(key=key, bad=bad):
                _, worker, scenes, _ = full_frame()
                scenes[5]["world"]["nearbyCreatures"][-1][key] = bad
                before = copy.deepcopy(scenes)
                with self.assertRaises(fixture.cave.Error):
                    worker.policy_facts(scenes, "priority")
                self.assertEqual(scenes, before)


@unittest.skipUnless(BINARY.is_file(), "set BUDDY_LUAPOLICY_TEST_BINARY to the local compiled bridge")
class CaveCohortAcceptedQueryFrameTests(unittest.TestCase):
    def evaluate(self, party, frame):
        before = copy.deepcopy(frame)
        packed = runtime.packed(frame, runtime.MAX_INPUT)
        self.assertLessEqual(len(packed), 32768)
        result, binding = fixture.cave.policy.evaluate_accepted(party.rpcs[1],
            {"sessionId": party.rpcs[1].session, "characterGuid": "1"}, frame)
        self.assertEqual(frame, before)
        self.assertEqual(runtime.packed(frame, runtime.MAX_INPUT), packed)
        self.assertEqual(binding, {"sessionId": party.rpcs[1].session, "characterGuid": "1",
            "revision": party.revision, "generation": str(party.generation)})
        self.assertLessEqual(len(runtime.packed(result, runtime.MAX_INPUT)), 32768)
        return result

    def test_all_96_rows_and_75_slots_use_actual_accepted_export_query_profile(self):
        party, _, scenes, frame = full_frame()
        before = copy.deepcopy(scenes)
        self.assertEqual(self.evaluate(party, frame), {"status": "ready", "choice": {"kind": "none"}})
        self.assertEqual(scenes, before)
        self.assertEqual(party.events, [])

    def test_real_query_command_has_explicit_1000ms_and_unchanged_default_json_bound(self):
        party,_,_,frame=full_frame()
        commands=[];actual_run=subprocess.run
        def record(command,*args,**kwargs):
            commands.append(list(command));return actual_run(command,*args,**kwargs)
        with patch.object(subprocess,'run',side_effect=record):
            self.assertEqual(self.evaluate(party,frame),{'status':'ready','choice':{'kind':'none'}})
        self.assertEqual(len(commands),1)
        command=commands[0]
        self.assertEqual(command[command.index('--execution-ms')+1],'1000')
        self.assertNotIn('--value-bytes',command)
        self.assertEqual(party.events,[])

    def test_query_runaway_still_refuses_before_any_native_action(self):
        party,_,_,frame=full_frame()
        party.source='return {starter_spell=function(input) while true do end end}'
        with self.assertRaisesRegex(ValueError,r'1s configured'):
            fixture.cave.policy.evaluate_accepted(party.rpcs[1],
                {'sessionId':'body-1','characterGuid':'1'},frame)
        self.assertEqual(party.events,[])

    def test_last_observer_last_row_changes_the_actual_priority_decision(self):
        party, worker, scenes, frame = full_frame()
        self.assertEqual(self.evaluate(party, frame), {"status": "ready", "choice": {"kind": "none"}})
        scenes[5]["world"]["nearbyCreatures"][-1].update(attackVictimGuid="4", attackAgeMs="1")
        frame = worker.policy_facts(scenes, "priority")
        self.assertEqual(self.evaluate(party, frame), {"status": "ready",
            "choice": {"kind": "actual_fresh_party_attacker", "observerGuid": "5"}})
        self.assertEqual(len(frame["actors"]["5"]["c"]), 32)
        self.assertEqual(party.events, [])

    def test_final_candidate_and_same_body_source_change_use_the_accepted_revision(self):
        party, worker, scenes, _ = full_frame("candidate")
        row = final_candidate(scenes)
        frame = worker.policy_facts(scenes, "candidate")
        expected = {"status": "ready", "choice": {"kind": "candidate", "targetGuid": row["guid"], "entry": 475}}
        self.assertEqual(self.evaluate(party, frame), expected)
        original, revision, sid = party.source, party.revision, party.rpcs[1].session
        anchor = "entries={[40]=true,[475]=true},idle_polls=3"
        self.assertEqual(original.count(anchor), 1)
        party.source = original.replace(anchor, "entries={[40]=true,[475]=false},idle_polls=3", 1)
        party.generation += 1
        self.assertNotEqual(party.revision, revision)
        self.assertEqual(self.evaluate(party, frame), {"status": "ready", "choice": {"kind": "none"}})
        party.source = original
        party.generation += 1
        self.assertEqual(party.revision, revision)
        self.assertEqual(party.rpcs[1].session, sid)
        self.assertEqual(self.evaluate(party, frame), expected)
        self.assertEqual(party.events, [])

    def test_complete_non_npc_rows_reach_the_actual_encounter_planner(self):
        party,worker,scenes,_=full_frame('encounter')
        for scene in scenes.values():
            for row in scene['world']['nearbyCreatures']:row['npcFlags']=0
        row=final_candidate(scenes)
        frame=worker.policy_facts(scenes,'encounter')
        self.assertEqual(self.evaluate(party,frame),{'status':'ready','choice':{'kind':'arm',
            'targetGuid':row['guid'],'entry':475,'engagerGuid':'4','order':['1','5','4']}})
        self.assertEqual(sum(len(actor['c']) for actor in frame['actors'].values()),96)
        self.assertEqual(sum(len(actor['q']) for actor in frame['actors'].values()),75)
        self.assertEqual(party.events,[])

    def test_late_invalid_requested_row_quest_slot_and_reference_reject_the_complete_frame(self):
        party, _, _, frame = full_frame()
        cases = []
        bad = copy.deepcopy(frame); bad["creatures"][-1][19][-1] = 40000; cases.append(bad)
        bad = copy.deepcopy(frame); bad["actors"]["5"]["q"][-1] = [62, 0]; cases.append(bad)
        bad = copy.deepcopy(frame); bad["actors"]["5"]["c"][-1] = 100; cases.append(bad)
        bad = copy.deepcopy(frame); bad["creatures"][-1][0] = "01"; cases.append(bad)
        bad = copy.deepcopy(frame); bad["creatures"][-1][0] = str(2**64); cases.append(bad)
        for index, malformed in enumerate(cases):
            with self.subTest(case=index):
                self.assertEqual(self.evaluate(party, malformed), {"status": "invalid"})
        self.assertEqual(party.events, [])


@unittest.skipUnless(BINARY.is_file(), "set BUDDY_LUAPOLICY_TEST_BINARY to the local compiled bridge")
class CaveCohortSelectorDefaultFrameTests(unittest.TestCase):
    """Shared-selector proof, explicitly not full-bundle 5ms qualification."""
    def compare(self,party,frame,want):
        source=(ROOT/'behaviors/starter-objectives.lua').read_text()
        before=copy.deepcopy(frame);raw=runtime.packed(frame,runtime.MAX_INPUT)
        self.assertLessEqual(len(raw),32768)
        commands=[];actual_run=subprocess.run
        def record(command,*args,**kwargs):
            commands.append(list(command));return actual_run(command,*args,**kwargs)
        with patch.object(subprocess,'run',side_effect=record):
            actual=runtime.Bridge(BINARY,ROOT/'modules').evaluate(source,frame)
        self.assertEqual(len(commands),1)
        self.assertNotIn('--execution-ms',commands[0]);self.assertNotIn('--value-bytes',commands[0])
        self.assertEqual(actual,want)
        accepted,binding=fixture.cave.policy.evaluate_accepted(party.rpcs[1],
            {'sessionId':'body-1','characterGuid':'1'},frame)
        self.assertEqual(actual,accepted)
        self.assertEqual(binding['revision'],party.revision)
        self.assertEqual(frame,before);self.assertEqual(runtime.packed(frame,runtime.MAX_INPUT),raw)
        self.assertEqual(party.events,[])

    def test_full_99_75_priority_and_last_attacker_match_full_source_query(self):
        party,worker,scenes,frame=full_frame()
        self.compare(party,frame,{'status':'ready','choice':{'kind':'none'}})
        scenes[5]['world']['nearbyCreatures'][-1].update(attackVictimGuid='4',attackAgeMs='1')
        self.compare(party,worker.policy_facts(scenes,'priority'),{'status':'ready',
            'choice':{'kind':'actual_fresh_party_attacker','observerGuid':'5'}})

    def test_full_non_npc_encounter_choice_uses_same_shared_selector_at_default(self):
        party,worker,scenes,_=full_frame('encounter')
        for scene in scenes.values():
            for row in scene['world']['nearbyCreatures']:row['npcFlags']=0
        row=final_candidate(scenes)
        self.compare(party,worker.policy_facts(scenes,'encounter'),{'status':'ready',
            'choice':{'kind':'arm','targetGuid':row['guid'],'entry':475,'engagerGuid':'4','order':['1','5','4']}})

    def test_late_invalid_complete_rows_and_slots_fail_at_default_and_query_profiles(self):
        party,_,_,frame=full_frame()
        cases=[]
        bad=copy.deepcopy(frame);bad['creatures'][-1][19][-1]=40000;cases.append(bad)
        bad=copy.deepcopy(frame);bad['actors']['5']['q'][-1]=[62,0];cases.append(bad)
        bad=copy.deepcopy(frame);bad['actors']['5']['c'][-1]=100;cases.append(bad)
        for bad in cases:self.compare(party,bad,{'status':'invalid'})


if __name__ == "__main__":
    unittest.main()
