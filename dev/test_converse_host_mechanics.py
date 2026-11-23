"""Host compatibility with fake CLI/RPC only; no model or game connection."""
import importlib.machinery
import importlib.util
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

loader = importlib.machinery.SourceFileLoader('converse_host_mechanics', str(Path(__file__).with_name('converse')))
spec = importlib.util.spec_from_loader(loader.name, loader)
c = importlib.util.module_from_spec(spec)
loader.exec_module(c)


def allowance(primary=1, secondary=1, **fields):
    return dict(ordinaryUsageAllowed=True, rateLimitsByLimitId={'codex': {
        'primary': {'usedPercent': primary}, 'secondary': {'usedPercent': secondary}}}, **fields)


def fake_cli(folder, result, reply=None):
    executable, audit = folder/'fake-codex', folder/'fake-cli.jsonl'
    executable.write_text('#!'+sys.executable+'\n'+
        'import json,pathlib,sys\n'
        'audit=pathlib.Path('+repr(str(audit))+')\n'
        'def record(value):\n'
        ' with audit.open("a") as out:out.write(json.dumps(value)+"\\n")\n'
        'record({"argv":sys.argv[1:]})\n'
        'if sys.argv[1:]==["app-server","--listen","stdio://"]:\n'
        ' for line in sys.stdin:\n'
        '  value=json.loads(line);record({"method":value["method"]})\n'
        '  if value["method"]=="initialize":print(json.dumps({"id":value["id"],"result":{"userAgent":"fake"}}),flush=True)\n'
        '  elif value["method"]=="account/rateLimits/read":print(json.dumps({"id":value["id"],"result":'+repr(result)+'}),flush=True)\n'
        'elif sys.argv[1]=="exec":\n'
        ' prompt=sys.stdin.read();args=sys.argv\n'
        ' schema=json.loads(pathlib.Path(args[args.index("--output-schema")+1]).read_text())\n'
        ' record({"prompt":prompt,"schema":schema})\n'
        ' pathlib.Path(args[args.index("--output-last-message")+1]).write_text(json.dumps('+repr(reply or {'text':'Ready.','emote':'NONE','action':'NONE'})+'))\n'
        ' print(json.dumps({"type":"turn.started"}));print(json.dumps({"type":"turn.completed"}))\n'
        'else:raise SystemExit("unexpected CLI mode; no credit/API fallback")\n')
    executable.chmod(0o700)
    return executable, audit


class IncludedUsageTests(unittest.TestCase):
    def test_exact_windows_require_included_allowance_without_a_seventy_percent_cap(self):
        self.assertTrue(c.included_usage_available(allowance(99.9, 99.9)))
        for result in ({}, dict(allowance(), ordinaryUsageAllowed=False, credits={'balance':100}),
                       allowance(100), allowance(1,100), allowance(True), allowance(float('nan')),
                       {'ordinaryUsageAllowed':True,'rateLimits':{'primary':{'usedPercent':0},'rateLimitReachedType':'credits'}}):
            with self.subTest(result=result):self.assertFalse(c.included_usage_available(result))
        self.assertTrue(c.included_usage_available({'ordinaryUsageAllowed':True,'rateLimits':{'primary':{'usedPercent':0}}}))

    def test_real_fake_cli_denial_or_unknown_never_launches_inference_or_credit_fallback(self):
        for result in ({}, {'ordinaryUsageAllowed':False,'credits':{'balance':100}}, allowance(100),
                       allowance(1,100), {'ordinaryUsageAllowed':True,'rateLimitsByLimitId':'malformed'}):
            with self.subTest(result=result), tempfile.TemporaryDirectory() as temporary:
                folder=Path(temporary);binary,audit=fake_cli(folder,result)
                brain=c.CodexBrain(binary,folder);brain.included_usage_only=True
                with self.assertRaises(c.IncludedUsageUnavailable):brain.generate([])
                records=[json.loads(line) for line in audit.read_text().splitlines()]
                self.assertEqual([r['argv'] for r in records if 'argv' in r], [['app-server','--listen','stdio://']])
                self.assertEqual([r['method'] for r in records if 'method' in r], ['initialize','initialized','account/rateLimits/read'])
                self.assertFalse(any('prompt' in r for r in records))
                self.assertIsNone(brain.last_development_result)

    def test_allowed_fake_cli_and_unconfigured_default_keep_existing_model_contract(self):
        for enabled in (True,False):
            with self.subTest(enabled=enabled),tempfile.TemporaryDirectory() as temporary:
                folder=Path(temporary);binary,audit=fake_cli(folder,allowance(75,99))
                brain=c.CodexBrain(binary,folder,model='fixture-model',reasoning_effort='high')
                brain.included_usage_only=enabled
                self.assertEqual(brain.generate([])['text'],'Ready.')
                calls=[r['argv'] for r in map(json.loads,audit.read_text().splitlines()) if 'argv' in r]
                self.assertEqual([args[0] for args in calls], ['app-server','exec'] if enabled else ['exec'])
                self.assertIn('--ignore-user-config',calls[-1]);self.assertIn('fixture-model',calls[-1])
                self.assertEqual(brain.model_selection()['requestedReasoningEffort'],'high')


