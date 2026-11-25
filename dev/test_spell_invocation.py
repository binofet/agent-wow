"""Generic learned casts through the actual Lua VM; no game transport."""
import copy
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import subprocess
import unittest

ROOT=Path(__file__).resolve().parent.parent
loader=importlib.machinery.SourceFileLoader('spell_invocation_runtime',str(ROOT/'dev/task-runtime'))
spec=importlib.util.spec_from_loader(loader.name,loader)
runtime=importlib.util.module_from_spec(spec);loader.exec_module(runtime)


class SpellInvocationPolicyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        binary=Path(os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY',ROOT/'data/bin/luapolicy'))
        if not binary.is_file():raise RuntimeError('actual luapolicy evaluator required')
        cls.bridge=runtime.Bridge(binary,ROOT/'modules',source_bytes=8*1024*1024,value_bytes=8*1024*1024,execution_ms=1000)

    def obs(self):
        return dict(sessionId='spell-body',selfGuid='4',visitId='1',mapId=0,hostOwned=True,
            spellbookKnown=True,spells={},targets={},players={'4':dict(guid='4',present=True,
            aliveKnown=True,alive=True,mapKnown=True,mapId=0,positionKnown=True,x=1,y=2,z=3)})

    def run_policy(self,events=None,checkpoint=None,obs=None,pending=None,select=True,composed=False,sequence="2",clock_ms=1000):
        if composed:
            if checkpoint:
                checkpoint=copy.deepcopy(checkpoint)
                checkpoint.update(session_id='spell-body',cursor=sequence,pending=pending or [])
            source=subprocess.check_output(['python3','-B',str(ROOT/'dev/encounter-bundle')],text=True)
            setup='local p=(function()\n'+source+'\nend)()\n'
            body='if input.checkpoint then assert(p.import_state(input.checkpoint)) end; '
            body+='for _,e in ipairs(input.events) do assert(p.on_event(e)) end; '
            body+='local out=p.decide(input.frame);return {output=out,checkpoint=p.export_state()}'
        else:
            source=(ROOT/'behaviors/spell-invocation.lua').read_text()
            setup='local p=(function()\n'+source+'\nend)()\n'
            body='if input.checkpoint then assert(p.import_state(input.checkpoint)) end; '
            body+='for _,e in ipairs(input.events) do assert(p.on_event(e)) end;p.capture(input.frame); '
            body+='local a,s=p.propose(input.frame,input.obs,"2");if input.select then p.commit(a) end; '
            body+='return {output={action=a,spellInvocation=s},checkpoint=p.export_state()}'
        value=dict(events=events or [],checkpoint=checkpoint,obs=obs or self.obs(),select=select,
            frame=dict(session_id='spell-body',cursor=sequence,pending=pending or [],
                input=dict(kind='encounter',observationSequence=sequence),clock=dict(monotonicMs=clock_ms,unixMs=clock_ms)))
        return self.bridge.evaluate(setup+'return {decide=function(input) '+body+' end}',value)

    def events(self,spell=2120,**fields):
        return [dict(kind='encounter_observation',sequence='2',value=self.obs()),
            dict(kind='command',sequence='3',value=dict(task='cast_spell',id='explicit-1',spellId=spell,**fields))]

    def support_obs(self, mode):
        obs=self.obs()
        obs.update(worldObservationRevision='12',meleeKnown=True,
            party=dict(group=dict(known=True,inGroup=True,groupGuid='99',leaderGuid='1',
                members=[dict(guid='4',online=True),dict(guid='1',online=True)])))
        patient=dict(guid='1',present=True,aliveKnown=True,alive=mode!='resurrect',healthKnown=True,
            health=0 if mode=='resurrect' else 20,maxHealth=100,mapKnown=True,mapId=0,
            positionKnown=True,x=51 if mode!='near_heal' else 6,y=2,z=3)
        obs['players']['1']=patient
        obs['players']['4'].update(classKnown=True,classId=2,movementKnown=True,healthKnown=True,health=100,maxHealth=100)
        obs['targets']={'77':dict(guid='77',present=True,aliveKnown=True,alive=True,healthKnown=True,health=100,
            maxHealth=100,mapKnown=True,mapId=0,positionKnown=True,x=80,y=2,z=3,unitFlagsKnown=True,unitFlags=0,
            attackable=True)}
        obs['targetMovement']=dict(targetGuid='1',visitId='1',rangeYards=30,requireLos=True,
            ready=False,phase='follow_navigation_unavailable')
        if mode=='resurrect':
            obs['spells']={'7328':dict(known=True,ready=True,metadataKnown=True,capability='resurrect',maxRange=30)}
            obs['support']=dict(casterInCombat=False,resurrections=[dict(patientGuid='1',castTargetGuid='1',
                spellId=7328,rangeYards=30,ready=False,canApproach=True)])
        else:
            obs['spells']={'635':dict(known=True,ready=True,available=True,metadataKnown=True,capability='heal',maxRange=30)}
            obs['healing']=dict(sessionId='spell-body',caster=dict(guid='4',ready=True,mana=100),
                patients=[dict(guid='1',health=20,maxHealth=100,distance=5 if mode=='near_heal' else 50)],
                spells=[dict(spellId=635,rank=1,level=1,cost=10,amount=50,castMs=1500,minRange=0,maxRange=30)])
        return obs

    def encounter_event(self,obs,sequence='2'):
        return dict(kind='encounter_observation',sequence=sequence,value=obs)

    def spell_request(self,id='owner-seal'):
        return dict(kind='command',sequence='3',value=dict(task='cast_spell',id=id,spellId=21084))

    def test_owner_spell_precedes_real_automatic_heal_and_unreachable_support_approaches(self):
        for mode in ('far_heal','resurrect','near_heal'):
            with self.subTest(mode=mode):
                obs=self.support_obs(mode)
                baseline=self.run_policy([self.encounter_event(obs)],composed=True)
                auto=baseline['output']['action']
                self.assertEqual(auto['capability'],'heal' if mode=='near_heal' else 'approach_target')
                self.assertEqual(auto['targetGuid'],'1')
                if mode!='near_heal':
                    self.assertEqual(auto['rangeYards'],30);self.assertTrue(auto['requireLos'])
                requested=self.run_policy([self.encounter_event(obs),self.spell_request()],composed=True)
                self.assertEqual(requested['output']['action']['capability'],'cast_spell')
                self.assertEqual(requested['output']['action']['spellId'],21084)
                self.assertEqual(requested['output']['queue'],baseline['output']['queue'])
                self.assertIn('77',[row['targetGuid'] for row in requested['output']['queue']])
                self.assertTrue(requested['checkpoint']['data']['spellInvocation']['data']['current']['issued'])

    def test_retained_request_survives_full_reload_pending_native_cast_then_gets_one_turn(self):
        obs=self.support_obs('far_heal')
        waiting=self.run_policy([self.encounter_event(obs),self.spell_request()],
            pending=[dict(action_id='71',outcome='unknown')],composed=True)
        self.assertNotIn('action',waiting['output'])
        task=waiting['checkpoint']['data']['spellInvocation']['data']['current']
        self.assertFalse(task.get('issued',False))
        settled=dict(kind='action_result',sequence='4',receipt=dict(action_id='71',outcome='settled_unverified',
            payload=dict(capability='heal',target_guid='1',spell_id=635)))
        chosen=self.run_policy([settled,self.encounter_event(obs,'5')],checkpoint=waiting['checkpoint'],
            composed=True,sequence='5')
        self.assertEqual(chosen['output']['action']['capability'],'cast_spell')
        self.assertEqual(chosen['output']['action']['spellId'],21084)
        unknown=dict(kind='action_result',sequence='6',receipt=dict(action_id='72',outcome='unknown',
            payload=dict(capability='cast_spell',spell_id=21084)))
        held=self.run_policy([unknown,self.encounter_event(obs,'7')],checkpoint=chosen['checkpoint'],
            pending=[dict(action_id='72',outcome='unknown')],composed=True,sequence='7')
        self.assertNotIn('action',held['output'])
        current=held['checkpoint']['data']['spellInvocation']['data']['current']
        self.assertTrue(current['issued']);self.assertEqual(current['phase'],'outcome_unknown')

    def test_automatic_help_resumes_after_one_explicit_failure_and_new_intent_gets_its_turn(self):
        obs=self.support_obs('near_heal')
        first=self.run_policy([self.encounter_event(obs),self.spell_request()],composed=True)
        failure=dict(kind='action_result',sequence='4',receipt=dict(action_id='72',outcome='failed',
            payload=dict(capability='cast_spell',spell_id=21084,phase='server_rejected_no_replay',failure=85)))
        resumed=self.run_policy([failure,self.encounter_event(obs,'5')],checkpoint=first['checkpoint'],composed=True,sequence='5')
        self.assertEqual(resumed['output']['action']['capability'],'heal')
        self.assertNotIn('current',resumed['checkpoint']['data']['spellInvocation']['data'])
        next_request=self.spell_request('owner-seal-after-full-mana')
        next_request['sequence']='7'
        fresh=self.run_policy([self.encounter_event(obs,'6'),next_request],checkpoint=resumed['checkpoint'],composed=True,sequence='6')
        self.assertEqual(fresh['output']['action']['capability'],'cast_spell')

    def test_final_outer_release_preempts_without_committing_or_losing_explicit_request(self):
        obs=self.support_obs('far_heal');obs['interaction']=dict(owner='training',token='finite-owned-lease')
        yielded=self.run_policy([self.encounter_event(obs),self.spell_request()],composed=True)
        self.assertEqual(yielded['output']['action']['capability'],'release_interaction')
        current=yielded['checkpoint']['data']['spellInvocation']['data']['current']
        self.assertFalse(current.get('issued',False))
        obs.pop('interaction')
        selected=self.run_policy([self.encounter_event(obs,'3')],checkpoint=yielded['checkpoint'],composed=True,sequence='3')
        self.assertEqual(selected['output']['action']['capability'],'cast_spell')

    def stationary_obs(self, moving=False, remaining=None, owner='spell:stationary-1'):
        obs=self.obs()
        obs['players']['4'].update(movementKnown=True,moving=moving)
        obs['navigation']=dict(movementOwner={})
        if remaining is not None:
            obs['navigation']['movementOwner']=dict(active=True,owner=owner,remainingMs=str(remaining))
        return obs

    def stationary_request(self, **fields):
        return dict(kind='command',sequence='3',value=dict(task='cast_spell',id='stationary-1',spellId=8427,
            requirements=dict(stationary=True),targets=dict(mask=64,destination=dict(x=1,y=2,z=3)),**fields))

    def prep_receipt(self, mode='acquire', outcome='confirmed', action_id='41', **payload):
        data=dict(capability='movement_owner',mode=mode,owner='spell:stationary-1')
        if mode!='release':data['result']=dict(token='exact-finite-token')
        data.update(payload)
        return dict(kind='action_result',receipt=dict(action_id=action_id,outcome=outcome,payload=data))

    def geometry_obs(self, ready=False):
        obs=self.stationary_obs(remaining=2900,owner='spell:geometry-1')
        obs['hostTargetRangeBandSupported']=True
        obs['players']['4'].update(x=0,y=0,z=0,orientation=3.141592653589793)
        obs['targets']={'77':dict(guid='77',present=True,aliveKnown=True,alive=True,
            positionKnown=True,mapKnown=True,mapId=0,x=24,y=0,z=0,orientation=0,entry=123,
            attackable=True,engaged=True)}
        obs['spells']={'10312':dict(known=True,metadataKnown=True,maxRange=30,minRange=0,castTimeMs=0)}
        obs['targetMovement']=dict(targetGuid='77',visitId='1',rangeYards=25,minRangeYards=15,
            clearanceYards=0,requireLos=True,ready=ready)
        return obs

    def geometry_request(self, position=None):
        return dict(kind='command',sequence='3',value=dict(task='cast_spell',id='geometry-1',spellId=10312,
            targets=dict(mask=2,objectGuid='77'),requirements=dict(position=position or dict(minRangeYards=15,maxRangeYards=25))))

    def geometry_acquired(self, obs=None):
        obs=obs or self.geometry_obs()
        obs['navigation']['movementOwner']={}
        first=self.run_policy([self.encounter_event(obs),self.geometry_request()],composed=True)
        self.assertEqual(first['output']['action']['capability'],'movement_owner')
        self.assertEqual(first['output']['action']['mode'],'acquire')
        obs['navigation']['movementOwner']=dict(active=True,owner='spell:geometry-1',remainingMs=2900)
        receipt=self.prep_receipt();receipt['receipt']['payload']['owner']='spell:geometry-1'
        return obs,first,receipt

    def test_opt_in_unit_preparation_aims_with_floor_then_casts_only_after_ready(self):
        obs,first,receipt=self.geometry_acquired()
        aim=self.run_policy([receipt,self.encounter_event(obs,'4')],checkpoint=first['checkpoint'],composed=True,sequence='4')
        action=aim['output']['action']
        self.assertEqual(action['capability'],'approach_target')
        self.assertEqual(action['targetGuid'],'77')
        self.assertEqual(action['minRangeYards'],15)
        self.assertTrue(action['requireLos'])
        # A stale ready bit does not override current backward facing.
        obs['targetMovement']['ready']=True
        wrong=self.run_policy([self.encounter_event(obs,'5')],checkpoint=aim['checkpoint'],composed=True,sequence='5')
        self.assertNotEqual(wrong['output'].get('action',{}).get('capability'),'cast_spell')
        obs['players']['4']['orientation']=0
        cast=self.run_policy([self.encounter_event(obs,'6')],checkpoint=wrong['checkpoint'],composed=True,sequence='6')
        self.assertEqual(cast['output']['action']['capability'],'cast_spell')
        self.assertEqual(cast['output']['action']['spellId'],10312)
        again=self.run_policy([self.encounter_event(obs,'7')],checkpoint=cast['checkpoint'],composed=True,sequence='7')
        self.assertNotEqual(again['output'].get('action',{}).get('capability'),'cast_spell')

    def test_geometry_default_try_now_and_unsupported_unknown_target_timeout(self):
        obs=self.geometry_obs()
        plain=self.run_policy([self.encounter_event(obs),dict(kind='command',sequence='3',value=dict(
            task='cast_spell',id='try-now',spellId=10312,targets=dict(mask=2,objectGuid='77')))],composed=True)
        self.assertEqual(plain['output']['action']['capability'],'cast_spell')
        for mode in ('unsupported','estimated','unknown_position','changed_visit'):
            with self.subTest(mode=mode):
                obs,first,receipt=self.geometry_acquired()
                if mode=='unsupported':obs['hostTargetRangeBandSupported']=False
                elif mode=='estimated':obs['targets']['77']['positionEstimated']=True
                elif mode=='unknown_position':obs['targets']['77']['positionKnown']=False
                else:obs['visitId']='2'
                hold=self.run_policy([receipt,self.encounter_event(obs,'4')],checkpoint=first['checkpoint'],composed=True,sequence='4')
                self.assertNotEqual(hold['output'].get('action',{}).get('capability'),'cast_spell')
                expired=self.run_policy([self.encounter_event(obs,'5')],checkpoint=hold['checkpoint'],composed=True,sequence='5',clock_ms=10000)
                self.assertNotEqual(expired['output'].get('action',{}).get('capability'),'cast_spell')
                if mode!='changed_visit':
                    self.assertEqual(expired['checkpoint']['data']['spellInvocation']['data']['last']['phase'],'positioning_timeout')
                    self.assertEqual(expired['output']['action']['capability'],'movement_owner')
                    self.assertEqual(expired['output']['action']['mode'],'release')

    def test_ranged_scope_preempts_aim_with_its_own_hazard_floor(self):
        obs=self.geometry_obs()
        obs['targets']['77'].update(x=3,entry=123)
        obs['targets']['78']=dict(obs['targets']['77'],guid='78',x=40)
        obs['navigation']['movementOwner']={}
        directive=dict(kind='command',sequence='3',value=dict(task='ranged_only',id='hazard-band',
            creatureEntry=123,mapId=0,visitId='1',minRangeYards=15,maxRangeYards=25,clearanceYards=2,durationMs=60000,enabled=True))
        first=self.run_policy([self.encounter_event(obs),directive,self.geometry_request()],composed=True)
        self.assertEqual(first['output']['action']['owner'],'ranged:hazard-band')
        self.assertNotEqual(first['output']['action']['capability'],'cast_spell')
        obs['navigation']['movementOwner']=dict(active=True,owner='ranged:hazard-band',remainingMs=50000)
        receipt=self.prep_receipt();receipt['receipt']['payload']['owner']='ranged:hazard-band'
        receipt['receipt']['payload']['result']['state']=dict(sessionId=obs['sessionId'],selfGuid=obs['selfGuid'],movementOwner=dict(active=True,owner='ranged:hazard-band'))
        step=self.run_policy([receipt,self.encounter_event(obs,'4')],checkpoint=first['checkpoint'],composed=True,sequence='4')
        action=step['output']['action']
        self.assertEqual(action['capability'],'approach_target')
        self.assertEqual(action['minRangeYards'],17)
        self.assertEqual(action['clearanceYards'],17)
        self.assertEqual(action['clearanceTargetGuids'],['77','78'])
        self.assertNotEqual(action['capability'],'cast_spell')

    def test_geometry_deadline_stops_owned_motion_before_releasing_lease(self):
        obs,first,receipt=self.geometry_acquired()
        obs['players']['4']['moving']=True
        stop=self.run_policy([receipt,self.encounter_event(obs,'4')],checkpoint=first['checkpoint'],composed=True,sequence='4',clock_ms=10000)
        self.assertEqual(stop['output']['action']['capability'],'stop_movement')
        self.assertEqual(stop['output']['action']['targetGuid'],'4')
        obs['players']['4']['moving']=False
        stopped=dict(kind='action_result',receipt=dict(action_id='42',outcome='settled_unverified',
            payload=dict(capability='stop_movement',target_guid='4',visit_id='1')))
        done=self.run_policy([stopped,self.encounter_event(obs,'5')],checkpoint=stop['checkpoint'],composed=True,sequence='5',clock_ms=10001)
        self.assertEqual(done['output']['action']['capability'],'movement_owner')
        self.assertEqual(done['output']['action']['mode'],'release')
        self.assertEqual(done['checkpoint']['data']['spellInvocation']['data']['last']['phase'],'positioning_timeout')

    def test_geometry_requires_positive_minimum_and_unknown_pending_is_not_replayed(self):
        obs=self.geometry_obs()
        for minimum in (0,-1):
            denied=self.run_policy([self.encounter_event(obs),self.geometry_request(dict(minRangeYards=minimum,maxRangeYards=25))],composed=True)
            self.assertNotEqual(denied['output'].get('action',{}).get('capability'),'cast_spell')
        obs,first,receipt=self.geometry_acquired()
        pending=[dict(action_id='pending-motion',outcome='unknown')]
        held=self.run_policy([receipt,self.encounter_event(obs,'4')],checkpoint=first['checkpoint'],pending=pending,composed=True,sequence='4')
        self.assertNotEqual(held['output'].get('action',{}).get('capability'),'cast_spell')
        self.assertNotEqual(held['output'].get('action',{}).get('capability'),'approach_target')
        obs['navigation']['motionUnconfirmed']=True
        uncertain=self.run_policy([receipt,self.encounter_event(obs,'5')],checkpoint=first['checkpoint'],composed=True,sequence='5',clock_ms=10000)
        self.assertNotEqual(uncertain['output'].get('action',{}).get('capability'),'cast_spell')
        self.assertNotEqual(uncertain['output'].get('action',{}).get('mode'),'release')
        self.assertIn('current',uncertain['checkpoint']['data']['spellInvocation']['data'])

    def prepared_cast(self):
        obs=self.stationary_obs()
        acquired=self.run_policy([self.encounter_event(obs),self.stationary_request()],composed=True)
        self.assertEqual(acquired['output']['action']['mode'],'acquire')
        obs=self.stationary_obs(remaining=2900)
        cast=self.run_policy([self.prep_receipt(),self.encounter_event(obs,'4')],
            checkpoint=acquired['checkpoint'],composed=True,obs=obs,sequence='4')
        self.assertEqual(cast['output']['action']['capability'],'cast_spell')
        return cast

    def test_moving_ground_cast_stops_then_observes_stationary_acquires_and_casts(self):
        obs=self.stationary_obs(moving=True)
        first=self.run_policy([self.encounter_event(obs),self.stationary_request()],composed=True)
        action=first['output']['action']
        self.assertEqual(action['capability'],'stop_movement')
        self.assertEqual(action['targetGuid'],'4')
        self.assertNotIn('issued',first['checkpoint']['data']['spellInvocation']['data']['current'])
        stop=dict(kind='action_result',receipt=dict(action_id='40',outcome='settled_unverified',
            payload=dict(capability='stop_movement',target_guid='4',visit_id='1',transport_written=True)))
        obs['navigation']['motionUnconfirmed']=True
        uncertain=self.run_policy([stop,self.encounter_event(obs,'4')],checkpoint=first['checkpoint'],
            composed=True,sequence='4')
        self.assertNotIn('action',uncertain['output'])
        repeat=self.run_policy([self.encounter_event(obs,'5')],checkpoint=uncertain['checkpoint'],composed=True,sequence='5')
        self.assertNotIn('action',repeat['output']) # Unknown STOP is never replayed after reload.
        obs=self.stationary_obs()
        acquire=self.run_policy([self.encounter_event(obs,'6')],checkpoint=repeat['checkpoint'],composed=True,sequence='6')
        self.assertEqual(acquire['output']['action']['mode'],'acquire')
        self.assertEqual(acquire['output']['action']['leaseMs'],3000)
        obs=self.stationary_obs(remaining=2900)
        cast=self.run_policy([self.prep_receipt(),self.encounter_event(obs,'7')],checkpoint=acquire['checkpoint'],composed=True,sequence='7')
        self.assertEqual(cast['output']['action']['capability'],'cast_spell')
        self.assertEqual(cast['output']['action']['targets']['mask'],64)
        self.assertTrue(cast['checkpoint']['data']['spellInvocation']['data']['current']['issued'])

    def test_channel_pending_renews_finite_lease_and_end_releases_without_replaying(self):
        cast=self.prepared_cast();pending=[dict(action_id='51',outcome='unknown')]
        obs=self.stationary_obs(remaining=1000)
        renew=self.run_policy([self.encounter_event(obs,'5')],checkpoint=cast['checkpoint'],pending=pending,
            composed=True,sequence='5',clock_ms=3000)
        self.assertEqual(renew['output']['action']['capability'],'movement_owner')
        self.assertEqual(renew['output']['action']['mode'],'renew')
        self.assertEqual(renew['output']['action']['token'],'exact-finite-token')
        current=renew['checkpoint']['data']['spellInvocation']['data']['current']
        self.assertTrue(current['issued']);self.assertEqual(current['actionId'],'51')
        stale=self.run_policy([self.prep_receipt('renew',action_id='52')],
            checkpoint=renew['checkpoint'],pending=pending,composed=True,sequence='5',clock_ms=3100)
        self.assertNotIn('action',stale['output']) # No renewal on the already-settled sample.
        obs=self.stationary_obs(remaining=2900)
        waiting=self.run_policy([self.encounter_event(obs,'6')],
            checkpoint=stale['checkpoint'],pending=pending,composed=True,sequence='6',clock_ms=3200)
        self.assertNotIn('action',waiting['output'])
        ended=dict(kind='action_result',receipt=dict(action_id='51',outcome='settled_unverified',
            payload=dict(capability='cast_spell',spell_id=8427,phase='channel_ended')))
        release=self.run_policy([ended,self.encounter_event(obs,'7')],checkpoint=waiting['checkpoint'],
            composed=True,sequence='7',clock_ms=4000)
        self.assertEqual(release['output']['action']['mode'],'release')
        data=release['checkpoint']['data']['spellInvocation']['data']
        self.assertNotIn('current',data);self.assertEqual(data['last']['receipt']['phase'],'channel_ended')
        obs=self.stationary_obs()
        done=self.run_policy([self.prep_receipt('release',action_id='53'),self.encounter_event(obs,'8'),self.stationary_request()],
            checkpoint=release['checkpoint'],composed=True,sequence='8',clock_ms=4100)
        self.assertNotIn('action',done['output'])
        self.assertNotIn('releasing',done['checkpoint']['data']['spellInvocation']['data'])

    def test_rejection_and_cancel_before_cast_release_only_the_owned_token(self):
        cast=self.prepared_cast();obs=self.stationary_obs(remaining=2500)
        rejection=dict(kind='action_result',receipt=dict(action_id='51',outcome='failed',
            payload=dict(capability='cast_spell',spell_id=8427,phase='server_rejected_no_replay')))
        release=self.run_policy([rejection,self.encounter_event(obs,'5')],checkpoint=cast['checkpoint'],composed=True,sequence='5')
        self.assertEqual(release['output']['action']['mode'],'release')
        self.assertEqual(release['output']['action']['token'],'exact-finite-token')
        # Cancellation after a grant but before dispatch has the same finite cleanup.
        start=self.run_policy([self.encounter_event(self.stationary_obs()),self.stationary_request()],composed=True)
        cancel=dict(kind='command',value=dict(task='cancel_spell_request',id='stationary-1'))
        stopped=self.run_policy([self.prep_receipt(),cancel,self.encounter_event(obs,'4')],checkpoint=start['checkpoint'],composed=True,sequence='4')
        self.assertEqual(stopped['output']['action']['mode'],'release')
        # A later owner's movement instruction is never released or reacquired.
        other=self.stationary_obs(remaining=2500,owner='task:newer-owner')
        replaced=self.run_policy([rejection,self.encounter_event(other,'5')],checkpoint=cast['checkpoint'],composed=True,sequence='5')
        self.assertNotIn('action',replaced['output'])
        self.assertNotIn('releasing',replaced['checkpoint']['data']['spellInvocation']['data'])

    def test_cancel_transport_beats_renewal_but_original_channel_keeps_hold_until_end(self):
        cast=self.prepared_cast();obs=self.stationary_obs(remaining=1000);pending=[dict(action_id='51',outcome='unknown')]
        cancel=dict(kind='command',value=dict(task='cancel_spell',id='cancel-pending',spellId=8427,
            expectedOperationRevision='987654321098765432',channel=True))
        chosen=self.run_policy([cancel,self.encounter_event(obs,'5')],checkpoint=cast['checkpoint'],pending=pending,composed=True,sequence='5')
        self.assertEqual(chosen['output']['action']['capability'],'cancel_spell')
        prep=chosen['checkpoint']['data']['spellInvocation']['data']['current']['preparation']
        self.assertNotIn('operation',prep) # Preempted renewal is not privately reserved.
        receipt=dict(kind='action_result',receipt=dict(action_id='52',outcome='settled_unverified',
            payload=dict(capability='cancel_spell',spell_id=8427,phase='cancel_transport_written')))
        held=self.run_policy([receipt,self.encounter_event(obs,'6')],checkpoint=chosen['checkpoint'],pending=pending,composed=True,sequence='6')
        self.assertEqual(held['output']['action']['mode'],'renew')
        self.assertTrue(held['checkpoint']['data']['spellInvocation']['data']['current']['issued'])

    def test_unknown_grant_allows_help_and_expiry_never_guesses_token_or_releases_later_owner(self):
        obs=self.stationary_obs()
        first=self.run_policy([self.encounter_event(obs),self.stationary_request()],composed=True)
        obs=self.support_obs('near_heal');obs['navigation']=dict(movementOwner=dict(active=True,owner='spell:stationary-1',remainingMs='2000'))
        unknown=self.prep_receipt(outcome='settled_unverified',result={})
        helping=self.run_policy([unknown,self.encounter_event(obs,'4')],checkpoint=first['checkpoint'],composed=True,sequence='4',clock_ms=2000)
        self.assertEqual(helping['output']['action']['capability'],'heal')
        self.assertNotIn('token',helping['checkpoint']['data']['spellInvocation']['data']['current']['preparation'])
        cancel=dict(kind='command',value=dict(task='cancel_spell_request',id='stationary-1'))
        other=self.stationary_obs(remaining=3000,owner='task:later-instruction')
        done=self.run_policy([cancel,self.encounter_event(other,'5')],checkpoint=helping['checkpoint'],composed=True,sequence='5',clock_ms=5000)
        self.assertNotIn('action',done['output'])
        self.assertNotIn('releasing',done['checkpoint']['data']['spellInvocation']['data'])

    def test_known_no_write_stop_retries_fresh_but_rejected_renewal_never_assumes_token(self):
        obs=self.stationary_obs(moving=True)
        first=self.run_policy([self.encounter_event(obs),self.stationary_request()],composed=True)
        refused=dict(kind='action_result',receipt=dict(action_id='40',outcome='not_dispatched',
            payload=dict(capability='stop_movement',target_guid='4',visit_id='1',transport_written=False)))
        retry=self.run_policy([refused,self.encounter_event(obs,'4')],checkpoint=first['checkpoint'],composed=True,sequence='4')
        self.assertEqual(retry['output']['action']['capability'],'stop_movement')
        cast=self.prepared_cast();obs=self.stationary_obs(remaining=1000)
        renewed=self.run_policy([self.encounter_event(obs,'5')],checkpoint=cast['checkpoint'],
            pending=[dict(action_id='51',outcome='unknown')],composed=True,sequence='5')
        # The same public owner label cannot prove our token survived a refusal.
        denied=self.prep_receipt('renew',outcome='not_dispatched',action_id='52',error='token replaced')
        held=self.run_policy([denied,self.encounter_event(obs,'6')],checkpoint=renewed['checkpoint'],
            pending=[dict(action_id='51',outcome='unknown')],composed=True,sequence='6')
        self.assertNotIn('action',held['output'])
        self.assertNotIn('token',held['checkpoint']['data']['spellInvocation']['data']['current']['preparation'])

    def test_death_visit_and_body_change_cleanup_does_not_touch_another_body(self):
        cast=self.prepared_cast()
        for change in ('dead','visit','body'):
            with self.subTest(change=change):
                obs=self.stationary_obs(remaining=2000)
                if change=='dead':obs['players']['4']['alive']=False
                elif change=='visit':obs['visitId']='2'
                else:obs['sessionId']='replacement-body'
                result=self.run_policy([self.encounter_event(obs,'5')],checkpoint=cast['checkpoint'],
                    pending=[dict(action_id='51',outcome='unknown')],composed=True,sequence='5')
                data=result['checkpoint']['data']['spellInvocation']['data']
                if change=='body':
                    self.assertNotIn('action',result['output'])
                    # Current-body gate prevents all mutations; the old lease
                    # expires natively instead of being sent to another body.
                else:
                    self.assertEqual(result['output']['action']['mode'],'release')
                    self.assertEqual(result['output']['action']['token'],'exact-finite-token')
                    self.assertNotIn('current',data)

    def test_final_outer_release_does_not_reserve_unselected_preparation(self):
        obs=self.support_obs('far_heal');obs['navigation']=dict(movementOwner={})
        obs['interaction']=dict(owner='training',token='current-training-token')
        first=self.run_policy([self.encounter_event(obs),self.stationary_request()],composed=True)
        self.assertEqual(first['output']['action']['capability'],'release_interaction')
        prep=first['checkpoint']['data']['spellInvocation']['data']['current']['preparation']
        self.assertNotIn('operation',prep)
        obs.pop('interaction')
        selected=self.run_policy([self.encounter_event(obs,'4')],checkpoint=first['checkpoint'],composed=True,sequence='4')
        self.assertEqual(selected['output']['action']['capability'],'movement_owner')
        self.assertEqual(selected['output']['action']['mode'],'acquire')

    def test_requirement_facts_opt_in_without_class_ids_and_instant_moving_cast_stays_direct(self):
        obs=self.stationary_obs(moving=True);obs['spells']={'4294967294':dict(castTimeMs=2000)}
        request=dict(kind='command',value=dict(task='cast_spell',id='newly-learned',spellId=4294967294))
        prepared=self.run_policy([self.encounter_event(obs),request],composed=True)
        self.assertEqual(prepared['output']['action']['capability'],'stop_movement')
        request['value']['requirements']=dict(stationary=False)
        direct=self.run_policy([self.encounter_event(obs),request],composed=True)
        self.assertEqual(direct['output']['action']['capability'],'cast_spell')
        obs['spells']={};request['value'].pop('requirements')
        instant=self.run_policy([self.encounter_event(obs),request],composed=True)
        self.assertEqual(instant['output']['action']['capability'],'cast_spell')

    def test_ground_flamestrike_blizzard_implicit_explosion_and_unfamiliar_learned_id(self):
        point=dict(x=123.25,y=-45.5,z=18.0)
        for spell,targets in ((2120,dict(mask=64,destination=point)),(10,dict(mask=64,destination=point)),
                (1449,None),(4294967294,dict(mask=64,destination=point))):
            with self.subTest(spell=spell):
                fields={} if targets is None else dict(targets=targets)
                result=self.run_policy(self.events(spell,**fields),composed=True)
                action=result['output']['action']
                self.assertEqual(action['capability'],'cast_spell')
                self.assertEqual(action['spellId'],spell)
                self.assertEqual(action['selfGuid'],'4');self.assertEqual(action['sessionId'],'spell-body')
                self.assertEqual(action['visitId'],'1')
                self.assertEqual(action.get('targets'),targets)
                self.assertNotIn('targetGuid',action)
                self.assertIn('encounterOwnership',result['output']) # No classified spell row required.

    def test_unselected_proposal_survives_reload_but_selected_unknown_never_replays(self):
        requested=self.run_policy(self.events(10,targets=dict(mask=64,destination=dict(x=1,y=2,z=3))),select=False)
        self.assertNotIn('issued',requested['checkpoint']['data']['current'])
        issued=self.run_policy(checkpoint=requested['checkpoint'])
        self.assertTrue(issued['checkpoint']['data']['current']['issued'])
        waiting=self.run_policy(checkpoint=issued['checkpoint'],pending=[dict(action_id='71',outcome='unknown')])
        self.assertNotIn('action',waiting['output'])
        receipt=dict(kind='action_result',sequence='4',receipt=dict(action_id='71',outcome='unknown',
            payload=dict(capability='cast_spell',spell_id=10,phase='transport_unknown')))
        unknown=self.run_policy([receipt],checkpoint=waiting['checkpoint'])
        self.assertEqual(unknown['checkpoint']['data']['current']['phase'],'outcome_unknown')
        duplicate=self.run_policy(self.events(10),checkpoint=unknown['checkpoint'])
        self.assertNotIn('action',duplicate['output'])
        wrong=copy.deepcopy(receipt);wrong['receipt'].update(action_id='72',outcome='confirmed')
        still=self.run_policy([wrong],checkpoint=duplicate['checkpoint'])
        self.assertEqual(still['checkpoint']['data']['current']['actionId'],'71')

    def test_transport_receipt_does_not_claim_effect_or_rearm_issued_id(self):
        first=self.run_policy(self.events(1449))
        receipt=dict(kind='action_result',receipt=dict(action_id='8',outcome='settled_unverified',
            payload=dict(capability='cast_spell',spell_id=1449,phase='server_cast_go',server_cast_go=True,effect_observed=False)))
        done=self.run_policy([receipt],checkpoint=first['checkpoint'])
        last=done['checkpoint']['data']['last']
        self.assertEqual(last['receipt']['outcome'],'settled_unverified')
        self.assertNotIn('effectConfirmed',last)
        retry=self.run_policy(self.events(1449),checkpoint=done['checkpoint'])
        self.assertNotIn('action',retry['output'])

    def test_cancel_before_dispatch_and_body_visit_change_do_not_cast(self):
        requested=self.run_policy(self.events(2120),select=False)
        cancel=dict(kind='command',value=dict(task='cancel_spell_request',id='explicit-1'))
        canceled=self.run_policy([cancel],checkpoint=requested['checkpoint'])
        self.assertNotIn('action',canceled['output'])
        for field,value in (('sessionId','replacement'),('visitId','2')):
            obs=self.obs();obs[field]=value
            moved=self.run_policy(checkpoint=requested['checkpoint'],obs=obs)
            self.assertNotIn('action',moved['output'])

    def test_explicit_cancel_is_selectable_with_pending_channel_without_retiring_it(self):
        first=self.run_policy(self.events(10),composed=True)
        cancel=dict(kind='command',sequence='4',value=dict(task='cancel_spell',id='cancel-1',spellId=10,
            expectedOperationRevision='987654321098765432',channel=True))
        pending=[dict(action_id='22',outcome='unknown')]
        issued=self.run_policy([cancel],checkpoint=first['checkpoint'],pending=pending,composed=True)
        action=issued['output']['action']
        self.assertEqual(action['capability'],'cancel_spell')
        self.assertEqual(action['expectedOperationRevision'],'987654321098765432')
        self.assertTrue(action['channel'])
        original=issued['checkpoint']['data']['spellInvocation']['data']['current']
        self.assertTrue(original['issued']);self.assertEqual(original['spellId'],10)
        receipt=dict(kind='action_result',sequence='5',receipt=dict(action_id='23',outcome='settled_unverified',
            payload=dict(capability='cancel_spell',spell_id=10,phase='cancel_transport_written')))
        settled=self.run_policy([receipt,cancel],checkpoint=issued['checkpoint'],pending=pending,composed=True)
        self.assertNotIn('action',settled['output'])
        data=settled['checkpoint']['data']['spellInvocation']['data']
        self.assertTrue(data['current']['issued'])
        self.assertEqual(data['lastCancel']['receipt']['outcome'],'settled_unverified')
        self.assertNotIn('cancel',data)

    def test_cancel_only_claims_body_without_a_classified_spell_or_module_cast_task(self):
        events=[dict(kind='encounter_observation',sequence='2',value=self.obs()),
            dict(kind='command',sequence='3',value=dict(task='cancel_spell',id='cancel-old-channel',
                spellId=10,expectedOperationRevision='987654321098765432',channel=True))]
        result=self.run_policy(events,pending=[dict(action_id='1',outcome='unknown')],composed=True)
        self.assertEqual(result['output']['action']['capability'],'cancel_spell')
        self.assertIn('encounterOwnership',result['output'])
        self.assertNotIn('current',result['checkpoint']['data']['spellInvocation']['data'])


if __name__=='__main__':unittest.main()
