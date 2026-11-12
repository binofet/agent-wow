"""Mock-only fixed-party encounter, inventory and custody checks."""
import copy
import importlib.machinery
import importlib.util
import json
from pathlib import Path
import tempfile
import threading
import unittest

loader = importlib.machinery.SourceFileLoader('tested_cave_catchup', str(Path(__file__).with_name('cave-catchup')))
spec = importlib.util.spec_from_loader(loader.name, loader)
cave = importlib.util.module_from_spec(spec); loader.exec_module(cave)
TARGET = 0xf1300001db000001  # Actual encoded entry475, arbitrary observed spawn fixture.


class Party:
    def __init__(self):
        self.time = 0.; self.events = []; self.saved = []; self.hook = None; self.lost_reply = None
        self.rpcs = {g: Actor(self, g) for g in cave.ROSTER}

    def worker(self, stored=None, stop=lambda: False, seconds=600):
        return cave.Catchup(self.rpcs, {g: a.session for g, a in self.rpcs.items()},
            save=lambda value: self.saved.append(copy.deepcopy(value)), stored=stored,
            now=lambda: self.time, stopped=stop, max_seconds=seconds)

    def kill(self):
        for actor in self.rpcs.values():
            actor.target['alive'], actor.target['health'] = False, 0
            actor.target['dynamicFlags'] = actor.target.get('dynamicFlags', 0) | 8
            actor.actions['fargodeep']['active'] = False
            actor.self['unitFlags'] &= ~0x80000


class Actor:
    def __init__(self, party, guid):
        self.party, self.guid, self.session = party, guid, f'body-{guid}'
        self.calls = []
        self.self = dict(guid=str(guid), present=True, mapKnown=True, mapId=0, positionKnown=True,
            x=-9800., y=120., z=8., aliveKnown=True, alive=True, healthKnown=True, health=100,
            maxHealth=100, movementFlagsKnown=True, classKnown=True, classId=cave.ROSTER[guid][2], unitFlagsKnown=True,
            unitFlags=8, levelKnown=True, level=6, powerKnown=True, powerType=3 if guid==5 else 0, power=100, maxPower=100)
        self.actions = dict(position=dict(known=True, mapKnown=True, mapId=0, x=-9800., y=120., z=8.),
            healing=guid==1, assisting=True, following=False)
        self.target = dict(guid=str(TARGET), entry=475, present=True, mapKnown=True, mapId=0, positionKnown=True,
            x=-9790., y=120., z=8., aliveKnown=True, alive=True, healthKnown=True, health=50, maxHealth=50,
            levelKnown=True, level=5, unitFlagsKnown=True, dynamicFlagsKnown=True, targetKnown=True, positionRevision='1')
        self.logs = [dict(slot=i, questId=(47,60,62)[i] if i<3 else 0, state=0,
            complete=False, failed=False, objectiveCounts=[0,0,0,0]) for i in range(25)]
        self.bags = [dict(bag=0, slotKnown=True, contentsKnown=True, slotCount=16,
            slots=[dict(bag=0,slot=i,itemGuid='0') for i in range(16)])]
        self.bags += [dict(bag=i, slotKnown=True, containerGuid='0', slotCount=0, slots=[]) for i in range(1,5)]
        self.loot = dict(sessionId=self.session, enabled=True, phase='idle')
        self.trade = dict(sessionId=self.session, phase='IDLE')
        self.professions = dict(sessionId=self.session, selfGuid=str(guid), active=False)
        self.group = dict(known=True,inGroup=True,groupGuid='99',members=[dict(guid=str(g),online=True,self=g==guid) for g in cave.ROSTER]
            +[dict(guid='3',online=False,name='Joel')])
        self.lootable = []

    def world(self, params):
        value = dict(self=copy.deepcopy(self.self),selfQuestLogKnown=True,selfQuestLogRevision='1',
            selfQuestLog=copy.deepcopy(self.logs),inventoryKnown=True,inventoryRevision='1',inventoryBags=copy.deepcopy(self.bags),
            nearbyCreatures=[copy.deepcopy(self.target)],lootableCorpses=copy.deepcopy(self.lootable))
        if params.get('creatureGuid') == self.target['guid']: value['requestedCreature']=copy.deepcopy(self.target)
        return value

    def call(self, method, params):
        self.calls.append((method,copy.deepcopy(params)))
        if self.party.hook: self.party.hook(self,method,params)
        if method=='buddy-party.state': return dict(sessionId=self.session,group=copy.deepcopy(self.group))
        if method=='buddy-world.snapshot': return self.world(params)
        if method=='buddy-actions.status': return copy.deepcopy(self.actions)
        if method=='buddy-loot.state': return copy.deepcopy(self.loot)
        if method=='buddy-quests.state': return dict(sessionId=self.session,actionInFlight=False)
        if method=='buddy-trade.state': return copy.deepcopy(self.trade)
        if method=='buddy-professions.state': return copy.deepcopy(self.professions)
        if method=='buddy-actions.fargodeep_encounter':
            self.party.events.append((self.guid,copy.deepcopy(params)))
            if params['sessionId']!=self.session: raise cave.Error('wrong body')
            if params['enabled']:
                latest=self.party.saved[-1]['encounters'][-1]
                assert latest['requests'][str(self.guid)]['phase']=='reserved', 'No durable prewrite reservation'
                if int(params['engagerGuid'])==self.guid:
                    assert all(a.actions.get('fargodeep',{}).get('active') is True for g,a in self.party.rpcs.items() if g!=self.guid), 'Pull before supports'
                self.actions['fargodeep']=dict(active=True,targetGuid=params['targetGuid'],engagerGuid=params['engagerGuid'],phase='armed',remainingMs='120000')
                if self.party.lost_reply==self.guid: raise cave.Error('lost activation response')
            else:
                state=self.actions.get('fargodeep',{})
                if state.get('targetGuid')!=params['targetGuid'] or state.get('engagerGuid')!=params['engagerGuid']: raise cave.Error('wrong encounter')
                state['active']=False
            return copy.deepcopy(self.actions)
        raise AssertionError(method)

    def items(self, dust, candles):
        for slot, entry, count in ((0,773,dust),(1,772,candles)):
            self.bags[0]['slots'][slot]=dict(bag=0,slot=slot,itemGuid=str(0x4000000000010000+self.guid*10+slot),entry=entry,
                stackCount=count,metadataKnown=True) if count else dict(bag=0,slot=slot,itemGuid='0')

    def complete(self, qid):
        for row in self.logs:
            if row['questId']==qid: row.update(complete=True,state=1)