def poll(messages=(), session='chat-body'):
    latest=int(messages[-1]['sequence']) if messages else 1
    return dict(sessionId=session,messages=list(messages),latestSequence=str(latest),nextSequence=str(latest))


class ChatRPC:
    url='http://127.0.0.1:8090/rpc'
    def __init__(self,responses):self.responses=list(responses);self.calls=[]
    def call(self,method,params,timeout=None):
        self.calls.append((method,params))
        if method=='buddy-chat.poll':return self.responses.pop(0) if self.responses else poll()
        if method=='buddy-chat.send':return {'transportWritten':True}
        raise AssertionError('unexpected live action: '+method)


class Brain:
    def __init__(self,reply=None):self.reply=reply or {'text':'Ready.','emote':'NONE'};self.states=[]
    def generate(self,history,control_state=None):
        self.states.append(control_state)
        if isinstance(self.reply,Exception):raise self.reply
        return self.reply


def gm_loop(text='Lotharen, gm .fixture inspect', *, sender=3, owner=3, guid=87, name='Lotharen', resumed=False, reply=None):
    message=dict(sequence='1',senderGuid=str(sender),receiverGuid=str(guid),channel='WHISPER',text=text)
    rpc=ChatRPC([poll([message])]);brain=Brain(reply);statuses=[]
    loop=c.Conversation(rpc,brain,player_guid=owner,character_name=name,character_class='mage',character_guid=guid,
        status=lambda **value:statuses.append(value),resume_whispers=resumed)
    if not resumed:loop.initialized=True;loop.session_id='chat-body'
    return loop,rpc,brain,statuses


class GMCompatibilityTests(unittest.TestCase):
    def test_bootstrap_optional_synthetic_catalog_and_owner_only_schema(self):
        with tempfile.TemporaryDirectory() as temporary:
            folder=Path(temporary);dev=folder/'dev';dev.mkdir()
            binary,audit=fake_cli(folder,allowance(),{'text':'Requested.','emote':'NONE','action':'NONE','gm_command':'.fixture inspect'})
            state={'current_request':{'source':'owner'},'gm_controls_available':True}
            with patch.object(c,'__file__',str(dev/'converse')):
                absent=c.CodexBrain(binary,folder,character_name='Lotharen')
                self.assertIn('rank 2',absent.persona);self.assertIn('wrath tunnel',absent.persona)
                self.assertIn('joelmax',absent.developer_persona)
                (dev/'lotharen-gm-commands.txt').write_text('SYNTHETIC_COMMAND_REFERENCE_ONLY')
                brain=c.CodexBrain(binary,folder,character_name='Lotharen')
                self.assertEqual(brain.generate([],state)['gm_command'],'.fixture inspect')
                record=next(r for r in reversed(list(map(json.loads,audit.read_text().splitlines()))) if 'prompt' in r)
                self.assertIn('SYNTHETIC_COMMAND_REFERENCE_ONLY',record['prompt'])
                self.assertIn('gm_command',record['schema']['required'])
                for source in ('party_agent','mission_fault'):
                    with self.subTest(source=source),self.assertRaises(c.ConversationError):
                        brain.generate([],dict(state,current_request={'source':source}))
                with self.assertRaises(c.ConversationError):c.CodexBrain(binary,folder).generate([],state)
                # The catalog is metadata, not a required native transport file.
                self.assertTrue(absent.gm_instructions)

    def test_literal_owner_command_and_model_command_use_only_existing_transport(self):
        for text,reply in (('Lotharen, gm .fixture inspect',None),
                           ('Lotharen, inspect the fixture',{'text':'Requested.','emote':'NONE','action':'NONE','gm_command':'.fixture inspect'})):
            with self.subTest(text=text):
                loop,rpc,brain,statuses=gm_loop(text,reply=reply)
                with patch.object(c,'send_lotharen_gm',return_value='aw-87') as send:loop.tick()
                send.assert_called_once_with(rpc,'.fixture inspect')
                self.assertEqual(loop.cursor,1)
                self.assertTrue(any(s.get('lastGmCommand',{}).get('outcome')=='sent_to_realm' for s in statuses))
                self.assertTrue(any("haven't confirmed" in args['text'] for method,args in rpc.calls if method=='buddy-chat.send'))

    def test_foreign_peer_resumed_replaced_or_unbound_body_never_sends_gm(self):
        for mode in ('peer','foreign_owner','resumed','wrong_guid','wrong_name','replacement','unbound'):
            with self.subTest(mode=mode):
                loop,rpc,brain,_=gm_loop(sender=1 if mode=='peer' else 4 if mode=='foreign_owner' else 3,
                    owner=4 if mode=='foreign_owner' else 3,resumed=mode=='resumed',
                    guid=1 if mode=='wrong_guid' else 87,name='Arlen' if mode=='wrong_name' else 'Lotharen')
                if mode=='peer':brain.developer_workspace=Path('/synthetic-workspace')
                if mode=='replacement':rpc.responses.append(poll(session='replacement'))
                if mode=='unbound':loop.session_id=''
                with patch.object(c,'send_lotharen_gm') as send:
                    if mode=='unbound':
                        with self.assertRaises(c.ConversationError):loop.run_gm_command('.fixture inspect','WHISPER',3,'WHISPER')
                    else:loop.tick()
                send.assert_not_called()
                self.assertTrue(all(state.get('gm_controls_available') is not True for state in brain.states))

    def test_invalid_commands_never_read_transport_and_validation_rejects_action_mixing(self):
        for command in (None,'', '.fixture\ninspect','.fixture|inspect','.fixture inspect ','.'+'a'*255):
            with self.subTest(command=command),patch.object(c.Path,'read_text',side_effect=AssertionError('transport data read')):
                with self.assertRaises(c.ConversationError):c.send_lotharen_gm(object(),command)
        reply={'text':'Requested.','emote':'NONE','action':'NONE','gm_command':'.fixture inspect'}
        with self.assertRaises(c.ConversationError):c.validate_reply(reply)
        for extra in ({'action':'FOLLOW'},{'emote':'WAVE'}):
            with self.assertRaises(c.ConversationError):c.validate_reply(dict(reply,**extra),allow_gm=True)
        with patch.object(c.Path,'read_text',return_value='{"synthetic":true}'),\
             patch.object(c.gm_tasks,'single_command',return_value='aw-87') as transport:
            rpc=object();self.assertEqual(c.send_lotharen_gm(rpc,'.fixture inspect'),'aw-87')
            transport.assert_called_once_with(rpc,{'synthetic':True},'.fixture inspect',c.ROOT)

    def test_unknown_command_and_usage_pause_consume_cursor_without_retry(self):
        loop,rpc,_,statuses=gm_loop()
        with patch.object(c,'send_lotharen_gm',side_effect=c.ConversationError('outcome unconfirmed')) as send:
            loop.tick();loop.tick();send.assert_called_once()
        self.assertTrue(any(s.get('lastGmCommand',{}).get('outcome')=='send_unconfirmed' for s in statuses))
        loop,rpc,brain,statuses=gm_loop('Lotharen, hello',reply=c.IncludedUsageUnavailable('no included allowance'))
        loop.tick();loop.tick()
        self.assertEqual(len(brain.states),1)
        self.assertTrue(any(s.get('phase')=='budget_paused' for s in statuses))
        self.assertTrue(any('protect your Codex credits' in args['text'] for method,args in rpc.calls if method=='buddy-chat.send'))


