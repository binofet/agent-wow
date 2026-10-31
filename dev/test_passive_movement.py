"""Passive aura flags use normal ground guards; all game operations are fake."""
import copy
import importlib.machinery
import importlib.util
from pathlib import Path
import unittest


def load(filename):
    loader=importlib.machinery.SourceFileLoader('passive_'+filename.replace('.','_'),str(Path(__file__).with_name(filename)))
    spec=importlib.util.spec_from_loader(loader.name,loader)
    value=importlib.util.module_from_spec(spec);loader.exec_module(value);return value


PASSIVE=(0x10000000,0x20000000,0x40000000,0x70000000)
UNSAFE=(0x200,0x1000,0x2000,0x200000,0x1000000,0x2000000,0x4000000,0x80000000)


class PassiveMovementTests(unittest.TestCase):
    def fixture(self,name,cls):
        module=load(name);case=getattr(module,cls)('runTest')
        if hasattr(case,'setUp'):case.setUp()
        self.addCleanup(case.doCleanups)
        return module,case

    def test_shared_quest_scene_allows_only_passive_ground_modes(self):
        t=load('test_converse.py');a,w=t.QuestApproachCandidateTests.safe_scene()
        for flags in PASSIVE:
            w['self']['movementFlags']=flags
            self.assertTrue(t.converse.BuddyControls.quest_scene_safe(a,w,1))
            for bad in UNSAFE:
                w['self']['movementFlags']=flags|bad
                self.assertFalse(t.converse.BuddyControls.quest_scene_safe(a,w,1),(flags,bad))
        w['self'].update(movementFlags=0x70000000,extraMovementFlags=1)
        self.assertFalse(t.converse.BuddyControls.quest_scene_safe(a,w,1))
        w['self']['extraMovementFlags']=0;a['motionUnconfirmed']=True
        self.assertFalse(t.converse.BuddyControls.quest_scene_safe(a,w,1))

    def test_nearby_giver_keeps_distance_freshness_and_unsupported_mode_gates(self):
        t=load('test_converse.py');case=t.QuestApproachCandidateTests('runTest');a,w=case.safe_scene()
        for flags in PASSIVE:
            row=case.candidate();row['creature']['movementFlags']=flags
            self.assertTrue(t.converse.BuddyControls.quest_candidates([row],a,w,1))
            for bad in UNSAFE:
                row['creature']['movementFlags']=flags|bad
                self.assertEqual(t.converse.BuddyControls.quest_candidates([row],a,w,1),[])

    def test_town_still_rejects_real_movement_uncertainty_and_unsafe_modes(self):
        t,case=self.fixture('test_party_town.py','TownTests');actor,rpc,*_=case.rig()
        context=actor.read()
        for flags in PASSIVE:
            context['self']['movementFlags']=flags
            actor.safe(context)
            for bad in UNSAFE:
                context['self']['movementFlags']=flags|bad
                with self.assertRaises(t.m.Error):actor.safe(context)
        context['self']['movementFlags']=0x70000000;context['actions']['moving']=True
        with self.assertRaises(t.m.Error):actor.safe(context)

    def test_harvest_passive_self_and_owner_can_complete_but_unsafe_owner_cannot_cast(self):
        t,case=self.fixture('test_party_harvest.py','HarvestTests')
        for flags in PASSIVE:
            worker,rpc,_=case.rig();rpc.own['movementFlags']=rpc.owner['movementFlags']=flags
            worker.tick()
            self.assertTrue(any(method=='buddy-professions.attempt' for method,_ in rpc.calls))
        for bad in UNSAFE:
            worker,rpc,_=case.rig();rpc.owner['movementFlags']=0x70000000|bad
            with self.assertRaises(t.m.Error):worker.tick()
            self.assertFalse(any(method=='buddy-professions.attempt' for method,_ in rpc.calls))

    def test_maintenance_existing_stationary_mask_accepts_passive_without_toggle_changes(self):
        t,case=self.fixture('test_party_maintenance.py','MaintenanceTests')
        for flags in PASSIVE:
            case.rpc.world['self']['movementFlags']=flags
            case.rpc.world['owner']['movementFlags']=flags
            case.tick()
        self.assertTrue(any(method=='buddy-training.buy' for method,_ in case.rpc.calls))
        self.assertFalse(any(method in ('buddy-actions.follow','buddy-actions.healing','buddy-actions.assist')
            for method,_ in case.rpc.calls))

    def test_cave_search_keeps_physical_controller_and_unsupported_mode_guards(self):
        t=load('test_cave_catchup.py');party=t.Party();worker=party.worker();scenes=worker.read()
        worker.data['controls']=worker.controls(scenes)
        for flags in PASSIVE:
            for scene in scenes.values():scene['self']['movementFlags']=flags
            self.assertTrue(worker.safe_search(scenes))
            target=copy.deepcopy(party.rpcs[1].target);target['movementFlags']=flags
            self.assertIsNotNone(t.cave.creature_position(target))
            for bad in UNSAFE:
                scenes[1]['self']['movementFlags']=flags|bad
                self.assertFalse(worker.safe_search(scenes))
                target['movementFlags']=flags|bad
                self.assertIsNone(t.cave.creature_position(target))
                scenes[1]['self']['movementFlags']=flags

    def test_stationary_area_profession_and_turnin_npcs_only_gain_passive_bits(self):
        area,case=self.fixture('test_area_quests.py','AreaTests');actor,rpc,*_=case.rig()
        context=actor.read();actor.anchor=copy.deepcopy(context['actions']['position'])
        baseline=len(actor.givers(context));self.assertGreater(baseline,0)
        for flags in PASSIVE:
            for row in context['quests']['nearby']:row['creature']['movementFlags']=flags
            self.assertEqual(len(actor.givers(context)),baseline)
        for row in context['quests']['nearby']:row['creature']['movementFlags']=0x70000001
        self.assertEqual(actor.givers(context),[])
        professions,case=self.fixture('test_professions.py','ProfessionTests');actor,rpc,*_=case.rig()
        context=actor.read();baseline=len(actor.candidates(context));self.assertGreater(baseline,0)
        for flags in PASSIVE:
            for row in context['nearby']:row['creature']['movementFlags']=flags
            self.assertEqual(len(actor.candidates(context)),baseline)
        for row in context['nearby']:row['creature']['movementFlags']=0x70000001
        self.assertEqual(actor.candidates(context),[])
        follow=load('test_cave_followthrough.py');rpc=follow.FakeRPC(complete62=True)
        actor=follow.h.Actor(follow.h.ROSTER[1],rpc);world={'requestedCreatures':rpc.npcs}
        for flags in PASSIVE:
            for row in rpc.npcs:row['movementFlags']=flags
            self.assertEqual(actor.giver(world,rpc.actions,241)[2]['entry'],241)
        for row in rpc.npcs:row['movementFlags']=0x70000001
        with self.assertRaises(follow.h.Error):actor.giver(world,rpc.actions,241)

    def test_regional_known_giver_memory_keeps_original_bounds_and_stationary_gate(self):
        t,case=self.fixture('test_sync_quests.py','SyncTests')
        for flags in PASSIVE:
            actor,rpc,*_=case.rig();state=rpc.state()
            for row in state['nearby']:
                row['creature'].update(present=True,aliveKnown=True,alive=True,mapKnown=True,positionKnown=True,
                    npcFlags=2,movementFlags=flags,x=-8900.,y=-100.,z=60.)
            actor.remember_observed_givers(state,rpc.session)
            self.assertEqual(len(actor.data['knownGivers']),3)
        actor,rpc,*_=case.rig();state=rpc.state()
        for row in state['nearby']:
            row['creature'].update(present=True,aliveKnown=True,alive=True,mapKnown=True,positionKnown=True,
                npcFlags=2,movementFlags=0x70000001,x=-8900.,y=-100.,z=60.)
        actor.remember_observed_givers(state,rpc.session)
        self.assertEqual(actor.data['knownGivers'],{})


if __name__=='__main__':unittest.main()
