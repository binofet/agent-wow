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

    def run_policy(self,events=None,checkpoint=None,obs=None,pending=None,select=True,composed=False,
            sequence="2",input_fields=None):
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
                input=dict(dict(kind='encounter',observationSequence=sequence),**(input_fields or {})),
                clock=dict(monotonicMs=1000,unixMs=1000)))
        return self.bridge.evaluate(setup+'return {decide=function(input) '+body+' end}',value)

    def events(self,spell=2120,**fields):
        return [dict(kind='encounter_observation',sequence='2',value=self.obs()),
            dict(kind='command',sequence='3',value=dict(task='cast_spell',id='explicit-1',spellId=spell,**fields))]

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

    def roll_events(self,sequence="2",obs=None):
        offer=dict(rollGuid='4611686018427426929',revision='1',visitId='1',mapId=0,lootSlot=2,
            itemEntry=5000,itemCount=1,offeredMask=1,remainingMs='1',available=True,phase='pending')
        # Unknown metadata near the actual deadline selects Pass without a
        # fabricated scorer or any policy/module stub in the full bundle.
        view=dict(sessionId='spell-body',selfGuid='4',visitId='1',offers=[offer],metadata=[])
        return [dict(kind='encounter_observation',sequence=sequence,value=obs or self.obs()),
            dict(kind='loot_roll_observation',sequence=sequence,value=view)]

    def test_final_scheduler_preemption_does_not_reserve_an_unsent_roll(self):
        for winner in ('cancel_spell','loot_close','release_interaction'):
            with self.subTest(winner=winner):
                obs=self.obs()
                if winner=='release_interaction':
                    obs['interaction']=dict(owner='trade',token='actual-held-token')
                    obs['targets']={'10':dict(present=True,aliveKnown=True,alive=True,mapKnown=True,mapId=0,
                        attackable=True,attackObservation=dict(victimKnown=True,victimGuid='4',
                            targetKnown=True,targetGuid='4',ageMs='0'))}
                events=self.roll_events(obs=obs)
                if winner=='cancel_spell':
                    events.append(dict(kind='command',sequence='3',value=dict(task='cancel_spell',
                        id='cancel-before-roll',spellId=10,expectedOperationRevision='77',channel=True)))
                elif winner=='loot_close':
                    events.append(dict(kind='corpse_loot_observation',sequence='2',value=dict(
                        sessionId='spell-body',selfGuid='4',state=dict(pendingCorpseGuid='123'),
                        actions=dict(interactionOwner='loot'),window=dict(observed=True,
                            corpseGuid='123',openRequestId='actual-open',revision='1',slots=[]))))
                first=self.run_policy(events,composed=True,input_fields=dict(
                    observationSequences=dict(loot_roll='2',corpse_loot='2')))
                self.assertEqual(first['output']['action']['capability'],winner)
                self.assertFalse(first['checkpoint']['data']['lootRoll']['data']['reservations'])
                fresh=self.run_policy(self.roll_events('4'),checkpoint=first['checkpoint'],composed=True,
                    sequence='4',input_fields=dict(observationSequences=dict(loot_roll='4')))
                self.assertEqual(fresh['output']['action']['capability'],'loot_roll')
                self.assertEqual(fresh['output']['action']['choice'],'pass')
                reservations=fresh['checkpoint']['data']['lootRoll']['data']['reservations']
                self.assertEqual(list(reservations.values()),['pass'])
                unknown=dict(kind='action_result',sequence='5',receipt=dict(action_id='81',outcome='unknown',
                    payload=dict(capability='loot_roll',phase='transport_unconfirmed_no_retry')))
                retained=self.run_policy([unknown]+self.roll_events('6'),checkpoint=fresh['checkpoint'],
                    pending=[dict(action_id='81',outcome='unknown')],composed=True,sequence='6',
                    input_fields=dict(observationSequences=dict(loot_roll='6')))
                self.assertNotIn('action',retained['output'])
                self.assertEqual(retained['checkpoint']['data']['lootRoll']['data']['reservations'],reservations)
                duplicate=self.run_policy(self.roll_events('8'),checkpoint=retained['checkpoint'],composed=True,
                    sequence='8',input_fields=dict(observationSequences=dict(loot_roll='8')))
                self.assertNotIn('action',duplicate['output'])
                self.assertEqual(duplicate['checkpoint']['data']['lootRoll']['data']['reservations'],reservations)

    def test_pending_action_prevents_an_ordinary_roll_proposal(self):
        waiting=self.run_policy(self.roll_events(),pending=[dict(action_id='71',outcome='unknown')],
            composed=True,input_fields=dict(observationSequences=dict(loot_roll='2')))
        self.assertNotIn('action',waiting['output'])
        self.assertFalse(waiting['checkpoint']['data']['lootRoll']['data']['reservations'])
        fresh=self.run_policy(self.roll_events('4'),checkpoint=waiting['checkpoint'],composed=True,
            sequence='4',input_fields=dict(observationSequences=dict(loot_roll='4')))
        self.assertEqual(fresh['output']['action']['capability'],'loot_roll')


if __name__=='__main__':unittest.main()