class CatchupTests(unittest.TestCase):
    def test_supports_arm_before_engager_and_only_bound_rpc_writes(self):
        p=Party(); w=p.worker(); w.tick()
        self.assertEqual([g for g,r in p.events if r['enabled']], [1,5,4])
        self.assertEqual(w.data['encounters'][0]['phase'],'active')
        self.assertEqual(w.status['phase'],'waiting_for_actual_combat_and_loot')
        self.assertFalse(w.done)
        for guid,request in p.events:
            self.assertEqual(request['sessionId'],p.rpcs[guid].session)
            self.assertEqual(request['targetGuid'],str(TARGET))
            self.assertNotIn('x',request);self.assertNotIn('spellId',request)

    def test_offline_joel_allowed_but_actual_three_party_required(self):
        p=Party(); w=p.worker(); w.tick();self.assertEqual(len(p.events),3)
        for mode in ('missing','offline','different_group'):
            p=Party();w=p.worker()
            if mode=='missing':p.rpcs[4].group['members']=p.rpcs[4].group['members'][1:]
            if mode=='offline':p.rpcs[4].group['members'][0]['online']=False
            if mode=='different_group':p.rpcs[4].group['groupGuid']='100'
            with self.assertRaises(cave.Error):w.tick()
            self.assertEqual(p.events,[])

    def test_no_synthetic_quest_credit_from_kill_or_log_objective_counters(self):
        p=Party();w=p.worker();w.tick();p.kill();w.tick();p.time+=3;w.tick()
        self.assertFalse(w.done)
        for actor in p.rpcs.values():
            for row in actor.logs:row['objectiveCounts']=[65535]*4
            actor.items(9,7)
        w.tick()
        self.assertNotEqual(w.status['phase'],'own_collection_quests_complete_for_all_three')
        self.assertEqual([(r['goldDust'],r['largeCandles']) for r in w.status['characters']],[(9,7)]*3)

    def test_completion_requires_every_own_flag_and_known_all_bag_counts(self):
        p=Party();w=p.worker()
        for actor in p.rpcs.values():actor.items(10,8);actor.complete(47);actor.complete(60)
        w.tick();self.assertTrue(w.done);self.assertEqual(p.events,[])
        self.assertTrue(all(r['quest47Complete'] and r['quest60Complete'] for r in w.status['characters']))
        self.assertFalse(any(r['quest62Complete'] for r in w.status['characters']))
        p=Party();w=p.worker();p.rpcs[4].bags[0]['contentsKnown']=False
        with self.assertRaises(cave.Error):w.tick()
        self.assertEqual(p.events,[])

    def test_ambiguous_arming_reserved_before_write_and_never_replayed(self):
        p=Party();p.lost_reply=4;w=p.worker();w.tick()
        self.assertTrue(w.done)
        self.assertEqual([g for g,r in p.events if r['enabled']],[1,5,4])
        saved=copy.deepcopy(w.data);p.events=[];p.lost_reply=None
        resumed=p.worker(stored=saved);resumed.tick()
        self.assertTrue(resumed.done)
        self.assertFalse(any(r['enabled'] for _,r in p.events))

    def test_reserved_process_restart_and_body_change_never_new_pull(self):
        p=Party();w=p.worker();w.tick();saved=copy.deepcopy(w.data)
        saved['encounters'][0]['phase']='arming'
        for request in saved['encounters'][0]['requests'].values():request['phase']='reserved'
        p.events=[];restored=p.worker(stored=saved);restored.tick()
        self.assertTrue(restored.done);self.assertFalse(any(r['enabled'] for _,r in p.events))
        p=Party();w=p.worker();p.rpcs[4].session='new-body'
        with self.assertRaises(cave.Error):w.tick()
        self.assertTrue(w.done);self.assertEqual(p.events,[])

    def test_stop_cleanup_only_exact_body_target_and_engager(self):
        p=Party();stop=[False];w=p.worker(stop=lambda:stop[0]);w.tick()
        p.rpcs[4].actions['fargodeep']['targetGuid']=str(TARGET+1)
        p.events=[];stop[0]=True;w.tick()
        self.assertTrue(w.done);self.assertEqual(w.status['phase'],'cleanup_unconfirmed')
        self.assertEqual([g for g,r in p.events if not r['enabled']],[1,5])
        self.assertTrue(p.rpcs[4].actions['fargodeep']['active'])

    def test_dead_target_then_each_actual_own_loot_before_next_pull(self):
        p=Party();w=p.worker();w.tick();p.kill()
        p.rpcs[4].lootable=[copy.deepcopy(p.rpcs[4].target)]
        w.tick();p.time+=10;w.tick()
        self.assertEqual(w.status['phase'],'waiting_for_each_own_server_permitted_loot')
        self.assertEqual(len([r for _,r in p.events if r['enabled']]),3)
        p.time+=30;w.tick();self.assertTrue(w.done)
        self.assertEqual(w.status['phase'],'loot_uncollected_or_cleanup_unconfirmed_no_new_pull')

    def test_retained_bound_death_recovers_missed_short_corpse_without_fake_credit_or_rearming(self):
        for resumed in (False, True):
            p=Party();w=p.worker();w.tick();row=w.data['encounters'][0];old_requests=copy.deepcopy(row['requests'])
            for actor in p.rpcs.values():
                actor.actions['fargodeep'].update(active=False,phase='target_death_observed')
                original=actor.world
                def missing(params,original=original):
                    value=original(params);value.pop('requestedCreature',None);value['nearbyCreatures']=[];return value
                actor.world=missing
            if resumed:
                row['phase']='unconfirmed'
                # One actual participant's retained receipt is sufficient;
                # other participants need not retain a matching death phase.
                p.rpcs[1].actions['fargodeep']['phase']='target_position_unavailable_or_outside_range'
                p.rpcs[5].actions['fargodeep']['phase']='navigation_step_failed'
                w=p.worker(stored=copy.deepcopy(w.data));row=w.data['encounters'][0]
            p.events=[];w.tick()
            self.assertTrue(row['deadObserved']);self.assertEqual(row['phase'],'loot')
            self.assertEqual(row['deathObservation']['source'],'actions_retained_bound_death')
            self.assertEqual(row['deathObservation']['targetGuid'],str(TARGET))
            self.assertFalse(w.done);self.assertEqual(p.events,[])
            p.time+=3;w.tick()
            self.assertEqual(row['phase'],'settled');self.assertFalse(w.done)
            self.assertTrue(all(not r['quest47Complete'] and not r['quest60Complete'] and r['goldDust']==r['largeCandles']==0 for r in w.status['characters']))
            self.assertEqual(set(row['requests']),set(old_requests));self.assertEqual(p.events,[])

    def test_retained_death_requires_exact_target_engager_and_same_body(self):
        for mode in ('wrong_target','wrong_engager','not_death','still_active','body_changed'):
            p=Party();w=p.worker();w.tick();row=w.data['encounters'][0]
            for actor in p.rpcs.values():
                actor.actions['fargodeep'].update(active=False,phase='target_death_observed')
                if mode=='wrong_target':actor.actions['fargodeep']['targetGuid']=str(TARGET+1)
                if mode=='wrong_engager':actor.actions['fargodeep']['engagerGuid']='5'
                if mode=='not_death':actor.actions['fargodeep']['phase']='navigation_unavailable'
                if mode=='still_active':actor.actions['fargodeep']['active']=True
                original=actor.world
                def missing(params,original=original):
                    value=original(params);value.pop('requestedCreature',None);value['nearbyCreatures']=[];return value
                actor.world=missing
            p.events=[]
            if mode=='body_changed':
                p.rpcs[4].session='replacement-body'
                with self.assertRaises(cave.Error):w.tick()
            else:w.tick()
            self.assertFalse(row['deadObserved'],mode)
            self.assertNotIn('deathObservation',row)
            self.assertFalse(any(request['enabled'] for _,request in p.events),mode)

    def test_human_control_change_or_death_yields_without_reenable(self):
        for mode in ('follow','assist','loot','dead'):
            p=Party();w=p.worker();w.tick();p.events=[]
            if mode=='follow':p.rpcs[4].actions['following']=True
            if mode=='assist':p.rpcs[4].actions['assisting']=False
            if mode=='loot':p.rpcs[4].loot['enabled']=False
            if mode=='dead':p.rpcs[4].self['alive']=False;p.rpcs[4].self['health']=0
            w.tick();self.assertTrue(w.done)
            self.assertFalse(any(r['enabled'] for _,r in p.events))

    def test_unhealthy_busy_unknown_items_and_other_encounter_do_not_pull(self):
        for mode in ('hp','impossible_hp','mana','energy','wrong_power','lease','trade','professions','healing','other_encounter'):
            p=Party();w=p.worker();a=p.rpcs[1]
            if mode=='hp':a.self['health']=94
            if mode=='impossible_hp':a.self['health']=101
            if mode=='mana':a.self['power']=59
            if mode=='energy':p.rpcs[5].self['power']=89
            if mode=='wrong_power':p.rpcs[5].self['powerType']=0
            if mode=='lease':a.actions['interactionOwner']='trade'
            if mode=='trade':a.trade['phase']='OPEN'
            if mode=='professions':a.professions['active']=True
            if mode=='healing':a.actions['healing']=False
            if mode=='other_encounter':a.actions['fargodeep']=dict(active=True,targetGuid=str(TARGET+1),engagerGuid='5')
            w.tick();self.assertEqual(p.events,[],mode)

    def test_candidate_uses_only_observed_permitted_entry_and_stays_bounded(self):
        for mode in ('caster','named','hurt','unknown_level','too_high','far','unknown_position','unknown_flags','claimed','selected','moving','spline'):
            p=Party();w=p.worker()
            for a in p.rpcs.values():
                if mode=='caster':a.target['entry']=476;a.target['guid']=str(0xf1300001dc000001)
                if mode=='named':a.target['entry']=327;a.target['guid']=str(0xf130000147000001)
                if mode=='hurt':a.target['health']=49
                if mode=='unknown_level':a.target['levelKnown']=False
                if mode=='too_high':a.self['level']=3
                if mode=='far':a.target['x']+=40
                if mode=='unknown_position':a.target['positionKnown']=False
                if mode=='unknown_flags':a.target['unitFlagsKnown']=False
                if mode=='claimed':a.target['dynamicFlags']=4
                if mode=='selected':a.target['targetGuid']='99'
                if mode=='moving':a.target['movementFlags']=1
                if mode=='spline':a.target['trajectory']=dict(known=True,stopped=False)
            w.tick();self.assertEqual(p.events,[],mode)

    def test_unisolated_target_waits_without_consuming_or_inventing_a_target(self):
        p=Party();w=p.worker()
        for actor in p.rpcs.values():
            original=actor.world
            def with_add(params, original=original):
                result=original(params);other=copy.deepcopy(result['nearbyCreatures'][0])
                other['guid']=str(TARGET+1);other['x']+=7
                result['nearbyCreatures'].append(other);return result
            actor.world=with_add
        w.tick();self.assertEqual(p.events,[]);self.assertEqual(w.data['encounters'],[])
        self.assertEqual(w.status['phase'],'waiting_for_actual_nearby_isolated_kobold')

    def test_fixed_endpoint_and_corrupt_saved_time_rejected(self):
        p=Party();p.rpcs[4].url='http://127.0.0.1:8088/rpc'
        with self.assertRaises(cave.Error):p.worker()
        p=Party();w=p.worker();w.tick();saved=copy.deepcopy(w.data)
        for value in ('wrong',float('nan'),-1,True):
            invalid=copy.deepcopy(saved);invalid['encounters'][0]['started']=value
            with self.assertRaises(cave.Error):p.worker(stored=invalid)

    def test_finite_budget_journal_identity_and_private_bounded_storage(self):
        p=Party();w=p.worker(seconds=1);p.time=2;w.tick();self.assertTrue(w.done);self.assertEqual(p.events,[])
        with self.assertRaises(cave.Error):p.worker(seconds=601)
        w=p.worker();bad=copy.deepcopy(w.data);bad['identity']['questIds']=[999]
        with self.assertRaises(cave.Error):p.worker(stored=bad)
        with tempfile.TemporaryDirectory() as tmp:
            path=Path(tmp)/'ledger.json';cave.write_private(path,w.data)
            self.assertEqual(cave.read_private(path),w.data)
            self.assertEqual(path.stat().st_mode & 0o777,0o600)
            path.write_bytes(b' '*(cave.MAX_LEDGER+1))
            with self.assertRaises(cave.Error):cave.read_private(path)
        with self.assertRaises(ValueError):cave.decode(b'{"x":1,"x":2}')
        with self.assertRaises(ValueError):cave.decode(b'{"x":NaN}')


    def test_ordinary_patrol_uses_fresh_header_or_recent_endpoint_before_arming(self):
        for age in (500, 4000):
            p=Party();w=p.worker()
            for actor in p.rpcs.values():
                row=actor.target
                row.update(movementFlags=0x08000101,positionAgeMs=age)
                row['trajectory']=dict(known=True,durationMs=3000,ageMs=age,destinationKnown=True,
                    destinationX=-9790.,destinationY=120.,destinationZ=8.)
                if age>3000:row['x']=-9840. # Stale raw XYZ would incorrectly fail the28yd gate.
            scenes=w.read();candidate=w.candidate(scenes)
            self.assertIsNotNone(candidate);self.assertEqual(candidate['x'],-9790.)
            self.assertEqual(p.rpcs[1].target['x'],-9840. if age>3000 else -9790.)
            w.tick();self.assertEqual([g for g,r in p.events if r['enabled']],[1,5,4])

    def test_unknown_unsupported_or_stale_patrol_never_consumes_target(self):
        for mode in ('midleg','stale','cyclic','transport','destination','zero_duration','long_duration','flying','long_route','far_endpoint'):
            p=Party();w=p.worker()
            for actor in p.rpcs.values():
                row=actor.target;row.update(movementFlags=0x08000001,positionAgeMs=4000)
                t=dict(known=True,durationMs=3000,ageMs=4000,destinationKnown=True,
                    destinationX=-9790.,destinationY=120.,destinationZ=8.)
                row['trajectory']=t
                if mode=='midleg':t['ageMs']=1000;row['positionAgeMs']=1000
                if mode=='stale':t['ageMs']=8001
                if mode=='cyclic':t['cyclic']=True
                if mode=='transport':t['transportRelative']=True
                if mode=='destination':t['destinationKnown']=False
                if mode=='zero_duration':t['durationMs']=0
                if mode=='long_duration':t['durationMs']=120001
                if mode=='flying':row['movementFlags']|=0x2000000
                if mode=='long_route':t['destinationX']+=70
                if mode=='far_endpoint':t['destinationX']+=30
            w.tick();self.assertEqual(p.events,[],mode);self.assertEqual(w.data['encounters'],[],mode)

    def test_patrol_isolation_uses_current_endpoint_not_stale_header(self):
        for near in (True,False):
            p=Party();w=p.worker()
            for actor in p.rpcs.values():
                original=actor.world
                def with_patrol(params,original=original):
                    result=original(params);other=copy.deepcopy(result['nearbyCreatures'][0]);other['guid']=str(TARGET+1)
                    other.update(x=-9830. if near else -9788.,movementFlags=0x08000001,positionAgeMs=4000)
                    other['trajectory']=dict(known=True,durationMs=3000,ageMs=4000,destinationKnown=True,
                        destinationX=-9788. if near else -9830.,destinationY=120.,destinationZ=8.)
                    result['nearbyCreatures'].append(other);return result
                actor.world=with_patrol
            # Ignore the other NPC as a candidate to isolate neighbor behavior.
            w.data['encounters'].append(dict(target=TARGET+1))
            candidate=w.candidate(w.read())
            self.assertEqual(candidate is None,near)

    def test_unknown_midleg_neighbor_is_not_treated_as_absent(self):
        p=Party();row=copy.deepcopy(p.rpcs[1].target)
        row.update(x=-9780.,movementFlags=0x08000001,positionAgeMs=1500)
        row['trajectory']=dict(known=True,durationMs=3000,ageMs=1500,destinationKnown=True,
            destinationX=-9795.,destinationY=120.,destinationZ=8.)
        self.assertIsNone(cave.creature_position(row))
        self.assertTrue(cave.neighbor_near(row,p.rpcs[1].target))
        row['x']=-9750.;row['trajectory']['destinationX']=-9740.
        self.assertFalse(cave.neighbor_near(row,p.rpcs[1].target))


    def test_caster_priority_changes_only_after_own_collection_completion(self):
        for complete,expected in (((),4),((4,),1),((4,1),5)):
            p=Party();w=p.worker()
            for guid in complete:
                for qid in cave.GOALS:p.rpcs[guid].complete(qid)
            w.tick();row=w.data['encounters'][0]
            self.assertEqual(row['engager'],expected)
            self.assertEqual([g for g,r in p.events if r['enabled']][-1],expected)

    def test_terminal_engager_cleans_supports_immediately_and_tries_different_guid(self):
        p=Party();w=p.worker();w.tick();row=w.data['encounters'][0]
        p.rpcs[4].actions['fargodeep'].update(active=False,phase='navigation_unavailable');p.events=[]
        w.tick()
        self.assertFalse(w.done);self.assertEqual(row['phase'],'stopped');self.assertFalse(row['deadObserved'])
        self.assertEqual(row['abandonment']['kind'],'outcome_unknown_target_quarantined')
        self.assertEqual([g for g,r in p.events if not r['enabled']],[1,5]);self.assertEqual(p.time,0.)
        self.assertFalse(any(r['enabled'] for _,r in p.events))
        old=copy.deepcopy(row)
        for actor in p.rpcs.values():actor.target['guid']=str(TARGET+1)
        w.tick()
        self.assertEqual(w.data['encounters'][0],old)
        self.assertEqual([int(r['targetGuid']) for _,r in p.events if r['enabled']],[TARGET+1]*3)
        self.assertEqual(w.data['encounters'][1]['phase'],'active')

    def test_terminal_failure_cannot_advance_before_actual_safe_cleanup(self):
        for mode in ('cast','autoattack','motion','lease','loot','profession','combat','owner','human_stop','foreign_active','body'):
            p=Party();w=p.worker();w.tick();row=w.data['encounters'][0]
            p.rpcs[4].actions['fargodeep'].update(active=False,phase='navigation_unavailable');a=p.rpcs[1]
            if mode=='cast':a.actions['castPending']=True
            if mode=='autoattack':a.actions['autoAttackRequested']=True
            if mode=='motion':a.actions['motionUnconfirmed']=True
            if mode=='lease':a.actions['interactionOwner']='quests'
            if mode=='loot':a.loot.update(phase='waiting_release',pendingCorpseGuid=str(TARGET))
            if mode=='profession':a.professions['active']=True
            if mode=='combat':a.self['unitFlags']|=0x80000
            if mode=='owner':
                original=a.world
                def with_owner(params,original=original):
                    value=original(params);value['owner']=dict(guid='3',present=True);return value
                a.world=with_owner
            if mode=='human_stop':a.actions['fargodeep'].update(active=False,phase='explicit_follow_stop')
            if mode=='foreign_active':a.actions['fargodeep']['targetGuid']=str(TARGET+1)
            if mode=='body':a.session='replaced-body'
            p.events=[]
            if mode=='body':
                with self.assertRaises(cave.Error):w.tick()
            else:w.tick()
            self.assertNotEqual(row['phase'],'stopped',mode)
            self.assertFalse(any(r['enabled'] for _,r in p.events),mode)
            self.assertEqual(len(w.data['encounters']),1,mode)

    def test_definite_activation_rejection_can_quarantine_but_ambiguous_cannot(self):
        for definite in (True,False):
            p=Party();w=p.worker()
            def reject(actor,method,params):
                if actor.guid==4 and method=='buddy-actions.fargodeep_encounter' and params.get('enabled'):
                    raise (cave.EncounterRejected if definite else cave.Error)('FailedPrecondition')
            p.hook=reject;w.tick();row=w.data['encounters'][0]
            self.assertEqual(row['phase'],'stopped' if definite else 'unconfirmed')
            self.assertEqual(w.done,not definite)
            if definite:
                self.assertEqual(row['activationRejection'],{'guid':4,'status':'FailedPrecondition'})
                self.assertEqual(row['abandonment']['cause'],'activation_failed_precondition')
            else:self.assertNotIn('abandonment',row)
            self.assertFalse(row['deadObserved'])

    def test_rpc_definite_rejection_requires_matching_structured_server_identity(self):
        class Response:
            def __init__(self,value):self.body=json.dumps(value).encode()
            def __enter__(self):return self
            def __exit__(self,*args):pass
            def read(self,limit):return self.body[:limit]
        class Opener:
            def __init__(self,mode):self.mode=mode
            def open(self,request,timeout):
                body=json.loads(request.data);value=dict(jsonrpc='2.0',id=body['id'],error=dict(code=-32000,data=dict(module='buddy-actions',method='fargodeep_encounter',grpc_status='FailedPrecondition')))
                if self.mode=='wrongid':value['id']+=1
                if self.mode=='wrongmodule':value['error']['data']['module']='other'
                if self.mode=='wrongmethod':value['error']['data']['method']='follow'
                if self.mode=='unavailable':value['error']['data']['grpc_status']='Unavailable'
                if self.mode=='messageonly':value['error']=dict(code=-32000,message='FailedPrecondition')
                return Response(value)
        for mode in ('matching','wrongid','wrongmodule','wrongmethod','unavailable','messageonly'):
            rpc=cave.RPC('http://127.0.0.1:8086/rpc');rpc.opener=Opener(mode)
            with self.assertRaises(cave.Error) as raised:rpc.call('buddy-actions.fargodeep_encounter',dict(enabled=True))
            self.assertEqual(isinstance(raised.exception,cave.EncounterRejected),mode=='matching')


    def test_additional_attacker_terminal_retires_unknown_and_never_replays_or_credits(self):
        for restart in (False, True):
            with self.subTest(restart=restart):
                p = Party(); w = p.worker(); w.tick(); row = w.data['encounters'][0]
                target = row['target']
                p.rpcs[4].actions['fargodeep'].update(active=False, phase='additional_attacker_observed')
                if restart:
                    row['phase'] = 'unconfirmed'
                    w = p.worker(stored=copy.deepcopy(w.data)); row = w.data['encounters'][0]
                before = [copy.deepcopy(a.bags) for a in p.rpcs.values()]
                p.events = []; w.tick()
                self.assertFalse(w.done); self.assertEqual(row['phase'], 'stopped')
                self.assertFalse(row['deadObserved']); self.assertNotIn('deathObservation', row)
                self.assertEqual(row['abandonment']['kind'], 'outcome_unknown_target_quarantined')
                self.assertEqual(row['abandonment']['cause'], 'additional_attacker_observed')
                self.assertEqual([a.bags for a in p.rpcs.values()], before)
                self.assertFalse(any(request['enabled'] for _, request in p.events))
                w.tick(); self.assertEqual(row['target'], target)
                self.assertFalse(any(request['enabled'] for _, request in p.events))
                self.assertTrue(all(not r['quest47Complete'] and not r['quest60Complete'] for r in w.status['characters']))

    def test_unconfirmed_add_retirement_requires_exact_inactive_terminal_and_fresh_idle_cleanup(self):
        for mode in ('target', 'engager', 'phase', 'active', 'combat', 'loot', 'owner', 'body', 'cleanup'):
            with self.subTest(mode=mode):
                p = Party(); w = p.worker(); w.tick(); row = w.data['encounters'][0]; row['phase'] = 'unconfirmed'
                f = p.rpcs[4].actions['fargodeep']; f.update(active=False, phase='additional_attacker_observed')
                if mode == 'target': f['targetGuid'] = str(TARGET+1)
                if mode == 'engager': f['engagerGuid'] = '5'
                if mode == 'phase': f['phase'] = 'unknown'
                if mode == 'active': f['active'] = True
                if mode == 'combat': p.rpcs[1].self['unitFlags'] |= 0x80000
                if mode == 'loot': p.rpcs[5].loot.update(phase='waiting_item', pendingCorpseGuid=str(TARGET))
                if mode == 'owner':
                    old = p.rpcs[1].world
                    p.rpcs[1].world = lambda params: dict(old(params), owner=dict(present=True, guid='3'))
                if mode == 'body': p.rpcs[4].session = 'changed'
                if mode == 'cleanup': p.rpcs[5].actions['fargodeep']['targetGuid'] = str(TARGET+2)
                p.events = []
                try: w.tick()
                except cave.Error: pass
                self.assertNotEqual(row['phase'], 'stopped'); self.assertFalse(row['deadObserved'])
                self.assertNotIn('abandonment', row)
                self.assertFalse(any(request['enabled'] for _, request in p.events))


