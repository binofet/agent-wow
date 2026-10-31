"""Mock-only fixed-party encounter, inventory and custody checks."""
import copy
import importlib.machinery
import importlib.util
import json
from pathlib import Path
import tempfile
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
            actor.actions['fargodeep']['active'] = False
            actor.self['unitFlags'] &= ~0x80000


class Actor:
    def __init__(self, party, guid):
        self.party, self.guid, self.session = party, guid, f'body-{guid}'
        self.calls = []
        self.self = dict(guid=str(guid), present=True, mapKnown=True, mapId=0, positionKnown=True,
            x=-9800., y=120., z=8., aliveKnown=True, alive=True, healthKnown=True, health=100,
            maxHealth=100, classKnown=True, classId=cave.ROSTER[guid][2], unitFlagsKnown=True,
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


if __name__=='__main__':unittest.main()
