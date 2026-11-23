"""Real DirectTools -> SessionTransport framing -> simulated native feedback."""
import copy
import json
from pathlib import Path
import struct
import subprocess
import tempfile
import unittest
from unittest import mock

import direct_tools
import test_direct_tools as fixtures


def packet(wire):
    assert wire[0] == 0 and struct.unpack('>I',wire[1:5])[0] == len(wire)-5
    def varint(offset):
        value=0;shift=0
        while True:
            byte=wire[offset];offset+=1;value|=(byte&127)<<shift
            if not byte&128:return value,offset
            shift+=7
    assert wire[5]==8
    opcode,index=varint(6)
    assert wire[index]==18
    size,index=varint(index+1)
    assert index+size==len(wire)
    return opcode,wire[index:]


class NativeRPC(fixtures.RPC):
    def __init__(self):
        super().__init__('87')
        self.selected='87';self.settle_selection=True;self.packets=[];self.commands=[]
        self.post_failure=None;self.feedback_enabled=True;self.cursor_lost=False;self.stale=False
        self.group_revision=1;self.after_selection_read_error=False
        self.allow_gm=True;self.cancelled=False
        self.corpses=[]
        self.local=['3','4','5'];self.on_read=None
        self.messages=[dict(sequence=str(i),text='Old SYSTEM '+str(i)) for i in range(1,11)]

    def call(self,method,params,**kwargs):
        if method not in ('buddy-world.snapshot','buddy-party.state','buddy-actions.status','buddy-chat.poll'):
            return super().call(method,params,**kwargs)
        self.calls.append((method,copy.deepcopy(params)))
        if method=='buddy-actions.status':
            value=dict(sessionId=self.sid,selfGuid='87',position=dict(known=True,mapKnown=True,mapId=349,
                x=12,y=34,z=56,estimated=True,inferred=True,revision='pose-7'))
        elif method=='buddy-party.state':value=dict(sessionId=self.sid,group=dict(revision=self.group_revision))
        elif method=='buddy-world.snapshot':
            if self.after_selection_read_error and any(code==0x13d for code,_ in self.packets):
                raise TimeoutError('World read unavailable after selection ACK')
            players=[dict(guid=guid,present=True,mapKnown=True,mapId=349,x=int(guid),aliveKnown=True,alive=True) for guid in self.local]
            value=dict(visitRevision='visit-1',self=dict(guid='87',present=True,mapKnown=True,mapId=349,
                targetKnown=True,targetGuid=self.selected,x=12,y=34,z=56),
                owner=next((row for row in players if row['guid']=='3'),dict(guid='3',present=False)),
                nearbyPlayers=[row for row in players if row['guid']!='3'],nearbyCreatures=[],playerCorpses=self.corpses)
        else:
            after=int(params.get('afterSequence',0));limit=params.get('limit',50)
            if after and self.post_failure=='read':raise TimeoutError('Poll read failed after known write')
            rows=[row for row in self.messages if int(row['sequence'])>after][:limit]
            if self.commands and self.stale:rows=self.messages[:1]
            value=dict(sessionId='foreign-chat' if after and self.post_failure=='chat' else self.chat_sid,
                messages=copy.deepcopy(rows),latestSequence=str(len(self.messages)),oldestSequence='1',
                nextSequence=rows[-1]['sequence'] if rows else str(after),cursorLost=self.cursor_lost)
            if after and self.post_failure=='regressed':value.update(messages=[],latestSequence='0')
        if self.on_read:self.on_read(method,value)
        return value

    def send(self,*args,**kwargs):
        code,body=packet(kwargs['input']);self.packets.append((code,body))
        if code==0x13d:
            self.last_selection=str(struct.unpack('<Q',body)[0])
            if self.settle_selection:self.selected=self.last_selection
            if self.post_failure=='selection_transport':raise TimeoutError('selection ACK lost')
        elif code==0x95:
            assert struct.unpack('<II',body[:8])==(1,7) and body[-1:]==b'\0'
            command=body[8:-1].decode('ascii');self.commands.append(command)
            # Pinned GPS resolves a local unit, otherwise GM self. Its text
            # contains coordinates/instance only, no target name or GUID.
            target=self.selected if self.selected in self.local else '87'
            if self.feedback_enabled:
                self.messages.append(dict(sequence=str(len(self.messages)+1),channel='CHANNEL_UNSPECIFIED',
                    language=0,senderGuid='0',text='Map:349 X:'+target+' Y:34 Z:56 InstanceID:12'))
            if self.post_failure=='transport':raise TimeoutError('native command ACK lost')
            if self.post_failure=='body':self.sid='aw-999'
        else:raise AssertionError('Unexpected native opcode')
        return subprocess.CompletedProcess(args,0,b'grpc-status: 0\r\n',b'')


class GMCommandEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup)
        self.folder=Path(self.temp.name).resolve();self.workspace=self.folder/'workspace';self.workspace.mkdir(mode=0o700)
        self.host=self.folder/'host';self.host.mkdir(mode=0o700)
        self.rpc=NativeRPC();self.gm=direct_tools.load_helper(fixtures.ROOT/'dev/gm_tasks.py','evidence_gm_tasks')
        self.catalog=fixtures.DirectToolTests.legacy_poll_catalog(self)
        self.rpc.catalog=lambda:dict(copy.deepcopy(self.catalog),sessionId=self.rpc.sid)
        config=self.host/'data/converse/lotharen/gm-transport.json';config.parent.mkdir(parents=True,mode=0o700)
        config.write_text('{}')
        self.tools=self.make_tools()
        self.transport=mock.patch.object(self.gm.subprocess,'run',side_effect=self.rpc.send);self.transport.start();self.addCleanup(self.transport.stop)

    def make_tools(self):
        return direct_tools.DirectTools(self.rpc,self.rpc.sid,'87',self.workspace,self.host,
            allowed=lambda:True,gm_allowed=lambda:self.rpc.allow_gm,cancelled=lambda:self.rpc.cancelled,
            request_id='one-owner-request',chat_session_id=self.rpc.chat_sid,
            helpers={'gm_tasks':self.gm})

    def execute(self,command='.gps Joel',selected=None,operation='owner:1'):
        args=dict(command=command)
        if selected is not None:args['select_guid']=selected
        return self.tools.dispatch('gm_execute',args,operation_id=operation)

    def test_fresh_system_uses_latest_baseline_not_first_old_message_and_no_target_attribution(self):
        result=self.execute(selected='3')
        self.assertTrue(result['ok'],result);row=result['result'];feedback=row['feedback']
        self.assertTrue(row['commandTransportWritten']);self.assertTrue(row['selectionTransportWritten'])
        self.assertEqual(feedback['afterSequence'],'10')
        self.assertEqual([message['sequence'] for message in feedback['systemMessages']],['11'])
        self.assertEqual(feedback['correlation'],'temporal_only');self.assertFalse(feedback['targetIdentityKnown'])
        self.assertFalse(feedback['commandSuccessKnown']);self.assertFalse(row['gameEffectClaimed'])
        self.assertEqual([params for method,params in self.rpc.calls if method=='buddy-chat.poll'],[
            dict(afterSequence='0',limit=1,sessionId=self.rpc.chat_sid),dict(afterSequence='10',limit=256,sessionId=self.rpc.chat_sid)])

    def test_offscreen_selection_is_not_banned_but_gps_self_fallback_remains_unproven(self):
        result=self.execute(selected='1')
        self.assertTrue(result['ok'],result);row=result['result']
        self.assertTrue(row['commandTransportWritten']);self.assertFalse(row['selection']['requestedLocallyPresent'])
        self.assertEqual(row['selection']['observedTargetGuid'],'1')
        self.assertIn('X:87',row['feedback']['systemMessages'][0]['text'])
        self.assertFalse(row['feedback']['targetIdentityKnown'])
        self.assertFalse(row['selection']['realmCommandTargetKnown'])

    def test_player_corpse_presence_is_not_reported_as_local_unit_resolution(self):
        self.rpc.corpses=[dict(guid='9999',present=True,mapKnown=True,mapId=349)]
        row=self.execute(selected='9999')['result']
        self.assertTrue(row['commandTransportWritten'])
        self.assertTrue(row['selection']['requestedLocallyPresent'])
        self.assertEqual(row['selection']['observedSelectedObjectKind'],'corpse')
        self.assertIsNone(row['selection']['locallyResolvedSelectedGuid'])
        self.assertFalse(row['feedback']['targetIdentityKnown'])

    def test_selection_ack_without_observed_target_never_sends_or_queues_command(self):
        self.rpc.settle_selection=False
        result=self.execute(selected='3')
        self.assertTrue(result['ok'],result)
        self.assertTrue(result['result']['selectionTransportWritten']);self.assertFalse(result['result']['commandTransportWritten'])
        self.assertEqual(self.rpc.commands,[])
        self.rpc.selected='3'  # Later actual observation permits a fresh tool operation.
        result=self.execute(selected='3',operation='owner:2')
        self.assertTrue(result['result']['commandTransportWritten']);self.assertEqual(self.rpc.commands,['.gps Joel'])

    def test_selection_ack_then_read_loss_reports_no_command_not_unknown_command(self):
        self.rpc.after_selection_read_error=True
        result=self.execute(selected='3')
        self.assertTrue(result['ok'],result);self.assertFalse(result['result']['commandTransportWritten'])
        self.assertTrue(result['result']['selectionTransportWritten']);self.assertIsNone(self.tools.unknown_receipt)
        self.assertFalse(self.rpc.commands)

    def test_selection_changed_during_final_reservation_save_prevents_command(self):
        original=self.gm.runtime.Store.save
        reservations=[0]
        def save(store,data):
            original(store,data)
            if data.get('reserved'):
                reservations[0]+=1
                if reservations[0]==2:self.rpc.selected='5'
        with mock.patch.object(self.gm.runtime.Store,'save',save):result=self.execute(selected='3')
        self.assertTrue(result['ok'],result);self.assertFalse(result['result']['commandTransportWritten'])
        self.assertEqual(result['result']['selection']['observedTargetGuid'],'5');self.assertFalse(self.rpc.commands)

    def test_selection_changed_in_final_status_party_read_prevents_command(self):
        def observed(method,value):
            if method=='buddy-party.state' and any(code==0x13d for code,_ in self.rpc.packets):
                self.rpc.selected='5'
        self.rpc.on_read=observed
        result=self.execute(selected='3')
        self.assertTrue(result['ok'],result);self.assertFalse(result['result']['commandTransportWritten'])
        self.assertEqual(result['result']['selection']['observedTargetGuid'],'5');self.assertFalse(self.rpc.commands)

    def test_owner_cancel_or_authority_loss_during_final_world_read_is_known_no_packet(self):
        for mode in ('cancel','authority'):
            with self.subTest(mode=mode):
                self.rpc.selected='3';self.rpc.allow_gm=True;self.rpc.cancelled=False
                original=self.gm.runtime.Store.save
                armed=[False]
                def save(store,value):
                    original(store,value)
                    if value.get('reserved'):armed[0]=True
                def observe(method,value):
                    if method=='buddy-world.snapshot' and armed[0]:
                        if mode=='cancel':self.rpc.cancelled=True
                        else:self.rpc.allow_gm=False
                self.rpc.on_read=observe
                with mock.patch.object(self.gm.runtime.Store,'save',save):
                    result=self.execute(selected='3',operation='final-world:'+mode)
                self.assertFalse(result['ok'],result)
                self.assertIsNone(self.tools.unknown_receipt)
                self.assertFalse(self.rpc.packets);self.assertFalse(self.rpc.commands)

    def test_same_operation_dedups_but_new_tool_call_after_roster_change_repeats_command(self):
        first=self.execute('.groupsummon Arlen',operation='request:1')
        self.assertTrue(first['result']['commandTransportWritten'])
        repeated=self.execute('.groupsummon Arlen',operation='request:1')
        self.assertTrue(repeated['result']['historicalReceipt']);self.assertEqual(len(self.rpc.commands),1)
        self.rpc.group_revision+=1
        fresh=self.execute('.groupsummon Arlen',operation='request:3')
        self.assertTrue(fresh['result']['commandTransportWritten']);self.assertEqual(len(self.rpc.commands),2)
        self.assertEqual(fresh['result']['feedback']['afterSequence'],'11')
        self.assertFalse(self.execute('.summon Arlen',operation='request:1')['ok'])
        self.assertEqual(len(self.rpc.commands),2)
        self.assertFalse(self.tools.dispatch('gm_execute',dict(command='.gps',operation_id='model-chosen'))['ok'])

    def test_no_reply_stale_cursor_lost_and_read_failure_are_separate_from_known_write(self):
        for mode in ('none','stale','lost','read','chat','regressed'):
            with self.subTest(mode=mode):
                self.rpc.feedback_enabled=mode!='none';self.rpc.stale=mode=='stale';self.rpc.cursor_lost=mode=='lost'
                self.rpc.post_failure=mode if mode in ('read','chat','regressed') else None
                result=self.execute(operation='owner:'+mode)
                self.assertTrue(result['ok'],result);row=result['result'];self.assertTrue(row['commandTransportWritten'])
                self.assertIsNone(self.tools.unknown_receipt)
                expected='no_reply_yet' if mode=='none' else 'cursor_lost' if mode=='lost' else 'unavailable_after_written'
                self.assertEqual(row['feedback']['status'],expected)

    def test_unknown_native_write_and_recreated_exact_operation_never_replay(self):
        self.rpc.post_failure='transport'
        with self.assertRaises(direct_tools.ToolOutcomeUnknown) as caught:self.execute()
        self.assertTrue(caught.exception.receipt['commandOutcomeUnknown'])
        self.assertIsNone(caught.exception.receipt['commandTransportWritten'])
        count=len(self.rpc.packets);self.tools=self.make_tools()
        with self.assertRaises(direct_tools.ToolOutcomeUnknown):self.execute()
        self.assertEqual(len(self.rpc.packets),count)

    def test_unknown_selection_has_not_attempted_the_command(self):
        self.rpc.post_failure='selection_transport'
        with self.assertRaises(direct_tools.ToolOutcomeUnknown) as caught:self.execute(selected='3')
        self.assertTrue(caught.exception.receipt['selectionOutcomeUnknown'])
        self.assertIsNone(caught.exception.receipt['selectionTransportWritten'])
        self.assertIs(caught.exception.receipt['commandTransportWritten'],False)
        self.assertFalse(self.rpc.commands)

    def test_foreign_native_body_after_known_write_retains_written_fact_and_halts(self):
        self.rpc.post_failure='body'
        with self.assertRaises(direct_tools.ToolOutcomeUnknown) as caught:self.execute()
        self.assertIs(caught.exception.receipt['commandTransportWritten'],True)
        self.assertFalse(caught.exception.receipt['replayAllowed'])
        self.assertEqual(len(self.rpc.commands),1)

    def test_native_link_argument_is_preserved_and_control_separators_refused(self):
        command='.additem |Hitem:123|h[Example]|h|r'
        self.assertTrue(self.execute(command)['result']['commandTransportWritten'])
        self.assertEqual(self.rpc.commands,[command])
        for index,command in enumerate(('.gps\n.revive Joel','.gps\0','.gps\r')):
            self.assertFalse(self.execute(command,operation='invalid:'+str(index))['ok'])
        self.assertEqual(len(self.rpc.commands),1)

    def test_real_direct_brain_gives_distinct_actual_tool_calls_distinct_host_operations(self):
        import test_direct_brain as brain_fixture
        from test_direct_model import FakeHTTP, chat
        converse=brain_fixture.converse
        final=dict(choices=[dict(finish_reason='stop',message=dict(role='assistant',content='Both command receipts are retained.'))])
        def next_group(_):
            self.assertEqual(len(self.rpc.commands),1)
            self.rpc.group_revision+=1
            return chat(('second','gm_execute',dict(command='.groupsummon Arlen')))
        state=dict(character=dict(guid='87'),gm_controls_available=True,
            development=dict(enabled=True,directDevelopment=True,workspace=str(self.workspace),rpcUrl=self.rpc.url,
                sessionId=self.rpc.sid,selfGuid='87',ownerGuid='3'),
            current_request=dict(source='owner',senderGuid='3',channel='WHISPER',conversationScope='owner',
                messages=[dict(text='Perform my two distinct requested commands.')]))
        original_loader=direct_tools.load_helper
        def load(path,name):
            return self.gm if Path(path).name=='gm_tasks.py' else original_loader(path,name)
        first=chat(('first','gm_execute',dict(command='.groupsummon Arlen')))
        with FakeHTTP([first,copy.deepcopy(first),next_group,final]) as server, \
                mock.patch.object(converse,'ROOT',self.host),mock.patch.object(converse,'RPC',return_value=self.rpc), \
                mock.patch.object(converse,'checked_developer_workspace',return_value=self.workspace), \
                mock.patch.object(direct_tools,'load_helper',side_effect=load):
            brain=converse.DirectBrain(self.folder/'brain',developer_workspace=self.workspace,character_name='Lotharen',
                character_class='paladin',model='fixture',provider='local',base_url=server.base+'/v1')
            result=brain.generate([dict(text='Perform my two distinct requested commands.')],state)
        self.assertEqual(result['text'],'Both command receipts are retained.')
        self.assertEqual(self.rpc.commands,['.groupsummon Arlen']*2)
        self.assertEqual(len(brain.last_development_result['tools']),2)  # Provider same-call replay never entered dispatcher.
        records=[json.loads(path.read_text()) for path in (self.host/'data/converse/lotharen/gm-commands').glob('*/state.json')]
        self.assertEqual(len(records),2)
        self.assertEqual(len({row['result']['operationId'] for row in records}),2)

    def test_inspection_retains_visible_owner_and_raw_position_provenance(self):
        with mock.patch.object(self.gm,'bindings',return_value=[]),mock.patch.object(self.gm,'task_rpc',return_value=self.rpc):
            result=self.tools.dispatch('gm_inspect',{})
        self.assertTrue(result['ok'],result)
        facts=result['result']['clients'][0]['observations']
        self.assertEqual(facts['world']['owner']['guid'],'3')
        self.assertTrue(facts['world']['owner']['present'])
        self.assertTrue(facts['actions']['position']['estimated'])
        self.assertEqual(facts['actions']['position']['revision'],'pose-7')
