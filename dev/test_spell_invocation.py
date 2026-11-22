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

    def run_policy(self,events=None,checkpoint=None,obs=None,pending=None,select=True,composed=False,sequence="2"):
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
                input=dict(kind='encounter',observationSequence=sequence),clock=dict(monotonicMs=1000,unixMs=1000)))
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
