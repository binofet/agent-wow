"""Real DirectTools -> SessionTransport framing -> simulated native feedback."""
import copy
import hashlib
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
        self.group_members=['1','3','4','5','87'];self.group_leader='1'
        self.life={};self.life_known={};self.vitals_age=284517;self.world_revision=1
        self.world_x=12;self.position_inferred=False
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
        elif method=='buddy-party.state':value=dict(sessionId=self.sid,group=dict(known=True,inGroup=True,
            groupGuid='777',leaderGuid=self.group_leader,revision=self.group_revision,
            members=[dict(guid=guid,name={'1':'Arlen','3':'Joel','4':'Mira','5':'Rook','8':'Nixwick','87':'Lotharen'}[guid],online=True)
                for guid in self.group_members]))
        elif method=='buddy-world.snapshot':
            if self.after_selection_read_error and any(code==0x13d for code,_ in self.packets):
                raise TimeoutError('World read unavailable after selection ACK')
            players=[dict(guid=guid,present=True,mapKnown=True,mapId=349,positionKnown=True,x=int(guid),
                aliveKnown=self.life_known.get(guid,True),alive=self.life.get(guid,True),
                health=100 if self.life.get(guid,True) else 0,vitalsAgeMs=str(self.vitals_age),positionRevision=str(self.world_revision)) for guid in self.local]
            value=dict(visitRevision='visit-1',self=dict(guid='87',present=True,mapKnown=True,mapId=349,
                positionKnown=True,positionInferred=self.position_inferred,targetKnown=True,targetGuid=self.selected,x=self.world_x,y=34,z=56),
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
        self.now=100.;self.on_wait=None
        def wait(seconds):
            self.now+=seconds
            if self.on_wait:self.on_wait()
        for name,side_effect in (('monotonic',lambda:self.now),('sleep',wait)):
            patch=mock.patch.object(self.gm.time,name,side_effect=side_effect)
            patch.start();self.addCleanup(patch.stop)

    def make_tools(self, chat_cursor=None):
        return direct_tools.DirectTools(self.rpc,self.rpc.sid,'87',self.workspace,self.host,
            allowed=lambda:True,gm_allowed=lambda:self.rpc.allow_gm,cancelled=lambda:self.rpc.cancelled,
            request_id='one-owner-request',chat_session_id=self.rpc.chat_sid,chat_cursor=chat_cursor,
            helpers={'gm_tasks':self.gm})

    def execute(self,command='.gps Joel',selected=None,operation='owner:1'):
        args=dict(command=command)
        if selected is not None:args['select_guid']=selected
        return self.tools.dispatch('gm_execute',args,operation_id=operation)

    def test_named_location_guide_matches_pinned_catalog_without_changing_full_reference(self):
        reference=(fixtures.ROOT/'dev/lotharen-gm-commands.txt').read_text()
        guide=reference.split('END GENERAL GUIDE',1)[0]
        catalog=reference.split('FULL COMMAND HELP AND SOURCE SIGNATURES\n',1)[1].split(
            '\nPUBLIC SOURCE FILE MANIFEST\n',1)[0]
        self.assertEqual(hashlib.sha256(catalog.encode()).hexdigest(),
            '43204986573bd7c99ada6e92d499be5b8b05a481b2be3d5763ebef4002d94581')
        for syntax in ('.lookup teleport $substring','.teleport #location',
                       '.teleport name [#playername] #location'):
            self.assertIn(syntax,guide)
            self.assertIn('Syntax: '+syntax,catalog)
        self.assertIn('.teleport group #location',guide)
        # The pinned SQL help omits this separator; its registered handler
        # takes GameTele and explicitly selects the player and current group.
        group_entry=catalog.split('\n.teleport group\n',1)[1].split('\n========',1)[0]
        self.assertIn('HandleTeleGroupCommand(ChatHandler* handler, GameTele const* tele)',group_entry)
        self.assertIn('Teleport a selected player and his group members',group_entry)
        self.assertLess(guide.index('Named locations:'),guide.index('HISTORICAL WORKED FULL-PARTY'))
        self.assertIn('do not require the historical instance',guide)
        self.assertIn('a command write is not arrival proof',guide)
        self.assertIn('Current realm help, permissions and teleport/instance admission',guide)
        self.assertIn('Keep the old messages and every entered action/unknown receipt',guide)
        self.assertIn('feedback.messages and feedback.systemMessages',guide)
        self.assertIn('feedback.readMore to continue reading',guide)
        self.assertIn('chat_read() defaults to the current monotonic cursor',guide)
        self.assertIn('after_sequence="0" is ONLY old history, not a fresh command response',guide)
        self.assertFalse(self.rpc.packets)

    def test_named_location_forms_keep_exact_native_text_and_do_not_claim_arrival(self):
        commands=['.lookup teleport FixtureLocation','.teleport FixtureLocation',
                  '.teleport name Joel FixtureLocation','.teleport group FixtureLocation']
        for index,command in enumerate(commands):
            with self.subTest(command=command):
                result=self.execute(command,selected='3' if index==3 else None,
                    operation='named-location:'+str(index))
                self.assertTrue(result['ok'],result)
                self.assertTrue(result['result']['commandTransportWritten'])
                self.assertFalse(result['result']['gameEffectClaimed'])
                self.assertFalse(result['result']['feedback']['commandSuccessKnown'])
        self.assertEqual(self.rpc.commands,commands)
        self.assertEqual([code for code,_ in self.rpc.packets],[0x95,0x95,0x95,0x13d,0x95])

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

    def test_proven_no_selection_read_queries_allow_fresh_operations_but_cache_exact_operation(self):
        for index,command in enumerate(('.lookup teleport Fixture','.group list','.group list Joel')):
            with self.subTest(command=command):
                operation='query:'+str(index)
                first=self.execute(command,operation=operation)
                count=len(self.rpc.packets)
                cached=self.execute(command,operation=operation)
                self.assertTrue(cached['result']['historicalReceipt'])
                self.assertEqual(len(self.rpc.packets),count)
                second=self.execute(command,operation=operation+':new')
                self.assertTrue(first['result']['commandTransportWritten'])
                self.assertTrue(second['result']['commandTransportWritten'])
                self.assertEqual(len(self.rpc.packets),count+1)
                self.assertFalse(second['result']['gameEffectClaimed'])
                self.assertEqual(second['result']['feedback']['correlation'],'temporal_only')
                self.assertFalse(second['result']['feedback']['commandSuccessKnown'])
        self.assertEqual(len(self.rpc.commands),6)
        self.assertTrue(all(code==0x95 for code,_ in self.rpc.packets))

    def test_read_query_unknown_exact_operation_still_cannot_replay(self):
        for index,command in enumerate(('.lookup teleport Fixture','.group list')):
            with self.subTest(command=command):
                self.tools=self.make_tools();self.rpc.post_failure='transport'
                operation='unknown-query:'+str(index)
                with self.assertRaises(direct_tools.ToolOutcomeUnknown):self.execute(command,operation=operation)
                count=len(self.rpc.packets);self.tools=self.make_tools()
                with self.assertRaises(direct_tools.ToolOutcomeUnknown):self.execute(command,operation=operation)
                self.assertEqual(len(self.rpc.packets),count)

    def test_selected_queries_and_unproven_siblings_keep_semantic_repeat_guard(self):
        cases=[('.lookup teleport Fixture','3'),('.group list','3'),
               ('.lookup spell Fixture',None),('.lookup tele Fixture',None),
               ('.lookup teleport',None),('.group leader Joel',None),
               ('.group join Joel Mira',None),('.group remove Joel',None),('.group list Joel Extra',None)]
        for index,(command,selected) in enumerate(cases):
            with self.subTest(command=command,selected=selected):
                first=self.execute(command,selected,operation='guard:'+str(index))
                self.assertTrue(first['result']['commandTransportWritten'],first)
                count=len(self.rpc.packets)
                blocked=self.execute(command,selected,operation='guard:'+str(index)+':new')
                self.assertTrue(blocked['ok'],blocked)
                self.assertFalse(blocked['result']['commandTransportWritten'])
                self.assertEqual(len(self.rpc.packets),count)

    def test_legacy_raw_query_receipts_do_not_block_first_task_and_stay_unchanged(self):
        with mock.patch.object(direct_tools.DirectTools,'_gm_read_query',return_value=False):
            self.assertTrue(self.execute('.lookup teleport Fixture',operation='legacy:1')['result']['commandTransportWritten'])
            self.assertTrue(self.execute('.group list',operation='legacy:2')['result']['commandTransportWritten'])
        root=self.host/'data/converse/lotharen/gm-commands'
        before={path:path.read_bytes() for path in root.glob('*/state.json')}
        runner=mock.Mock(return_value=subprocess.CompletedProcess([],0,b'{"started":true}',b''))
        self.tools.runner=runner
        goal=dict(members=[dict(guid='3',name='Joel')],player=dict(guid='3',name='Joel'))
        with mock.patch.object(self.gm,'task_bridge',return_value=object()), \
             mock.patch.object(self.gm,'admit_goal',return_value=dict(accepted=True)) as admission, \
             mock.patch.object(self.gm.Host,'install_holds',side_effect=AssertionError('No physical task execution')):
            result=self.tools.dispatch('gm_task_start',dict(goal=goal))
        self.assertTrue(result['ok'],result);self.assertTrue(result['result']['result']['started'])
        runner.assert_called_once();admission.assert_called_once()
        self.assertEqual({path:path.read_bytes() for path in root.glob('*/state.json')},before)
        self.assertEqual(self.rpc.commands,['.lookup teleport Fixture','.group list'])

    def test_legacy_selected_query_and_real_mutation_still_block_new_task(self):
        for selected in (None,'3'):
            with self.subTest(selected=selected):
                self.tools=self.make_tools();self.tools.request_id='pending:'+str(selected)
                command='.revive Joel' if selected is None else '.group list'
                self.assertTrue(self.execute(command,selected,operation='pending:'+str(selected))['result']['commandTransportWritten'])
                with mock.patch.object(self.gm,'task_bridge',side_effect=AssertionError('Must block before bridge')):
                    result=self.tools.dispatch('gm_task_start',dict(goal=dict(members=[])))
                self.assertTrue(result['ok'],result);self.assertFalse(result['result']['started'])
                self.assertIn('same_request_direct_intent_unsettled',result['result']['completion'])

    def test_delayed_system_collects_after_empty_and_other_chat_without_resending_command(self):
        self.rpc.feedback_enabled=False
        waits=[];timeouts=[]
        def later():
            waits.append(self.now)
            if len(waits)==1:
                self.rpc.messages.append(dict(sequence='11',channel='WHISPER',senderGuid='3',text='Unrelated owner chat'))
            elif len(waits)==2:
                self.rpc.messages.append(dict(sequence='12',channel=0,language=0,senderGuid='0',text='Usage: go xyz'))
        self.on_wait=later
        original=self.rpc.call
        def read(method,params,**kwargs):
            if method=='buddy-chat.poll' and self.rpc.commands:
                timeouts.append(kwargs['timeout'])
                self.assertTrue(self.gm.task_store(self.gm.writer_folder(self.host,self.rpc.sid,{})).held())
            return original(method,params,**kwargs)
        with mock.patch.object(self.rpc,'call',side_effect=read):result=self.execute('.help go')
        self.assertTrue(result['ok'],result);row=result['result'];feedback=row['feedback']
        self.assertTrue(row['commandTransportWritten']);self.assertIsNone(self.tools.unknown_receipt)
        self.assertEqual(feedback['status'],'observed')
        self.assertEqual([message['sequence'] for message in feedback['messages']],['11','12'])
        self.assertEqual([message['text'] for message in feedback['systemMessages']],['Usage: go xyz'])
        self.assertEqual(feedback['readMore'],dict(after_sequence='12',limit=256))
        self.assertEqual([params['afterSequence'] for method,params in self.rpc.calls if method=='buddy-chat.poll'],['0','10','10','11'])
        self.assertTrue(all(0<timeout<=.75 for timeout in timeouts))
        self.assertEqual(len(waits),2);self.assertEqual(self.rpc.commands,['.help go'])
        self.assertEqual([code for code,_ in self.rpc.packets],[0x95])
        self.assertFalse(feedback['commandSuccessKnown']);self.assertFalse(feedback['targetIdentityKnown'])

    def test_no_reply_is_bounded_and_machine_ready_read_more_fetches_later_feedback(self):
        self.rpc.feedback_enabled=False
        started=self.now;result=self.execute('.help go')
        self.assertTrue(result['ok'],result);feedback=result['result']['feedback']
        self.assertEqual(feedback['status'],'no_reply_yet');self.assertEqual(feedback['systemMessages'],[])
        self.assertAlmostEqual(self.now-started,.75)
        self.assertEqual(feedback['readMore'],dict(after_sequence='10',limit=256))
        self.rpc.messages.append(dict(sequence='11',language=0,senderGuid='0',text='Late help response'))
        later=self.tools.dispatch('chat_read',feedback['readMore'])
        self.assertTrue(later['ok'],later)
        self.assertEqual([row['text'] for row in later['result']['messages']],['Late help response'])
        self.assertEqual(self.rpc.commands,['.help go']);self.assertIsNone(self.tools.unknown_receipt)

    def test_owner_interrupt_after_ack_stops_collection_but_settles_known_written_receipt(self):
        self.rpc.feedback_enabled=False
        def interrupt():self.rpc.cancelled=True
        self.on_wait=interrupt
        result=self.execute('.help go')
        self.assertTrue(result['ok'],result);row=result['result']
        self.assertTrue(row['commandTransportWritten']);self.assertTrue(row['transportWritten'])
        self.assertEqual(row['feedback']['status'],'interrupted_after_written')
        self.assertIn('request_cancelled',row['feedback']['error'])
        self.assertIsNone(self.tools.unknown_receipt);self.assertEqual(self.rpc.commands,['.help go'])
        self.assertLess(self.now-100.,.1)
        self.rpc.cancelled=False
        historical=self.execute('.help go')
        self.assertTrue(historical['result']['historicalReceipt']);self.assertEqual(self.rpc.commands,['.help go'])

    def test_host_current_cursor_default_is_fresh_and_explicit_history_cannot_regress_it(self):
        self.rpc.messages=[dict(sequence=str(i),text='Historical '+str(i)) for i in range(1,262)]
        self.tools=self.make_tools(chat_cursor='261')
        first=self.tools.dispatch('chat_read',dict(limit=50))
        self.assertTrue(first['ok'],first);self.assertEqual(first['result']['messages'],[])
        row=self.execute('.help go')['result']
        self.assertEqual(row['feedback']['afterSequence'],'261')
        self.assertEqual([message['sequence'] for message in row['feedback']['systemMessages']],['262'])
        self.assertEqual(self.tools.chat_cursor,'262')
        self.rpc.messages.append(dict(sequence='263',channel=0,text='New realm feedback'))
        fresh=self.tools.dispatch('chat_read',{})
        self.assertEqual([message['sequence'] for message in fresh['result']['messages']],['263'])
        old=self.tools.dispatch('chat_read',dict(after_sequence='0',limit=50))
        self.assertEqual(old['result']['messages'][0]['sequence'],'1')
        self.assertEqual(self.tools.chat_cursor,'263')
        self.assertEqual(self.tools.dispatch('chat_read',{})['result']['messages'],[])
        self.assertEqual(self.rpc.commands,['.help go'])
        for invalid in (261,'-1','01',str(2**64)):
            with self.assertRaises(direct_tools.ToolError):self.make_tools(chat_cursor=invalid)

    def test_future_or_malformed_poll_cursors_cannot_poison_the_fresh_default(self):
        self.tools=self.make_tools(chat_cursor='10')
        future=self.tools.dispatch('chat_read',dict(after_sequence=str(2**64-1)))
        self.assertTrue(future['ok'],future);self.assertEqual(future['result']['messages'],[])
        self.assertEqual(self.tools.chat_cursor,'10')
        self.rpc.messages.append(dict(sequence='11',text='Fresh after future read'))
        fresh=self.tools.dispatch('chat_read',{})
        self.assertEqual([row['text'] for row in fresh['result']['messages']],['Fresh after future read'])
        def malformed(method,value):
            if method=='buddy-chat.poll':value['nextSequence']='999'
        self.rpc.on_read=malformed
        invalid=self.tools.dispatch('chat_read',dict(after_sequence='0'))
        self.assertFalse(invalid['ok'],invalid);self.assertEqual(self.tools.chat_cursor,'11')
        self.rpc.on_read=None
        self.rpc.messages.append(dict(sequence='12',text='Fresh after malformed read'))
        final=self.tools.dispatch('chat_read',{})
        self.assertEqual([row['text'] for row in final['result']['messages']],['Fresh after malformed read'])
        self.assertFalse(self.rpc.packets);self.assertFalse(self.rpc.commands)

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
        unchanged=self.execute('.groupsummon Arlen',operation='request:2')
        self.assertFalse(unchanged['result']['commandTransportWritten'])
        self.assertEqual(len(self.rpc.commands),1)
        self.rpc.group_members=['1','3','4','8','87']
        fresh=self.execute('.groupsummon Arlen',operation='request:3')
        self.assertTrue(fresh['result']['commandTransportWritten']);self.assertEqual(len(self.rpc.commands),2)
        self.assertEqual(fresh['result']['feedback']['afterSequence'],'11')
        self.assertFalse(self.execute('.summon Arlen',operation='request:1')['ok'])
        self.assertEqual(len(self.rpc.commands),2)
        self.assertFalse(self.tools.dispatch('gm_execute',dict(command='.gps',operation_id='model-chosen'))['ok'])

    def test_returning_to_an_earlier_real_roster_uses_latest_intent_not_old_context(self):
        original=list(self.rpc.group_members)
        first=self.execute('.groupsummon Arlen',operation='cycle:1')
        self.rpc.group_members=['1','3','4','8','87']
        second=self.execute('.groupsummon Arlen',operation='cycle:2')
        self.rpc.group_members=original
        third=self.execute('.groupsummon Arlen',operation='cycle:3')
        self.assertTrue(all(row['result']['commandTransportWritten'] for row in (first,second,third)))
        self.assertEqual(self.rpc.commands,['.groupsummon Arlen']*3)
        records=[json.loads(path.read_text()) for path in (self.host/'data/converse/lotharen/gm-commands').glob('*/state.json')]
        self.assertEqual(sorted(row['commandOrdinal'] for row in records),[1,2,3])

    def test_inferred_pose_and_unknown_grammar_do_not_authorize_a_repeat(self):
        self.assertTrue(self.execute('.groupsummon Arlen')['result']['commandTransportWritten'])
        self.rpc.world_x=999;self.rpc.position_inferred=True
        inferred=self.execute('.groupsummon Arlen',operation='inferred:2')
        self.assertFalse(inferred['result']['commandTransportWritten'])
        for index,command in enumerate(('.tele add Example','.group unfamiliar Example')):
            self.assertTrue(self.execute(command,operation='raw:first'+str(index))['result']['commandTransportWritten'])
            self.rpc.world_x+=1;self.rpc.group_members=['1','3','4','8','87']
            repeated=self.execute(command,operation='raw:second'+str(index))
            self.assertFalse(repeated['result']['commandTransportWritten'])

    def test_named_then_selected_revive_with_only_ack_blocks_before_second_selection(self):
        self.rpc.selected='8';self.rpc.life['3']=False;self.rpc.feedback_enabled=False
        first=self.execute('.revive Joel',operation='request:2')
        self.assertTrue(first['result']['commandTransportWritten'])
        self.assertEqual(first['result']['feedback']['status'],'no_reply_yet')
        with mock.patch.object(self.gm,'bindings',return_value=[]),mock.patch.object(self.gm,'task_rpc',return_value=self.rpc):
            facts=self.tools.dispatch('gm_inspect',{})
        self.assertIs(facts['result']['clients'][0]['observations']['world']['owner']['alive'],False)
        count=len(self.rpc.packets)
        # A new tool instance/call ID does not discard the durable intent.
        self.tools=self.make_tools()
        alias=self.execute('.revive',selected='3',operation='request:4')
        self.assertTrue(alias['ok'],alias);row=alias['result']
        self.assertFalse(row['commandTransportWritten']);self.assertFalse(row['selectionTransportWritten'])
        self.assertEqual(row['repeatAdmission']['priorOperationId'],'request:2')
        self.assertIs(row['repeatAdmission']['priorCommandTransportWritten'],True)
        self.assertEqual(len(self.rpc.packets),count);self.assertEqual(self.rpc.commands,['.revive Joel'])
        self.assertFalse(row['gameEffectClaimed']);self.assertIsNone(self.tools.unknown_receipt)

    def test_same_request_revive_ignores_age_revision_selection_and_unknown_life_churn(self):
        self.rpc.selected='8';self.rpc.life['3']=False
        self.assertTrue(self.execute('.revive Joel')['result']['commandTransportWritten'])
        self.rpc.vitals_age+=100;self.rpc.world_revision+=1;self.rpc.group_revision+=1;self.rpc.selected='3'
        for index,args in enumerate((dict(command='.revive Joel'),dict(command='.revive 3'),
                dict(command='.revive |Hplayer:Joel|h[Joel]|h'),dict(command='.revive',select_guid='3'))):
            if index==3:self.rpc.life_known['3']=False
            result=self.tools.dispatch('gm_execute',args,operation_id='retry:'+str(index))
            self.assertTrue(result['ok'],result);self.assertFalse(result['result']['commandTransportWritten'])
        self.assertEqual(self.rpc.commands,['.revive Joel']);self.assertEqual(len(self.rpc.packets),1)

    def test_changed_known_life_or_fresh_owner_intent_is_separate_from_old_receipt(self):
        self.rpc.life['3']=False
        first=self.execute('.revive Joel',operation='old:1')
        self.rpc.life['3']=True
        changed=self.execute('.revive',selected='3',operation='old:2')
        self.assertTrue(changed['result']['commandTransportWritten'])
        self.tools=self.make_tools();self.tools.request_id='fresh-owner-intent'
        fresh=self.execute('.revive Joel',operation='fresh:1')
        self.assertTrue(fresh['result']['commandTransportWritten'])
        self.assertTrue(first['result']['commandTransportWritten']);self.assertFalse(first['result']['gameEffectClaimed'])
        self.assertEqual(len(self.rpc.commands),3)

    def test_unknown_revive_does_not_reopen_from_alias_or_new_life_context(self):
        self.rpc.selected='8';self.rpc.life['3']=False;self.rpc.post_failure='transport'
        with self.assertRaises(direct_tools.ToolOutcomeUnknown):self.execute('.revive Joel')
        count=len(self.rpc.packets);self.rpc.post_failure=None;self.rpc.life['3']=True
        self.tools=self.make_tools()
        repeat=self.execute('.revive',selected='3',operation='unknown:2')
        self.assertTrue(repeat['ok'],repeat);self.assertFalse(repeat['result']['commandTransportWritten'])
        self.assertEqual(len(self.rpc.packets),count)
        self.assertIsNone(repeat['result']['repeatAdmission']['priorCommandTransportWritten'])

    def test_known_no_command_send_does_not_reserve_the_effect(self):
        self.rpc.life['3']=False;self.rpc.settle_selection=False
        first=self.execute('.revive',selected='3')
        self.assertFalse(first['result']['commandTransportWritten'])
        self.rpc.selected='3'
        next_call=self.execute('.revive Joel',operation='nosend:2')
        self.assertTrue(next_call['result']['commandTransportWritten'])
        self.assertEqual(self.rpc.commands,['.revive Joel'])

    def test_pure_diagnostics_stay_refreshable_and_arbitrary_first_commands_stay_available(self):
        self.rpc.life['3']=False
        self.assertTrue(self.execute('.revive Joel')['result']['commandTransportWritten'])
        for index,command in enumerate(('.help revive','.help revive','.gps Joel','.gps Joel','.pinfo Joel',
                '.commands','.instance listbinds','.unfamiliar arbitrary arguments')):
            result=self.execute(command,operation='read:'+str(index))
            self.assertTrue(result['result']['commandTransportWritten'],result)
        repeat=self.execute('.unfamiliar arbitrary arguments',operation='raw:2')
        self.assertFalse(repeat['result']['commandTransportWritten'])
        self.assertEqual(len(self.rpc.commands),9)

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

    def test_selection_final_prewrite_refusal_is_known_no_packet(self):
        def refused():raise ValueError('owner cancelled before selection dispatch')
        result=self.gm.execute_command(self.rpc,{},'.gps Joel',self.host,select_guid='3',
            expected_session=self.rpc.sid,final_authorise=refused)
        self.assertIs(result['selectionTransportWritten'],False)
        self.assertIs(result['commandTransportWritten'],False)
        self.assertEqual(result['completion'],'selection_not_dispatched')
        self.assertFalse(self.rpc.packets)

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
            self.rpc.group_members=['1','3','4','8','87']
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