class PersistentListenerTests(unittest.TestCase):
    def run_listener(self,persistent=False,cap='replies'):
        with tempfile.TemporaryDirectory() as temporary:
            folder=Path(temporary);clock=[0.];instances=[];brains=[]
            class FakeBrain:
                def __init__(self,*args):
                    self.developer_workspace=args[6];self.model=args[7];self.reasoning_effort=args[8];brains.append(self)
                def model_selection(self):return {'requestedModel':self.model,'requestedReasoningEffort':self.reasoning_effort}
            class FakeLoop:
                def __init__(self,*args,**kwargs):self.replies=0;self.ticks=0;instances.append(self)
                def status_identity(self):
                    return dict(coreSessionId=None,coreCharacterGuid=None,chatSessionId=None)
                def tick(self):
                    self.ticks+=1
                    if cap=='replies':self.replies=1
                    else:clock[0]+=40000
                    if self.ticks==3:
                        record=c.read_json(folder/'status.json')
                        c.write_json(folder/'stop.json',{'instance':record['instance']})
            argv=['run','--state-dir',str(folder),'--banter-seconds','0','--max-replies','1',
                  '--included-usage-only','--model','fixture-model','--reasoning-effort','high']
            if persistent:argv.append('--persistent')
            with patch.object(c,'CodexBrain',FakeBrain),patch.object(c,'Conversation',FakeLoop),\
                 patch.object(c,'RPC',return_value=object()),patch.object(c,'BuddyControls',return_value=object()),\
                 patch.object(c,'registered_developer_workspace',return_value=None),patch.object(c.signal,'signal'),\
                 patch.object(c.time,'monotonic',side_effect=lambda:clock[0]),\
                 patch.object(c.time,'sleep',side_effect=lambda seconds:clock.__setitem__(0,clock[0]+seconds)),\
                 patch.object(c.sys,'stdout',io.StringIO()):
                self.assertEqual(c.main(argv),0)
            record=c.read_json(folder/'status.json')
            self.assertEqual(record['phase'],'stopped');self.assertEqual(record['persistent'],persistent)
            self.assertTrue(brains[0].included_usage_only)
            self.assertEqual(record['requestedModel'],'fixture-model');self.assertEqual(record['requestedReasoningEffort'],'high')
            self.assertEqual(record['character_identity']['profile'],'default')
            return instances[0].ticks

    def test_normal_caps_and_persistent_lifetime_both_honor_same_instance_stop(self):
        for cap in ('replies','hours'):
            with self.subTest(cap=cap):
                self.assertEqual(self.run_listener(cap=cap),1)
                self.assertEqual(self.run_listener(True,cap=cap),3)


if __name__=='__main__':unittest.main()