class SearchActor(Actor):
    def __init__(self, party, guid):
        super().__init__(party, guid)
        self.self['movementFlagsKnown'] = True
        self.actions['position'].update(cave.station(4), revision='1')
        self.target['present'] = False
        self.token = None
        self.mode = 'arrive'
        self.unblock = threading.Event()
        self.forks = 0

    def fork(self, timeout=35):
        actor = self
        self.forks += 1
        class Route:
            def call(self, method, params, timeout=None): return actor.route(method, params, timeout)
        return Route()

    def call(self, method, params, timeout=None):
        if method not in ('buddy-actions.acquire_interaction', 'buddy-actions.release_interaction'):
            return super().call(method, params)
        self.calls.append((method, copy.deepcopy(params)))
        self.party.movement.append((self.guid, method, copy.deepcopy(params)))
        if self.party.hook: self.party.hook(self, method, params)
        if method == 'buddy-actions.acquire_interaction':
            saved = self.party.saved[-1]
            reserved = saved.get('search', {}).get('legs', [])[-1:] + [r['lootApproaches'][-1] for r in saved['encounters'] if r.get('lootApproaches')]
            assert any(leg['phase'] == 'reserved' for leg in reserved)
            assert params == dict(owner='exploration', leaseMs=30000, harvest=True)
            self.token = 'token-' + str(self.guid)
            self.actions.update(interactionOwner='exploration', interactionHarvest=True, interactionLeaseRemainingMs=30000)
            if self.mode == 'acquire_lost': raise cave.Error('acquire response lost')
            return dict(token=self.token, state=copy.deepcopy(self.actions))
        assert params == dict(owner='exploration', token=self.token)
        if self.actions.get('interactionOwner') != 'exploration': raise cave.Error('exact lease no longer held')
        if self.mode == 'release_lost': raise cave.Error('release uncertain')
        self.actions.update(interactionOwner='', interactionHarvest=False, moving=False, traveling=False)
        self.unblock.set()
        return copy.deepcopy(self.actions)

    def route(self, method, params, timeout):
        self.party.movement.append((self.guid, method, copy.deepcopy(params)))
        assert method == 'buddy-actions.travel_to' and timeout == 35
        assert all(a.token for a in self.party.rpcs.values()), 'cohort was not fully in custody'
        assert params['token'] == self.token and params['mapId'] == 0 and params['arrivalDistance'] == .75
        self.actions.update(moving=True, traveling=True)
        if self.mode == 'block':
            if not self.unblock.wait(3): raise cave.Error('test route cancellation was not released')
            self.party.movement.append((self.guid, 'route_returned_after_release', {}))
            raise cave.Error('route cancelled')
        if self.mode not in ('stall', 'nav_failure'):
            self.actions['position'].update({k: params[k] for k in ('x', 'y', 'z')})
            self.actions['position']['revision'] = str(int(self.actions['position']['revision'])+1)
        self.actions.update(moving=False, traveling=False, reason='exploration_within_distance')
        if self.mode in ('partial', 'lost_nonarrival'): self.actions['position']['x'] += 2
        if self.mode in ('lost', 'lost_nonarrival'): raise cave.Error('travel response lost')
        if self.mode == 'nav_failure': self.actions['reason'] = 'exploration_navigation_unavailable'
        return copy.deepcopy(self.actions)


