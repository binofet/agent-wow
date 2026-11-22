"""Current owner receipt feedback through the real Conversation, without models/game actions."""
import importlib.machinery
import importlib.util
import json
from pathlib import Path
import tempfile
import threading
import unittest
from unittest.mock import patch

loader=importlib.machinery.SourceFileLoader('owner_ack_converse',str(Path(__file__).with_name('converse')))
spec=importlib.util.spec_from_loader(loader.name,loader)
c=importlib.util.module_from_spec(spec);loader.exec_module(c)


class OwnerAcknowledgementTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory(dir=Path(tempfile.gettempdir()).resolve())
        self.addCleanup(self.temp.cleanup)
        self.state=Path(self.temp.name)/'listener'
        class RPC:
            url='http://127.0.0.1:8090/rpc'
            def __init__(rpc):
                rpc.session='current-chat';rpc.rows=[];rpc.calls=[];rpc.ack_unknown=False;rpc.final_unknown=False
            def call(rpc,method,params,timeout=None):
                rpc.calls.append((method,dict(params)))
                if method=='buddy-chat.poll':
                    after=int(params.get('afterSequence',0));latest=int(rpc.rows[-1]['sequence']) if rpc.rows else 0
                    return dict(sessionId=rpc.session,latestSequence=str(latest),nextSequence=str(latest),
                        messages=[dict(row) for row in rpc.rows if int(row['sequence'])>after])
                if method=='buddy-chat.send':
                    if rpc.ack_unknown and params['text']==self.ack_text:
                        raise c.ConversationError('Chat transport outcome unknown')
                    if rpc.final_unknown and params['text']!=self.ack_text:
                        raise c.ConversationError('Final chat outcome unknown')
                    return dict(transportWritten=True)
                raise AssertionError('No gameplay RPC is allowed: '+method)
        self.rpc=RPC()
        self.ack_text='Got it—give me a moment.'
        outer=self
        class Brain:
            state_dir=self.state
            developer_workspace=None
            def __init__(brain):brain.calls=[];brain.error=None
            def generate(brain,history,state):
                brain.calls.append(list(history))
                outer.assertEqual(outer.sends()[0]['text'],outer.ack_text)
                if brain.error:raise brain.error
                return dict(text="The recovery result still needs checking.",action='NONE',emote='NONE')
        self.brain=Brain()

    def loop(self,**kwargs):
        return c.Conversation(self.rpc,self.brain,character_name='Lotharen',character_class='paladin',
            character_guid=87,party_agents={1:'Arlen',4:'Mira',5:'Rook',8:'Nixwick'},**kwargs)

    def add(self,text='gm revive the team',channel='WHISPER',sender=3,**fields):
        self.rpc.rows.append(dict(sequence=str(len(self.rpc.rows)+1),text=text,channel=channel,
            senderGuid=str(sender),receiverGuid='3',**fields))

    def sends(self):return [params for method,params in self.rpc.calls if method=='buddy-chat.send']

    def test_slow_brain_receives_ack_first_then_one_honest_final_response(self):
        loop=self.loop();loop.tick();self.add()
        entered,release=threading.Event(),threading.Event();original=self.brain.generate;errors=[]
        def slow(history,state):
            self.assertEqual(self.sends(),[dict(channel='WHISPER',language='COMMON',text=self.ack_text,recipient='Joel')])
            entered.set();self.assertTrue(release.wait(3));return original(history,state)
        self.brain.generate=slow
        def run():
            try:loop.tick()
            except BaseException as error:errors.append(error)
        worker=threading.Thread(target=run);worker.start()
        try:
            self.assertTrue(entered.wait(3));self.assertEqual(len(self.sends()),1)
        finally:release.set();worker.join(3)
        self.assertFalse(worker.is_alive());self.assertEqual(errors,[])
        self.assertEqual([p['text'] for p in self.sends()],[self.ack_text,'The recovery result still needs checking.'])
        loop.tick();self.assertEqual(len(self.sends()),2);self.assertEqual(len(self.brain.calls),1)

    def test_timeout_keeps_receipt_and_reports_uncertainty_without_replay(self):
        loop=self.loop();loop.tick();self.add();self.brain.error=c.ConversationError('model timed out')
        with self.assertRaisesRegex(c.ConversationError,'model timed out'):loop.tick()
        self.assertEqual([p['text'] for p in self.sends()],[self.ack_text,
            "I couldn't finish confirming that request. Its outcome still needs checking."])
        restarted=self.loop(resume_whispers=True);restarted.initialized=True
        restarted.session_id='current-chat';restarted.cursor=0
        restarted.tick()
        self.assertEqual(len(self.sends()),2);self.assertEqual(len(self.brain.calls),1)

    def test_unknown_ack_transport_does_not_retry_ack_or_owner_work(self):
        loop=self.loop();loop.tick();self.add();self.rpc.ack_unknown=True;loop.tick()
        self.assertEqual(len(self.sends()),2);self.assertEqual(len(self.brain.calls),1)
        again=self.loop();again.initialized=True;again.session_id='current-chat';again.tick()
        self.assertEqual(len(self.sends()),2);self.assertEqual(len(self.brain.calls),1)
        record=json.loads((self.state/'owner-acknowledgements/state.json').read_text())
        self.assertEqual(record['streams']['current-chat'],dict(sequence=1,outcome='transport_unconfirmed'))

    def test_real_retained_startup_does_not_repeat_work_after_both_chat_writes_are_unknown(self):
        loop=self.loop();loop.tick();self.add();self.rpc.ack_unknown=True;self.rpc.final_unknown=True
        with self.assertRaisesRegex(c.ConversationError,'Final chat outcome unknown'):loop.tick()
        self.assertEqual(len(self.brain.calls),1);self.assertEqual(len(self.sends()),2)
        # No WHISPER_INFORM was observed: the existing baseline legitimately
        # returns this retained input, which must still consult its reservation.
        restarted=self.loop(resume_whispers=True)
        self.assertFalse(restarted.initialized)
        restarted.tick()
        self.assertEqual(len(self.brain.calls),1);self.assertEqual(len(self.sends()),2)
        self.assertEqual(restarted.cursor,1)

    def test_known_team_quoted_unaddressed_foreign_and_stale_inputs_do_not_ack(self):
        # These retain existing conversation routing; only immediate receipt feedback is new.
        for text,channel,sender,fields in [
            ('Lotharen, help','PARTY',4,{}),('ordinary background','PARTY',3,{}),
            ('Mira, help','PARTY',3,{}),('"gm revive the team"','WHISPER',3,{}),
            ('> Lotharen, help','WHISPER',3,{}),('gm revive the team','WHISPER',3,{'receiverGuid':'99'}),
        ]:
            with self.subTest(text=text):
                self.rpc.rows=[];self.rpc.calls=[];loop=self.loop();loop.tick()
                row=dict(sequence='1',text=text,channel=channel,senderGuid=str(sender),receiverGuid='3');row.update(fields)
                self.rpc.rows.append(row)
                with patch.object(self.brain,'generate',return_value=dict(text='An ordinary reply.',action='NONE',emote='NONE')):
                    loop.tick()
                self.assertNotIn(self.ack_text,[p['text'] for p in self.sends()])
        self.rpc.rows=[];self.rpc.calls=[];self.add();loop=self.loop();loop.tick()
        self.assertEqual(self.sends(),[]);self.assertEqual(self.brain.calls,[])

    def test_explicit_public_address_acks_but_retained_startup_input_does_not(self):
        loop=self.loop();loop.tick();self.add('Lotharen, help us','PARTY');loop.tick()
        self.assertEqual(self.sends()[0]['channel'],'PARTY')
        self.assertEqual(self.sends()[0]['text'],self.ack_text)
        self.rpc.rows=[];self.rpc.calls=[];self.rpc.session='never-reserved-chat';self.add()
        loop=self.loop(resume_whispers=True)
        with patch.object(self.brain,'generate',return_value=dict(text='Current result needs checking.',action='NONE',emote='NONE')):
            loop.tick()
        self.assertEqual(len(self.sends()),1);self.assertNotEqual(self.sends()[0]['text'],self.ack_text)

    def test_new_owner_or_changed_body_before_ack_prevents_old_work(self):
        for changed in ('owner','body'):
            with self.subTest(changed=changed):
                self.rpc.rows=[];self.rpc.calls=[];loop=self.loop();loop.tick();self.add()
                original=self.rpc.call;checks=0
                def race(method,params,timeout=None):
                    nonlocal checks
                    if method=='buddy-chat.poll':
                        checks+=1
                        if checks==2:
                            if changed=='owner':self.add('Actually stop')
                            else:self.rpc.session='replacement-chat'
                    return original(method,params,timeout)
                with patch.object(self.rpc,'call',side_effect=race):loop.tick()
                self.assertEqual(self.sends(),[]);self.assertEqual(self.brain.calls,[])
                self.rpc.session='current-chat'

    def test_cancel_after_reservation_is_durable_and_never_sends_or_generates(self):
        stop=[False];loop=self.loop(stopped=lambda:stop[0]);loop.tick();self.add()
        reserve=c.owner_acknowledgement
        def cancelled(*args,**kwargs):
            result=reserve(*args,**kwargs)
            if result=='reserved':stop[0]=True
            return result
        with patch.object(c,'owner_acknowledgement',side_effect=cancelled):
            with self.assertRaises(c.Stopped):loop.tick()
        self.assertEqual(self.sends(),[]);self.assertEqual(self.brain.calls,[])
        stop[0]=False;again=self.loop();again.initialized=True;again.session_id='current-chat';again.tick()
        self.assertEqual(self.sends(),[]);self.assertEqual(self.brain.calls,[])


if __name__=='__main__':unittest.main()
