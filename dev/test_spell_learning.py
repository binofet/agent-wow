"""A coalesced same-body spellbook event wakes the ordinary brain, not banter."""
import copy
import hashlib
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT=Path(__file__).resolve().parent.parent
loader=importlib.machinery.SourceFileLoader('spell_learning_converse',str(ROOT/'dev/converse'))
spec=importlib.util.spec_from_loader(loader.name,loader)
c=importlib.util.module_from_spec(spec);loader.exec_module(c)


class RPC:
    url='http://127.0.0.1:8087/rpc'
    def __init__(self,source):
        self.sid,self.ids,self.revision,self.source='learn-body',{10},1,source
        self.calls,self.messages=[],[]
    def call(self,method,params,timeout=None):
        self.calls.append((method,copy.deepcopy(params)))
        if method=='buddy-actions.status':return dict(sessionId=self.sid,selfGuid='4')
        if method=='buddy-actions.spell_toolkit':return self.toolkit()
        if method=='buddy-party.state':return dict(sessionId=self.sid,group=dict(known=True,inGroup=False))
        if method=='buddy-chat.poll':return dict(sessionId=self.sid,latestSequence=str(len(self.messages)),
            nextSequence=str(len(self.messages)),messages=copy.deepcopy(self.messages))
        if method=='session.policy_source':return dict(sessionId=self.sid,characterGuid='4',source=self.source,
            revision=hashlib.sha256(self.source.encode()).hexdigest(),generation='1',sourceLimitBytes=8*1024*1024)
        if method=='session.attach':return dict(sessionId=self.sid,characterGuid='4',
            policy=dict(revision=hashlib.sha256(self.source.encode()).hexdigest(),generation='1'))
        if method=='buddy-world.snapshot':return dict(self=dict(guid='4',present=True,aliveKnown=True,alive=True))
        if method in ('buddy-chat.emotes','buddy-professions.state','buddy-inventory.state',
                'buddy-quests.state','buddy-loot.state','buddy-trade.state','buddy-quests.journal','buddy-quest-items.state'):
            return dict(sessionId=self.sid,selfGuid='4')
        raise AssertionError('Unexpected mutation: '+method)
    def toolkit(self):
        return dict(sessionId=self.sid,selfGuid='4',spellbookKnown=True,spellbookRevision=str(self.revision),
            spells=[dict(spellId=spell,metadataKnown=False,genericCastSupported=True) for spell in sorted(self.ids)])


class Brain:
    developer_workspace=ROOT
    def __init__(self,folder):self.state_dir=Path(folder);self.states=[];self.failure=None
    def generate(self,history,state):
        self.states.append(state)
        if self.failure:raise self.failure
        return dict(text='',action='NONE',emote='NONE')


class SpellLearningTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.binary=Path(os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY',ROOT/'data/bin/luapolicy'))
        if not cls.binary.is_file():raise RuntimeError('actual Lua evaluator required')
        cls.source=subprocess.check_output(['python3','-B',str(ROOT/'dev/encounter-bundle')],text=True)

    def setUp(self):
        self.temp=tempfile.TemporaryDirectory(dir=Path(tempfile.gettempdir()).resolve());self.addCleanup(self.temp.cleanup)
        self.clock=[100.]
        self.time_patch=patch.object(c.time,'monotonic',side_effect=lambda:self.clock[0])
        self.time_patch.start();self.addCleanup(self.time_patch.stop)
        original=c.evaluate_spell_learning
        self.eval_patch=patch.object(c,'evaluate_spell_learning',side_effect=lambda s,f,l:original(s,f,l,binary=self.binary))
        self.eval_patch.start();self.addCleanup(self.eval_patch.stop)
        self.rpc=RPC(self.source)
        self.controls=c.BuddyControls(self.rpc,character_class='mage',character_guid=4)
        self.controls.poll_combat=lambda:None
        self.brain=Brain(self.temp.name)
        self.status=[]
        self.loop=c.Conversation(self.rpc,self.brain,controls=self.controls,character_guid=4,character_name='Mira',
            character_class='mage',banter_seconds=0,status=lambda **v:self.status.append(v))
        self.loop.initialized=True;self.loop.session_id=self.rpc.sid
        self.loop.maybe_repair_mission=lambda:False;self.loop.maybe_review_combat=lambda:False

    def tick(self,advance=.1):self.clock[0]+=advance;self.loop.tick()

    def ready_notice(self):
        self.tick();self.rpc.ids.add(2120);self.rpc.revision+=1;self.tick()
        self.tick(1.);self.tick(3.)

    def test_training_batch_coalesces_and_wakes_once_with_banter_disabled(self):
        self.tick()
        self.assertIsNone(self.controls.learned_spell_notice)
        for spell in range(1000,1020):
            self.rpc.ids.add(spell);self.rpc.revision+=1;self.tick()
            self.assertEqual(self.brain.states,[])
        self.tick(1.);self.tick(3.)
        self.assertEqual(len(self.brain.states),1)
        state=self.brain.states[0]
        self.assertEqual(state['current_request']['source'],'spell_learning')
        self.assertEqual(state['learned_spell_changes']['spellIds'],list(range(1000,1020)))
        self.assertIs(type(state['current_request']),c.CodexBrain._SpellLearningRequest)
        self.assertIsNone(self.controls.learned_spell_notice)
        for _ in range(10):self.tick(1.)
        self.assertEqual(len(self.brain.states),1)
        self.assertFalse(any(m in ('buddy-chat.send','buddy-actions.cast_spell','session.reload_policy') for m,_ in self.rpc.calls))

    def test_initial_and_reconnected_spellbooks_do_not_create_model_storms(self):
        self.rpc.ids.update(range(100,150));self.tick()
        self.rpc.sid='replacement';self.rpc.ids.update(range(150,180));self.loop.session_id=self.rpc.sid
        for _ in range(4):self.tick(2.)
        self.assertEqual(self.brain.states,[])
        self.assertIsNone(self.controls.learned_spell_notice)

    def test_failed_review_is_reserved_once_and_never_replayed(self):
        self.brain.failure=c.ConversationError('actual model timeout')
        self.ready_notice()
        self.assertEqual(len(self.brain.states),1)
        rows=c.mission_fault_attempts(Path(self.temp.name)/'learned-spell-notices',4,self.rpc.url)
        self.assertEqual([r['outcome'] for r in rows],['unknown'])
        for _ in range(5):self.tick(5.)
        self.assertEqual(len(self.brain.states),1)

    def test_coalescing_is_same_session_accepted_lua_policy(self):
        self.rpc.source=self.source.replace('quiet_ms=3000,stable_observations=2','quiet_ms=10000,stable_observations=2')
        self.ready_notice()
        self.assertEqual(self.brain.states,[])
        self.tick(7.)
        self.assertEqual(len(self.brain.states),1)
        self.assertEqual(self.rpc.sid,'learn-body')

    def prepare_pending_notice(self):
        self.controls.refresh()
        self.rpc.ids.add(2120);self.rpc.revision+=1;self.controls.refresh()
        self.controls.refresh();self.controls.refresh();self.clock[0]+=4.

    def test_owner_input_and_changed_source_defer_before_model_reservation(self):
        self.prepare_pending_notice()
        self.rpc.messages=[dict(sequence='1',senderGuid='3',channel='WHISPER',text='Mira, hello')]
        self.assertTrue(self.loop.maybe_review_learned_spells())
        self.assertEqual(self.brain.states,[]);self.assertEqual(self.loop.cursor,0)
        self.rpc.messages=[]
        previous=c.evaluate_spell_learning
        def replace(source,frame,limit):
            result=previous(source,frame,limit)
            self.rpc.source+='\n-- actual intervening accepted source change'
            return result
        with patch.object(c,'evaluate_spell_learning',side_effect=replace):
            self.assertFalse(self.loop.maybe_review_learned_spells())
        self.assertEqual(self.brain.states,[])
        self.assertEqual(c.mission_fault_attempts(Path(self.temp.name)/'learned-spell-notices',4,self.rpc.url),[])

    def test_internal_learning_tools_require_the_exact_typed_body_bound_notice(self):
        self.prepare_pending_notice()
        notice=self.controls.learned_spell_notice
        self.loop.spell_learning_active=(self.rpc.sid,notice['key'])
        developer=self.loop.development_context('spell_learning')
        state=dict(character=dict(guid='4'),development=developer,
            current_request=c.CodexBrain._SpellLearningRequest(source='spell_learning',noticeKey=notice['key'],
                owned_learning_check=self.loop.spell_learning_current,controls_allowed=False))
        actual=c.CodexBrain('/bin/false',self.temp.name,developer_workspace=ROOT)
        self.assertTrue(actual.development_enabled(state))
        forged=dict(state,current_request=dict(state['current_request']))
        self.assertFalse(actual.development_enabled(forged))
        with patch.object(c.subprocess,'Popen',side_effect=AssertionError('model must not start')):
            with self.assertRaisesRegex(c.ConversationError,'binding changed'):actual.generate([],forged)
        self.rpc.sid='replacement'
        self.assertFalse(actual.development_enabled(state))

    def test_body_change_during_context_read_prevents_model_start(self):
        self.prepare_pending_notice()
        original=self.controls.context
        def changed():
            state=original();self.rpc.sid='replacement';return state
        self.controls.context=changed
        self.assertTrue(self.loop.maybe_review_learned_spells())
        self.assertEqual(self.brain.states,[])
        rows=c.mission_fault_attempts(Path(self.temp.name)/'learned-spell-notices',4,self.rpc.url)
        self.assertEqual([row['outcome'] for row in rows],['not_started'])


if __name__=='__main__':unittest.main()