class SearchParty(Party):
    def __init__(self):
        super().__init__()
        self.movement = []
        self.rpcs = {g: SearchActor(self, g) for g in cave.ROSTER}

    def scan(self, worker):
        for _ in range(3): worker.tick()

    def routes(self): return [v for v in self.movement if v[1] == 'buddy-actions.travel_to']


class CorridorSearchTests(unittest.TestCase):
    def test_idle_three_polls_then_cohort_precise_route_and_original_encounters_unchanged(self):
        p = SearchParty(); w = p.worker(); raw = [copy.deepcopy(a.self) for a in p.rpcs.values()]
        w.tick(); w.tick(); self.assertEqual(p.movement, [])
        w.tick()
        self.assertFalse(w.done); self.assertEqual(len(p.routes()), 3)
        self.assertEqual(w.status['phase'], 'corridor_station_arrived_reobserve')
        self.assertEqual(w.data['encounters'], [])
        self.assertEqual(w.data['search']['cursor'], 5)
        self.assertEqual(w.data['search']['legs'][0]['phase'], 'arrived')
        self.assertTrue(w.data['search']['legs'][0]['cleanupResolved'])
        self.assertEqual([a.self for a in p.rpcs.values()], raw, 'raw server pose must not be overwritten')
        self.assertTrue(all(a.forks == 1 for a in p.rpcs.values()))
        self.assertTrue(all(not a.actions['interactionOwner'] for a in p.rpcs.values()))
        # Actual post-arrival observed target enters the ordinary candidate flow.
        for a in p.rpcs.values():
            a.target.update(cave.station(5), present=True)
            a.target['x'] += 5
        w.tick()
        self.assertEqual([g for g, request in p.events if request['enabled']], [1, 5, 4])
        self.assertEqual(w.data['search']['legs'][0]['phase'], 'arrived')

    def test_max_eight_legs_and_ordered_reverse_next_pass_without_history_reset(self):
        p = SearchParty(); w = p.worker()
        for _ in range(8): p.scan(w)
        self.assertFalse(w.done); self.assertEqual(w.data['search']['cursor'], 12)
        self.assertEqual([leg['station'] for leg in w.data['search']['legs']], list(range(5, 13)))
        p.scan(w); self.assertTrue(w.done); self.assertEqual(len(p.routes()), 24)
        old = copy.deepcopy(w.data['search']['legs'])
        resumed = p.worker(stored=copy.deepcopy(w.data)); p.scan(resumed)
        self.assertEqual(resumed.data['search']['cursor'], 11)
        self.assertEqual(resumed.data['search']['direction'], -1)
        self.assertEqual(resumed.data['search']['legs'][:-1], old)

    def test_initial_station_is_actual_same_floor_and_regroups_spread_cohort(self):
        p = SearchParty(); w = p.worker()
        p.rpcs[5].actions['position']['x'] += 4
        p.scan(w)
        self.assertEqual({tuple(r[k] for k in ('x', 'y', 'z')) for _, _, r in p.routes()}, {cave.SEARCH_STATIONS[4]})
        self.assertEqual(w.data['search']['cursor'], 4)
        p = SearchParty(); w = p.worker()
        for a in p.rpcs.values(): a.actions['position']['z'] += 100
        p.scan(w); self.assertTrue(w.done); self.assertEqual(p.movement, [])
        self.assertEqual(w.status['phase'], 'corridor_not_near_current_party_floor')

    def test_search_refuses_resources_other_activities_unknowns_and_owner(self):
        modes = ('mana', 'health', 'bag', 'cast', 'attack', 'quest', 'trade', 'profession', 'lease', 'moving', 'owner', 'movement_unknown', 'rooted', 'fresh_attacker')
        for mode in modes:
            with self.subTest(mode=mode):
                p = SearchParty(); w = p.worker(); a = p.rpcs[1]
                if mode == 'mana': a.self['power'] = 59
                if mode == 'health': a.self['health'] = 94
                if mode == 'bag':
                    for i in range(16): a.bags[0]['slots'][i] = dict(bag=0, slot=i, itemGuid=str(100+i), metadataKnown=True, entry=117, stackCount=1)
                if mode == 'cast': a.actions['castPending'] = True
                if mode == 'attack': a.actions['autoAttackRequested'] = True
                if mode == 'quest':
                    old = a.call
                    def busy(method, params, timeout=None):
                        if method == 'buddy-quests.state': return dict(sessionId=a.session, actionInFlight=True)
                        return old(method, params, timeout)
                    a.call = busy
                if mode == 'trade': a.trade.update(phase='ACTIVE')
                if mode == 'profession': a.professions['active'] = True
                if mode == 'lease': a.actions.update(interactionOwner='quests')
                if mode == 'moving': a.actions['moving'] = True
                if mode == 'owner':
                    old = a.world
                    a.world = lambda params: dict(old(params), owner=dict(guid='3', present=True))
                if mode == 'movement_unknown': a.self['movementFlagsKnown'] = False
                if mode == 'rooted': a.self['unitFlags'] |= 0x40000
                if mode == 'fresh_attacker': a.target.update(present=True, attackVictimKnown=True, attackVictimGuid='4', attackAgeMs=3)
                p.scan(w); self.assertEqual(p.movement, []); self.assertEqual(p.events, [])

    def test_each_acquire_rechecks_all_three_and_never_moves_after_body_or_control_change(self):
        for mode in ('body', 'controls', 'owner', 'combat'):
            with self.subTest(mode=mode):
                p = SearchParty(); w = p.worker()
                def change(actor, method, params):
                    if method == 'buddy-actions.acquire_interaction' and actor.guid == 1:
                        if mode == 'body': p.rpcs[4].session = 'replacement'
                        if mode == 'controls': p.rpcs[4].actions['following'] = True
                        if mode == 'owner': p.rpcs[4].world = lambda params: dict(Actor.world(p.rpcs[4], params), owner=dict(present=True, guid='3'))
                        if mode == 'combat': p.rpcs[4].self['unitFlags'] |= 0x80000
                p.hook = change; p.scan(w)
                self.assertTrue(w.done); self.assertEqual(p.routes(), [])
                self.assertFalse(p.rpcs[1].actions.get('interactionOwner'))

    def test_ambiguous_partial_failed_and_nonprogress_route_never_retried_or_new_pull(self):
        for mode in ('lost_nonarrival', 'partial', 'nav_failure', 'stall', 'release_lost', 'acquire_lost'):
            with self.subTest(mode=mode):
                p = SearchParty(); w = p.worker(); p.rpcs[4].mode = mode
                p.scan(w); self.assertTrue(w.done); self.assertEqual(p.events, [])
                self.assertEqual(w.data['search']['legs'][-1]['phase'], 'yielded')
                calls = len(p.routes())
                resumed = p.worker(stored=copy.deepcopy(w.data)); resumed.tick()
                self.assertTrue(resumed.done); self.assertEqual(len(p.routes()), calls)

    def test_watchdog_releases_every_owned_token_before_waiting_on_blocking_routes(self):
        for mode in ('stop', 'deadline', 'combat', 'owner', 'controls', 'replacement'):
            with self.subTest(mode=mode):
                p = SearchParty(); stop = [False]; w = p.worker(stop=lambda: stop[0])
                for a in p.rpcs.values(): a.mode = 'block'
                changed = [False]
                def trigger(actor, method, params):
                    if method == 'buddy-party.state' and all(a.actions.get('moving') for a in p.rpcs.values()) and not changed[0]:
                        changed[0] = True
                        if mode == 'stop': stop[0] = True
                        if mode == 'deadline': p.time = 601
                        if mode == 'combat': p.rpcs[1].self['unitFlags'] |= 0x80000
                        if mode == 'owner': p.rpcs[4].world = lambda params: dict(Actor.world(p.rpcs[4], params), owner=dict(present=True, guid='3'))
                        if mode == 'controls': p.rpcs[4].actions['following'] = True
                        if mode == 'replacement':
                            p.rpcs[4].actions.update(interactionOwner='training', moving=False, traveling=False)
                            p.rpcs[4].unblock.set()
                p.hook = trigger; p.scan(w)
                self.assertTrue(changed[0]); self.assertTrue(w.done)
                self.assertEqual(len([v for v in p.movement if v[1] == 'buddy-actions.release_interaction']), 3)
                self.assertFalse(p.rpcs[1].actions.get('interactionOwner'))
                self.assertFalse(p.rpcs[5].actions.get('interactionOwner'))
                if mode == 'replacement':
                    self.assertEqual(p.rpcs[4].actions['interactionOwner'], 'training')
                    self.assertFalse(w.data['search']['legs'][-1]['cleanupResolved'])

    def test_reserved_leg_restart_does_not_arm_even_if_new_target_visible(self):
        p = SearchParty(); w = p.worker(); p.scan(w)
        saved = copy.deepcopy(w.data); saved['search']['legs'][-1]['phase'] = 'reserved'
        for a in p.rpcs.values(): a.target.update(cave.station(5), present=True)
        count = len(p.movement); resumed = p.worker(stored=saved); resumed.tick()
        self.assertTrue(resumed.done); self.assertEqual(p.events, []); self.assertEqual(len(p.movement), count)

    def test_original_v1_target_history_survives_search_and_cannot_be_rearmed(self):
        p = SearchParty(); w = p.worker()
        old = dict(target=TARGET, entry=475, engager=4, phase='stopped', deadObserved=False, started=0., requests={},
            abandonment=dict(kind='outcome_unknown_target_absent_all_three'))
        w.data['encounters'].append(copy.deepcopy(old)); p.scan(w)
        self.assertEqual(w.data['encounters'], [old])
        for a in p.rpcs.values(): a.target.update(cave.station(5), present=True)
        w.tick(); self.assertEqual(p.events, [])

    def test_final_stop_before_route_threads_and_changed_endpoint_never_write(self):
        p = SearchParty(); stop = [False]; w = p.worker(stop=lambda: stop[0])
        original = p.rpcs[5].fork
        def stop_before_fork(timeout=35):
            stop[0] = True
            return original(timeout)
        p.rpcs[5].fork = stop_before_fork
        p.scan(w); self.assertTrue(w.done); self.assertEqual(p.routes(), [])
        self.assertTrue(all(not a.actions.get('interactionOwner') for a in p.rpcs.values()))
        p = SearchParty(); w = p.worker()
        for g, actor in p.rpcs.items(): actor.url = f'http://127.0.0.1:{cave.ROSTER[g][1]}/rpc'
        def replace(actor, method, params):
            if method == 'buddy-actions.acquire_interaction' and actor.guid == 1:
                actor.url = 'http://127.0.0.1:8402/rpc'
        p.hook = replace; p.scan(w)
        self.assertTrue(w.done); self.assertEqual(p.routes(), [])
        self.assertFalse(any(v[0] == 1 and v[1] == 'buddy-actions.release_interaction' for v in p.movement), 'never send a token to a changed endpoint')

    def test_cleanup_body_or_endpoint_change_never_becomes_positive_custody_proof(self):
        for mode in ('body', 'endpoint'):
            with self.subTest(mode=mode):
                p = SearchParty(); w = p.worker(); changed = [False]
                for g, actor in p.rpcs.items(): actor.url = f'http://127.0.0.1:{cave.ROSTER[g][1]}/rpc'
                def replace(actor, method, params):
                    if actor.guid == 4 and method == 'buddy-actions.release_interaction':
                        changed[0] = True
                        if mode == 'body': actor.session = 'new-body'
                        else: actor.url = 'http://127.0.0.1:8402/rpc'
                p.hook = replace; p.scan(w)
                self.assertTrue(changed[0]); self.assertTrue(w.done)
                self.assertFalse(w.data['search']['legs'][-1]['cleanupResolved'])
                self.assertEqual(p.events, [])

    def test_search_budget_and_malformed_saved_state_never_write(self):
        p = SearchParty(); w = p.worker(seconds=35); p.scan(w)
        self.assertTrue(w.done); self.assertEqual(p.movement, [])
        p = SearchParty(); w = p.worker(); p.scan(w); saved = copy.deepcopy(w.data)
        for key, value in (('route', 'foreign'), ('cursor', -1), ('direction', 0), ('legs', [])):
            bad = copy.deepcopy(saved); bad['search'][key] = value
            with self.assertRaises(cave.Error): p.worker(stored=bad)
        bad = copy.deepcopy(saved); bad['search']['legs'] *= 65
        with self.assertRaises(cave.Error): p.worker(stored=bad)



class PriorityActor(SearchActor):
    def route(self, method, params, timeout):
        if self.mode not in ('priority', 'plain_cancel', 'normal_cancel', 'cancel_lost', 'cancel_motion_unknown'):
            return super().route(method, params, timeout)
        self.party.movement.append((self.guid, method, copy.deepcopy(params)))
        assert method == 'buddy-actions.travel_to' and params['token'] == self.token
        assert all(a.token for a in self.party.rpcs.values())
        self.actions.update(moving=True, traveling=True)
        if self.mode in ('priority', 'plain_cancel'):
            p = self.actions['position']
            p.update({k: p[k]+.4*(params[k]-p[k]) for k in ('x', 'y', 'z')})
            p['revision'] = str(int(p['revision'])+1)
            if self.mode == 'priority': self.self['unitFlags'] |= 0x80000
            self.actions.update(moving=False, traveling=False, reason='harvest_fresh_actual_attacker_takes_priority' if self.mode == 'priority' else 'exploration_lease_changed_or_expiring')
        else:
            if not self.unblock.wait(3): raise cave.Error('normal stop never arrived')
            self.actions.update(moving=False, traveling=False, reason='exploration_following_client_navmesh_estimate')
            if self.mode == 'cancel_lost': raise cave.Error('normal cancellation response lost')
            if self.mode == 'cancel_motion_unknown': self.actions['motionUnconfirmed'] = True
        return copy.deepcopy(self.actions)


class PriorityParty(SearchParty):
    def __init__(self):
        super().__init__(); self.rpcs = {g: PriorityActor(self, g) for g in cave.ROSTER}
        self.rpcs[1].mode = 'priority'
        for g in (4, 5): self.rpcs[g].mode = 'normal_cancel'

    def recover(self):
        for a in self.rpcs.values():
            a.self['unitFlags'] &= ~0x80000; a.self.update(health=100, power=100)
            a.mode = 'arrive'


class KnownSearchStopTests(unittest.TestCase):
    def test_known_combat_partial_waits_then_new_token_and_fresh_plan_without_claiming_old_arrival(self):
        p = PriorityParty(); w = p.worker(); p.scan(w)
        self.assertFalse(w.done)
        old = w.data['search']['legs'][0]
        self.assertEqual(old['phase'], 'preempted'); self.assertTrue(old['cleanupResolved'])
        self.assertTrue(all(r['normalStopped'] for r in old['routeResults'].values()))
        self.assertEqual(w.data['search']['cursor'], 4)
        calls = len(p.routes()); w.tick(); self.assertEqual(len(p.routes()), calls)
        self.assertEqual(w.status['phase'], 'waiting_for_safe_party_after_known_corridor_preemption')
        p.recover(); p.scan(w)
        self.assertFalse(w.done); self.assertEqual(len(p.routes()), calls+3)
        self.assertEqual([leg['phase'] for leg in w.data['search']['legs']], ['preempted', 'arrived'])
        self.assertEqual(w.data['search']['cursor'], 5); self.assertEqual(w.search_legs, 2)
        self.assertEqual(w.data['search']['legs'][1]['before']['1'], old['after']['1'])
        self.assertNotIn('arrivalObservation', old)
        self.assertEqual(w.data['encounters'], [])

    def test_preemption_recovery_fresh_resources_idle_owner_and_control_gates(self):
        for mode in ('mana', 'cast', 'attack', 'lease', 'owner', 'trade', 'loot'):
            with self.subTest(mode=mode):
                p = PriorityParty(); w = p.worker(); p.scan(w); p.recover(); a = p.rpcs[4]
                if mode == 'mana': a.self['power'] = 59
                if mode == 'cast': a.actions['castPending'] = True
                if mode == 'attack': a.actions['autoAttacking'] = True
                if mode == 'lease': a.actions['interactionOwner'] = 'quests'
                if mode == 'owner': a.world = lambda params: dict(Actor.world(a, params), owner=dict(present=True, guid='3'))
                if mode == 'trade': a.trade['phase'] = 'ACTIVE'
                if mode == 'loot': a.loot.update(phase='waiting_release', pendingCorpseGuid=str(TARGET))
                calls = len(p.routes()); p.scan(w)
                self.assertFalse(w.done); self.assertEqual(len(p.routes()), calls)
                self.assertEqual(w.status['phase'], 'waiting_for_safe_party_after_known_corridor_preemption')
        p = PriorityParty(); w = p.worker(); p.scan(w); p.recover(); p.rpcs[4].actions['following'] = True
        w.tick(); self.assertTrue(w.done); self.assertEqual(len(p.routes()), 3)

    def test_unknown_nonarrival_or_unresolved_stop_never_rearms_even_when_later_idle(self):
        for mode in ('cancel_lost', 'cancel_motion_unknown', 'release_lost', 'no_priority'):
            with self.subTest(mode=mode):
                p = PriorityParty(); w = p.worker()
                if mode == 'no_priority': p.rpcs[1].mode = 'plain_cancel'
                else: p.rpcs[4].mode = mode
                p.scan(w); self.assertTrue(w.done)
                self.assertEqual(w.data['search']['legs'][-1]['phase'], 'yielded')
                p.recover(); p.rpcs[4].actions.update(motionUnconfirmed=False, interactionOwner='')
                calls = len(p.routes()); restored = p.worker(stored=copy.deepcopy(w.data)); restored.tick()
                self.assertTrue(restored.done); self.assertEqual(len(p.routes()), calls)

    def test_observed_full_arrival_with_lost_rpc_records_station_fact_without_resending(self):
        p = SearchParty(); w = p.worker(); p.rpcs[4].mode = 'lost'; p.scan(w)
        self.assertFalse(w.done); self.assertEqual(len(p.routes()), 3)
        leg = w.data['search']['legs'][0]
        self.assertEqual(leg['phase'], 'arrived'); self.assertEqual(w.data['search']['cursor'], 5)
        self.assertFalse(leg['arrivalObservation']['oldRpcSuccessClaimed'])
        self.assertFalse(leg['routeResults']['4']['normalStopped'])
        self.assertEqual(w.data['encounters'], [])

    def legacy_yielded_arrival(self):
        p = SearchParty(); w = p.worker(); p.scan(w); saved = copy.deepcopy(w.data)
        leg = saved['search']['legs'][-1]; leg.update(phase='yielded', outcome='corridor_guard_changed')
        leg.pop('bodySessions', None); leg.pop('routeResults', None)
        saved['search']['cursor'] = 4
        return p, saved

    def test_existing_119_yielded_arrived_reconciles_only_positive_complete_samebody_proof(self):
        p, saved = self.legacy_yielded_arrival(); before = copy.deepcopy(saved['encounters'])
        calls = len(p.routes()); w = p.worker(stored=saved); w.tick()
        self.assertFalse(w.done); self.assertEqual(len(p.routes()), calls)
        leg = w.data['search']['legs'][-1]
        self.assertEqual(leg['phase'], 'arrived'); self.assertEqual(w.data['search']['cursor'], 5)
        self.assertEqual(leg['outcome'], 'corridor_guard_changed')
        self.assertFalse(leg['arrivalObservation']['oldRpcSuccessClaimed'])
        self.assertEqual(w.data['encounters'], before)
        for mode in ('no_close', 'missing_close', 'no_progress', 'not_arrived', 'fresh_not_arrived', 'reserved', 'wrong_body', 'revision_regressed'):
            with self.subTest(mode=mode):
                p, saved = self.legacy_yielded_arrival(); leg = saved['search']['legs'][-1]
                if mode == 'no_close': leg['cleanup'][0]['custodyResolved'] = False
                if mode == 'missing_close': leg['cleanup'].pop()
                if mode == 'no_progress': leg['after']['1']['revision'] = leg['before']['1']['revision']
                if mode == 'not_arrived': leg['after']['1']['x'] += 3
                if mode == 'fresh_not_arrived': p.rpcs[1].actions['position']['x'] += 3
                if mode == 'reserved': leg['phase'] = 'reserved'
                if mode == 'wrong_body': p.rpcs[4].session = 'replacement'
                if mode == 'revision_regressed': p.rpcs[1].actions['position']['revision'] = '0'
                if mode == 'wrong_body':
                    with self.assertRaises(cave.Error): p.worker(stored=saved)
                    continue
                w = p.worker(stored=saved); calls = len(p.routes())
                try: w.tick()
                except cave.Error: pass
                self.assertTrue(w.done); self.assertEqual(len(p.routes()), calls)
                self.assertNotEqual(leg['phase'], 'arrived'); self.assertNotIn('arrivalObservation', leg)

    def test_existing_trigger_to_station11_arrival_sets_reverse_cursor_without_resending(self):
        p, saved = self.legacy_yielded_arrival(); leg = saved['search']['legs'][-1]
        saved['search'].update(cursor=12, direction=1); leg['station'] = 11
        for g, actor in p.rpcs.items():
            leg['before'][str(g)].update(cave.station(12))
            leg['after'][str(g)].update(cave.station(11))
            actor.actions['position'].update(leg['after'][str(g)])
        calls = len(p.routes()); w = p.worker(stored=saved); w.tick()
        self.assertFalse(w.done); self.assertEqual(len(p.routes()), calls)
        self.assertEqual(w.data['search']['cursor'], 11); self.assertEqual(w.data['search']['direction'], -1)
        self.assertFalse(leg['arrivalObservation']['oldRpcSuccessClaimed'])

    def test_known_preemption_still_counts_every_attempt_toward_eight_and_body_binding(self):
        p = PriorityParty(); w = p.worker()
        for n in range(8):
            if n: p.recover(); p.rpcs[1].mode = 'priority'; p.rpcs[4].mode = p.rpcs[5].mode = 'normal_cancel'
            p.scan(w)
            self.assertFalse(w.done); self.assertEqual(w.search_legs, n+1)
        p.recover(); p.scan(w); self.assertTrue(w.done); self.assertEqual(len(p.routes()), 24)
        saved = copy.deepcopy(w.data); saved['search']['legs'][-1]['bodySessions']['4'] = 'other'
        with self.assertRaises(cave.Error): p.worker(stored=saved)
        saved = copy.deepcopy(w.data); saved['search']['legs'][-1].pop('routeResults')
        with self.assertRaises(cave.Error): p.worker(stored=saved)
        saved = copy.deepcopy(w.data); saved['search']['legs'][-1]['cleanup'].pop()
        with self.assertRaises(cave.Error): p.worker(stored=saved)



LOWER_FLOOR = dict(x=-9820.912, y=226.80743, z=23.68278)
UPPER_CORPSE = dict(x=-9804.73, y=238.0915, z=40.68351)


def corpse_party(priority=False):
    p = PriorityParty() if priority else SearchParty()
    for actor in p.rpcs.values():
        actor.actions['position'].update(LOWER_FLOOR)
        actor.target.update(UPPER_CORPSE, present=True, level=6)
    w = p.worker(); w.tick(); p.kill(); row = w.data['encounters'][0]
    assert w.cleanup(row)
    row.update(phase='loot', deadObserved=True, lootAt=0.)
    for actor in p.rpcs.values():
        actor.target['dynamicFlags'] = 13
        actor.lootable = [copy.deepcopy(actor.target)]
    return p, w, row


class CorpseCohortTests(unittest.TestCase):
    def test_reported_upper_floor_corpse_gets_full_normal_guarded_cohort_move_before_loot_clock(self):
        p, w, row = corpse_party(); p.time = 100
        before = [copy.deepcopy(a.bags) for a in p.rpcs.values()]
        requests = copy.deepcopy(row['requests']); target = row['target']
        w.tick()
        self.assertFalse(w.done); self.assertEqual(len(p.routes()), 3)
        self.assertEqual({tuple(r[k] for k in ('x','y','z')) for _,_,r in p.routes()}, {tuple(UPPER_CORPSE[k] for k in ('x','y','z'))})
        leg = row['lootApproaches'][0]
        self.assertEqual(leg['phase'], 'arrived'); self.assertTrue(leg['cleanupResolved'])
        self.assertEqual(row['lootAt'], 0); self.assertNotIn('lootWaitAt', row)
        self.assertEqual([a.bags for a in p.rpcs.values()], before)
        self.assertEqual(row['requests'], requests); self.assertEqual(row['target'], target)
        self.assertNotIn('search', w.data)
        p.time = 102; w.tick()
        self.assertFalse(w.done); self.assertEqual(row['lootWaitAt'], 102)
        p.time = 136; w.tick(); self.assertFalse(w.done)
        p.time = 138; w.tick(); self.assertTrue(w.done)
        self.assertEqual(w.status['phase'], 'loot_uncollected_or_cleanup_unconfirmed_no_new_pull')

    def test_actual_protobuf_json_omits_dead_alive_false_and_zero_health(self):
        p,w,row=corpse_party()
        for actor in p.rpcs.values():
            actor.target.pop('alive');actor.target.pop('health');actor.target.pop('mapId')
        w.tick();self.assertFalse(w.done);self.assertEqual(len(p.routes()),3)
        self.assertEqual(row['lootApproaches'][0]['phase'],'arrived')
        p,w,row=corpse_party()
        for actor in p.rpcs.values():
            actor.target.pop('alive');actor.target.pop('health');actor.target['aliveKnown']=False
        w.tick();self.assertTrue(w.done);self.assertEqual(p.routes(),[])

    def test_missing_observer_never_hides_contradictory_corpse_after_cleanup(self):
        for mode in ('alive','moved','wrong_guid','both_moved','both_revision'):
            with self.subTest(mode=mode):
                p,w,row=corpse_party();scenes=w.read(TARGET)
                expected=w.corpse_views(scenes,row)
                scenes[1]['world'].pop('requestedCreature')
                other=scenes[4]['world']['requestedCreature']
                if mode=='alive':other.update(alive=True,health=10)
                if mode=='moved':other['x']+=1
                if mode=='wrong_guid':other['guid']=str(TARGET+1)
                if mode=='both_moved':
                    for g in (4,5):scenes[g]['world']['requestedCreature']['x']+=1
                if mode=='both_revision':
                    for g in (4,5):scenes[g]['world']['requestedCreature']['positionRevision']='2'
                with self.assertRaises(cave.Error):w.corpse_arrival_guard(scenes,row,expected)

    def test_already_same_floor_in_loot_range_needs_no_exploration_or_new_encounter(self):
        p, w, row = corpse_party()
        for actor in p.rpcs.values(): actor.actions['position'].update(UPPER_CORPSE)
        p.events = []; p.time = 30; w.tick()
        self.assertFalse(w.done); self.assertEqual(p.routes(), []); self.assertEqual(p.events, [])
        self.assertEqual(row['lootWaitAt'], 30); self.assertNotIn('lootApproaches', row)

    def test_actual_bound_dead_corpse_and_normal_safety_are_required_before_any_move(self):
        modes = ('alive','wrong_guid','wrong_entry','wrong_map','unknown_position','unknown_revision','untapped','disagree',
            'request_not_stopped','foreign_encounter','mana','health','pending_loot','trade','profession','quest','owner','control','body','far')
        for mode in modes:
            with self.subTest(mode=mode):
                p, w, row = corpse_party(); a = p.rpcs[4]
                if mode == 'alive': a.target.update(alive=True, health=50)
                if mode == 'wrong_guid': a.target['guid'] = str(TARGET+1)
                if mode == 'wrong_entry': a.target['entry'] = 40
                if mode == 'wrong_map': a.target['mapId'] = 1
                if mode == 'unknown_position': a.target['positionKnown'] = False
                if mode == 'unknown_revision': a.target['positionRevision'] = '0'
                if mode == 'untapped': a.target['dynamicFlags'] = 1
                if mode == 'disagree': a.target['x'] += 1
                if mode == 'request_not_stopped': row['requests']['4']['phase'] = 'observed'
                if mode == 'foreign_encounter': a.actions['fargodeep']['targetGuid'] = str(TARGET+2)
                if mode == 'mana': a.self['power'] = 59
                if mode == 'health': a.self['health'] = 94
                if mode == 'pending_loot': a.loot.update(phase='waiting_item', pendingCorpseGuid=str(TARGET))
                if mode == 'trade': a.trade['phase'] = 'ACTIVE'
                if mode == 'profession': a.professions['active'] = True
                if mode == 'quest':
                    old = a.call
                    def busy(method, params, timeout=None):
                        if method == 'buddy-quests.state': return dict(sessionId=a.session, actionInFlight=True)
                        return old(method, params, timeout)
                    a.call = busy
                if mode == 'owner': a.world = lambda params: dict(Actor.world(a, params), owner=dict(guid='3', present=True))
                if mode == 'control': a.actions['following'] = True
                if mode == 'body': a.session = 'other-body'
                if mode == 'far':
                    for actor in p.rpcs.values(): actor.actions['position']['x'] -= 100
                p.events = []
                try: w.tick()
                except cave.Error: pass
                self.assertEqual(p.routes(), []); self.assertEqual(p.events, [])

    def test_corpse_revision_pose_or_life_change_during_lease_reads_stops_before_route(self):
        for mode in ('revision','pose','alive','absent','body'):
            with self.subTest(mode=mode):
                p, w, row = corpse_party()
                def change(actor, method, params):
                    if actor.guid == 1 and method == 'buddy-actions.acquire_interaction':
                        other = p.rpcs[4]
                        if mode == 'revision': other.target['positionRevision'] = '2'
                        if mode == 'pose': other.target['x'] += 1
                        if mode == 'alive': other.target.update(alive=True, health=50)
                        if mode == 'body': other.session = 'replacement'
                        if mode == 'absent':
                            old = other.world
                            def gone(params):
                                result = old(params); result.pop('requestedCreature', None); return result
                            other.world = gone
                p.hook = change; w.tick()
                self.assertTrue(w.done); self.assertEqual(p.routes(), [])
                self.assertFalse(p.rpcs[1].actions.get('interactionOwner'))
                self.assertEqual(row['lootApproaches'][0]['phase'], 'yielded')

    def test_known_combat_partial_defends_then_uses_new_snapshot_and_new_physical_plan(self):
        p, w, row = corpse_party(priority=True); w.tick()
        self.assertFalse(w.done); self.assertEqual(row['lootApproaches'][0]['phase'], 'preempted')
        self.assertNotIn('lootWaitAt', row); calls = len(p.routes())
        w.tick(); self.assertEqual(len(p.routes()), calls); self.assertFalse(w.done)
        p.recover()
        # A NEW dead-corpse observation is the new route goal. Frozen revision
        # comparisons applied within the prior leg, not guessed old coordinates.
        for a in p.rpcs.values(): a.target.update(x=UPPER_CORPSE['x']+.5, positionRevision='2')
        w.tick()
        self.assertFalse(w.done); self.assertEqual(len(p.routes()), calls+3)
        self.assertEqual([leg['phase'] for leg in row['lootApproaches']], ['preempted','arrived'])
        second = row['lootApproaches'][1]
        self.assertEqual(second['targetViews']['1']['positionRevision'], 2)
        self.assertEqual(second['goal']['x'], UPPER_CORPSE['x']+.5)
        self.assertEqual(second['before']['1'], row['lootApproaches'][0]['after']['1'])
        self.assertNotIn('lootWaitAt', row)
        w.tick(); self.assertEqual(row['lootWaitAt'], p.time)

    def test_unknown_or_reserved_corpse_movement_cannot_gain_retry_from_later_nearby_pose(self):
        for mode in ('lost_nonarrival','reserved'):
            with self.subTest(mode=mode):
                p, w, row = corpse_party()
                if mode == 'lost_nonarrival': p.rpcs[4].mode = mode
                w.tick()
                if mode == 'reserved': row['lootApproaches'][0]['phase'] = 'reserved'
                saved = copy.deepcopy(w.data); calls = len(p.routes()); restored = p.worker(stored=saved)
                restored.tick()
                self.assertTrue(restored.done); self.assertEqual(len(p.routes()), calls)
                self.assertNotIn('lootWaitAt', restored.data['encounters'][0])

    def test_expected_bound_loot_starts_after_release_without_invalidating_actual_arrival(self):
        for foreign in (False, True):
            with self.subTest(foreign=foreign):
                p,w,row=corpse_party()
                def begin_loot(actor,method,params):
                    if actor.guid==5 and method=='buddy-actions.release_interaction':
                        p.rpcs[4].loot.update(phase='waiting_item',pendingCorpseGuid=str(TARGET+1 if foreign else TARGET))
                        p.rpcs[4].actions['interactionOwner']='loot'
                p.hook=begin_loot;w.tick()
                if foreign:
                    self.assertTrue(w.done);self.assertEqual(row['lootApproaches'][0]['phase'],'yielded')
                else:
                    self.assertFalse(w.done);self.assertEqual(row['lootApproaches'][0]['phase'],'arrived')
                    w.tick();self.assertFalse(w.done);self.assertIn('lootWaitAt',row)
                    self.assertEqual(w.status['phase'],'waiting_for_each_own_server_permitted_loot')
                self.assertEqual(len(p.routes()),3)

    def test_near_corpse_loot_clock_pauses_during_actual_combat_without_resetting_history(self):
        p,w,row=corpse_party()
        for a in p.rpcs.values():a.actions['position'].update(UPPER_CORPSE)
        p.time=1;w.tick();self.assertEqual(row['lootWaitAt'],1)
        p.time=20;p.rpcs[1].self['unitFlags']|=0x80000;w.tick()
        self.assertFalse(w.done);self.assertEqual(row['lootWaitPauseAt'],20)
        p.time=80;p.rpcs[1].self['unitFlags']&=~0x80000;w.tick()
        self.assertFalse(w.done);self.assertEqual(row['lootWaitAt'],61)
        self.assertNotIn('lootWaitPauseAt',row);self.assertEqual(p.routes(),[])
        p.time=98;w.tick();self.assertTrue(w.done)

    def test_known_partial_corpse_movement_has_three_attempt_cap_and_missing_target_never_moves(self):
        p, w, row = corpse_party(priority=True)
        for n in range(3):
            if n: p.recover(); p.rpcs[1].mode = 'priority'; p.rpcs[4].mode = p.rpcs[5].mode = 'normal_cancel'
            w.tick(); self.assertFalse(w.done)
        p.recover(); w.tick(); self.assertTrue(w.done)
        self.assertEqual(len(p.routes()), 9); self.assertEqual(len(row['lootApproaches']), 3)
        p, w, row = corpse_party(priority=True); w.tick(); p.recover()
        for actor in p.rpcs.values():
            original = actor.world
            def gone(params,original=original):
                v=original(params);v.pop('requestedCreature',None);return v
            actor.world = gone
        calls=len(p.routes());w.tick();self.assertTrue(w.done);self.assertEqual(len(p.routes()),calls)



class FreshCorridorReconnectTests(unittest.TestCase):
    def test_upper_floor_corpse_pickup_then_no_candidates_returns_via_new_verified_goal(self):
        p,w,row=corpse_party();w.tick()
        self.assertEqual(row['lootApproaches'][-1]['phase'],'arrived')
        for a in p.rpcs.values():a.lootable=[];a.target['dynamicFlags']=8
        w.tick();p.time=3;w.tick();self.assertEqual(row['phase'],'settled')
        before=copy.deepcopy(row);w.tick();w.tick()
        self.assertFalse(w.done);self.assertEqual(len(p.routes()),6)
        leg=w.data['search']['legs'][-1]
        self.assertEqual(leg['phase'],'arrived');self.assertEqual(leg['reanchor']['corpseTargetGuid'],TARGET)
        self.assertEqual(row,before)
        self.assertEqual(leg['station'],2)
        self.assertEqual({tuple(v[k] for k in ('x','y','z')) for _,_,v in p.routes()[3:]},{cave.SEARCH_STATIONS[2]})

    def test_closed_arrived_cursor_reanchors_after_observed_normal_external_walk(self):
        p=SearchParty();w=p.worker();p.scan(w);old=copy.deepcopy(w.data['search']['legs'][0])
        for a in p.rpcs.values():a.actions['position'].update(cave.station(8),revision='10')
        p.scan(w)
        self.assertFalse(w.done);self.assertEqual(w.data['search']['cursor'],8)
        self.assertEqual(w.data['search']['legs'][0],old)
        new=w.data['search']['legs'][-1]
        self.assertEqual(new['reanchor']['priorCursor'],5);self.assertEqual(new['station'],8)
        self.assertEqual(w.search_legs,2)
        self.assertEqual(new['before']['1']['x'],cave.SEARCH_STATIONS[8][0])

    def test_unknown_scope_or_unbounded_origin_never_reanchors(self):
        for mode in ('reserved','yielded','too_far','no_closed_corpse'):
            with self.subTest(mode=mode):
                p=SearchParty();w=p.worker()
                if mode!='no_closed_corpse':p.scan(w)
                if mode in ('reserved','yielded'):w.data['search']['legs'][-1]['phase']=mode
                for a in p.rpcs.values():
                    a.actions['position'].update(UPPER_CORPSE if mode=='no_closed_corpse' else cave.station(8),revision='20')
                    if mode=='too_far':a.actions['position']['x']=-9700
                count=len(p.routes())
                if mode in ('reserved','yielded'):w.tick()
                else:p.scan(w)
                self.assertTrue(w.done);self.assertEqual(len(p.routes()),count)

    def test_known_stopped_search_never_blocks_processing_a_new_owned_encounter(self):
        p=PriorityParty();w=p.worker();p.scan(w);p.recover()
        for a in p.rpcs.values():a.target.update(cave.station(4),present=True);a.target['x']+=5
        w.tick();self.assertEqual(w.data['encounters'][0]['phase'],'active')
        p.kill();w.tick()
        self.assertEqual(w.data['encounters'][0]['phase'],'loot')
        self.assertNotEqual(w.status['phase'],'waiting_for_safe_party_after_known_corridor_preemption')



if __name__=='__main__':unittest.main()
