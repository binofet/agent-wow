"""Direct provider tests use only a local fake HTTP server and typed callbacks."""
import copy
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
import subprocess
import shutil
from pathlib import Path
import sys
import threading
import tempfile
import time
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
from direct_model import Cancelled, DirectModel, ModelError, ToolOutcomeUnknown, LMStudioPromptCounter


SCHEMA = dict(type="object", properties=dict(text=dict(type="string",maxLength=100),
    action=dict(type="string",enum=["NONE"]), count=dict(type="integer",minimum=0,maximum=10)),
    required=["text","action","count"], additionalProperties=False)
REPLY = dict(text="Ready.",action="NONE",count=0)
TOOLS = [dict(type="function",function=dict(name="observe",description="Read current facts",
    parameters=dict(type="object",properties=dict(what=dict(type="string",enum=["self"])),
                    required=["what"],additionalProperties=False)))]
MESSAGES = [dict(role="user",content="Inspect the supplied read-only fixture, then finish.")]


def chat(*calls, model="fixture-served-model"):
    return dict(model=model,usage=dict(prompt_tokens=11,completion_tokens=7,total_tokens=18),
        choices=[dict(finish_reason="tool_calls",message=dict(role="assistant",content=None,
            tool_calls=[dict(id=i,type="function",function=dict(name=n,arguments=json.dumps(a))) for i,n,a in calls]))])


class FakeHTTP:
    def __init__(self, responses):
        self.responses = list(responses)
        self.requests = []

    def __enter__(self):
        outer = self
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                raw = self.rfile.read(int(self.headers.get("Content-Length", "0")))
                observed = dict(path=self.path,headers=dict(self.headers),body=json.loads(raw))
                outer.requests.append(observed)
                if not outer.responses:
                    status, value = 500, dict(error=dict(message="unexpected extra HTTP call"))
                else:
                    value = outer.responses.pop(0)
                    if callable(value):value = value(observed)
                    status, value = value if isinstance(value,tuple) else (200,value)
                body = value if isinstance(value,bytes) else json.dumps(value).encode()
                try:
                    self.send_response(status)
                    self.send_header("Content-Type","application/json")
                    self.send_header("Content-Length",str(len(body)))
                    self.end_headers()
                    self.wfile.write(body)
                except (BrokenPipeError,ConnectionResetError):pass
            def log_message(self,*args):pass
        self.server = ThreadingHTTPServer(("127.0.0.1",0),Handler)
        self.thread = threading.Thread(target=self.server.serve_forever,daemon=True)
        self.thread.start()
        self.base = f"http://127.0.0.1:{self.server.server_port}"
        return self

    def __exit__(self,*args):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)


class DirectModelTests(unittest.TestCase):
    def generate(self, client, dispatch=lambda name,args: {}, **kwargs):
        return client.generate("Read-only fixture. Never invent game effects.",copy.deepcopy(MESSAGES),
                               copy.deepcopy(SCHEMA),copy.deepcopy(TOOLS),dispatch,**kwargs)

    def test_natural_text_and_full_native_tool_history_survive_next_owner_and_client_restart(self):
        def text(value):
            return dict(model='fixture-served-model', choices=[dict(finish_reason='stop',
                message=dict(role='assistant', content=value))])
        with tempfile.TemporaryDirectory() as directory, FakeHTTP([
                chat(('read-native','observe',dict(what='self'))), text('I see your exact state.'), text('Yes, I remember.')]) as server:
            path = Path(directory)/'conversation.json'
            first_user = 'Joel raw request: '+('unaltered owner words '*700)
            facts = 'Current full observed facts: '+('raw fact '*25000)
            client = DirectModel('local','fixture',server.base+'/v1')
            first = client.generate(facts, [dict(role='user', content=first_user)], SCHEMA, TOOLS,
                lambda *_:dict(exactObservedFact='original tool result'), natural=True,
                transcript_path=path, conversation_key='aw-1:87:chat-1:WHISPER:3', request_id='owner-1')
            self.assertEqual(first,dict(text='I see your exact state.', emote='NONE', action='NONE'))
            recreated = DirectModel('local','fixture',server.base+'/v1')
            recreated.generate('Fresh current facts', [dict(role='user', content='And now?')], SCHEMA, TOOLS,
                lambda *_: self.fail('Old tools must never be replayed'), natural=True,
                transcript_path=path, conversation_key='aw-1:87:chat-1:WHISPER:3', request_id='owner-2')
            first_payload = server.requests[0]['body']
            self.assertEqual(first_payload['tool_choice'],'auto')
            self.assertNotIn('max_tokens', first_payload)
            self.assertEqual(first_payload['messages'][0]['content'],facts)
            self.assertEqual(first_payload['messages'][1]['content'],first_user)
            self.assertNotIn('Finish using final_reply',first_payload['messages'][0]['content'])
            last = server.requests[-1]['body']['messages']
            self.assertEqual(last[0],dict(role='system',content='Fresh current facts'))
            self.assertEqual(last[1],dict(role='user',content=first_user))
            self.assertEqual(last[2]['tool_calls'][0]['id'],'read-native')
            self.assertEqual(last[3],dict(role='tool',tool_call_id='read-native',content='{"exactObservedFact":"original tool result"}'))
            self.assertEqual(last[4],dict(role='assistant',content='I see your exact state.'))
            self.assertEqual(last[5],dict(role='user',content='And now?'))
            self.assertEqual(path.stat().st_mode & 0o777,0o600)

    def test_natural_tool_exchange_has_no_implicit_sixteen_round_cutoff(self):
        final=dict(choices=[dict(finish_reason='stop',message=dict(role='assistant',content='Done observing.'))])
        calls=[chat((f'observe-{index}','observe',dict(what='self'))) for index in range(17)]
        calls[0]['choices'][0]['message']['content']='Let me check that.'
        with FakeHTTP([*calls,final]) as server:
            client=DirectModel('local','fixture',server.base+'/v1')
            dispatched,visible=[],[]
            result=self.generate(client,lambda name,args:dispatched.append(name) or dict(observed=True),
                natural=True,conversation_key='owner',assistant_text=visible.append)
            self.assertEqual(result['text'],'Done observing.')
            self.assertEqual(len(dispatched),17)
            self.assertEqual(visible,['Let me check that.'])
            self.assertEqual(len(server.requests),18)

    def test_natural_unknown_and_interruption_keep_actual_results_without_replay(self):
        text=dict(choices=[dict(finish_reason='stop',message=dict(role='assistant',content='I see the uncertain receipt.'))])
        with tempfile.TemporaryDirectory() as directory, FakeHTTP([
                chat(('entered','observe',dict(what='self'))),text,chat(('new-id','observe',dict(what='self')))]) as server:
            path=Path(directory)/'conversation.json'
            calls=[]
            def unknown(name,args):
                calls.append(name)
                error=ToolOutcomeUnknown('lost response')
                error.receipt=dict(sessionId='aw-1',replayAllowed=False,operationId='issued-once')
                raise error
            client=DirectModel('local','fixture',server.base+'/v1')
            with self.assertRaisesRegex(ModelError,'tool_outcome_unknown'):
                self.generate(client,unknown,natural=True,transcript_path=path,conversation_key='same-body',request_id='one')
            fresh=DirectModel('local','fixture',server.base+'/v1')
            self.generate(fresh,unknown,natural=True,transcript_path=path,conversation_key='same-body',request_id='two')
            rows=server.requests[1]['body']['messages']
            uncertain=json.loads(next(row['content'] for row in rows if row['role']=='tool'))
            self.assertTrue(uncertain['outcomeUnknown']);self.assertFalse(uncertain['replayAllowed'])
            self.assertEqual(uncertain['receipt']['operationId'],'issued-once')
            with self.assertRaisesRegex(ModelError,'tool_outcome_unknown'):
                self.generate(fresh,unknown,natural=True,transcript_path=path,conversation_key='same-body',request_id='three')
            self.assertEqual(calls,['observe'])
            with self.assertRaisesRegex(ModelError,'conversation_binding_changed'):
                self.generate(fresh,unknown,natural=True,transcript_path=path,conversation_key='replacement-body',request_id='four')
            self.assertEqual(len(server.requests),3)

    def test_natural_cancel_after_returned_tool_preserves_result_for_next_turn(self):
        stop=[False]
        final=dict(choices=[dict(finish_reason='stop',message=dict(role='assistant',content='I have the prior result.'))])
        with tempfile.TemporaryDirectory() as directory, FakeHTTP([chat(('observed','observe',dict(what='self'))),final]) as server:
            path=Path(directory)/'conversation.json'
            client=DirectModel('local','fixture',server.base+'/v1')
            def dispatch(*_): stop[0]=True;return dict(actualResult='retained')
            with self.assertRaises(Cancelled):
                self.generate(client,dispatch,natural=True,transcript_path=path,conversation_key='owner',
                              request_id='old',cancelled=lambda:stop[0])
            stop[0]=False
            result=self.generate(client,lambda *_:self.fail('No replay after owner interruption'),natural=True,
                transcript_path=path,conversation_key='owner',request_id='new')
            self.assertEqual(result['text'],'I have the prior result.')
            rows=server.requests[1]['body']['messages']
            self.assertEqual(json.loads(next(row['content'] for row in rows if row['role']=='tool')),dict(actualResult='retained'))

    def test_interrupted_declared_read_can_refresh_after_listener_restart(self):
        final=dict(choices=[dict(finish_reason='stop',message=dict(role='assistant',content='Fresh observation received.'))])
        with tempfile.TemporaryDirectory() as directory, FakeHTTP([
                chat(('first-read','observe',dict(what='self'))),
                final,chat(('fresh-read','observe',dict(what='self'))),final]) as server:
            path=Path(directory)/'conversation.json'
            old=DirectModel('local','fixture',server.base+'/v1')
            calls=[]
            def interrupted_read(*_): calls.append('old');raise TimeoutError('read response lost')
            self.generate(old,interrupted_read,natural=True,transcript_path=path,conversation_key='owner',
                          request_id='old',read_only_tools={'observe'})
            failed=json.loads(server.requests[1]['body']['messages'][-1]['content'])
            self.assertTrue(failed['readOnly']);self.assertTrue(failed['refreshAllowed'])
            self.assertNotIn('outcomeUnknown',failed);self.assertNotIn('replayAllowed',failed)
            new=DirectModel('local','fixture',server.base+'/v1')
            def fresh_read(*_): calls.append('fresh');return dict(observedAt='new-observation')
            result=self.generate(new,fresh_read,natural=True,transcript_path=path,conversation_key='owner',
                                 request_id='new',read_only_tools={'observe'})
            self.assertEqual(result['text'],'Fresh observation received.')
            self.assertEqual(calls,['old','fresh'])
            self.assertFalse(json.loads(path.read_text())['unknown'])
            self.assertIn('new-observation',server.requests[-1]['body']['messages'][-1]['content'])

    def test_claude_and_gemini_natural_text_use_native_auto_tool_choices(self):
        examples=[('claude',dict(stop_reason='end_turn',content=[dict(type='text',text='Hello naturally.')]),'/v1'),
            ('gemini',dict(candidates=[dict(finishReason='STOP',content=dict(role='model',parts=[
                dict(thought=True,text='Private reasoning must stay private.',thoughtSignature='opaque'),
                dict(text='Hello naturally.')]))]),'/v1beta')]
        for provider,response,suffix in examples:
            with self.subTest(provider=provider),FakeHTTP([response]) as server,patch.dict(os.environ,{'FIXTURE_KEY':'fixture'}):
                client=DirectModel(provider,'fixture',server.base+suffix,api_key_env='FIXTURE_KEY')
                result=self.generate(client,natural=True,conversation_key='owner')
                self.assertEqual(result['text'],'Hello naturally.')
                body=server.requests[0]['body']
                if provider=='claude':self.assertEqual(body['tool_choice'],dict(type='auto'))
                else:self.assertEqual(body['toolConfig']['functionCallingConfig']['mode'],'AUTO')

    def test_local_http_tool_roundtrip_final_schema_usage_and_no_caller_mutation(self):
        observed = []
        messages, tools, schema = copy.deepcopy(MESSAGES),copy.deepcopy(TOOLS),copy.deepcopy(SCHEMA)
        before = copy.deepcopy((messages,tools,schema))
        with FakeHTTP([chat(("read-1","observe",dict(what="self"))),chat(("final-1","final_reply",REPLY))]) as server:
            client = DirectModel("local","loaded-local-model",server.base+"/v1")
            with patch.dict(os.environ,dict(OPENAI_API_KEY="must-not-be-used",HTTPS_PROXY="http://unused.example:1")):
                result = client.generate("Fixture",messages,schema,tools,
                    lambda name,args: observed.append((name,args)) or dict(healthKnown=True),request_id="request-fixture")
            self.assertEqual(result,REPLY)
            self.assertEqual(observed,[("observe",dict(what="self"))])
            self.assertEqual(len(server.requests),2)
            first, second = server.requests
            self.assertEqual(first["path"],"/v1/chat/completions")
            self.assertNotIn("Authorization",first["headers"])
            self.assertEqual(first["body"]["model"],"loaded-local-model")
            self.assertEqual(first["body"]["tool_choice"],"required")
            self.assertIn("final_reply",[t["function"]["name"] for t in first["body"]["tools"]])
            self.assertEqual(second["body"]["messages"][-1],dict(role="tool",tool_call_id="read-1",content='{"healthKnown":true}'))
            self.assertEqual(client.last_receipt["usage"]["total_tokens"],36)
            self.assertEqual(client.last_receipt["providerReportedModel"],"fixture-served-model")
            self.assertEqual(client.last_receipt["execution"],"direct_api")
            self.assertEqual(client.last_receipt["tools"][0]["outcome"],"returned")
            self.assertEqual(client.last_receipt["requestId"],"request-fixture")
            self.assertIsNone(client.timeout)
        self.assertEqual((messages,tools,schema),before)

    def test_claude_native_messages_auth_tools_and_tool_result(self):
        first = dict(model="fixture-claude",stop_reason="tool_use",usage=dict(input_tokens=4,output_tokens=2),
            content=[dict(type="tool_use",id="toolu-one",name="observe",input=dict(what="self"))])
        final = dict(model="fixture-claude",stop_reason="tool_use",content=[dict(type="tool_use",id="toolu-final",name="final_reply",input=REPLY)])
        with FakeHTTP([first,final]) as server,patch.dict(os.environ,dict(FIXTURE_CLAUDE_KEY="fixture-key")):
            client=DirectModel("claude","fixture-claude",server.base+"/v1",api_key_env="FIXTURE_CLAUDE_KEY")
            self.assertEqual(self.generate(client,lambda *_:dict(observed=True)),REPLY)
            initial, followup = server.requests
            self.assertEqual(initial["path"],"/v1/messages")
            self.assertEqual(initial["headers"]["Authorization"],"Bearer fixture-key")
            self.assertEqual(initial["headers"]["anthropic-version"],"2026-10-21")
            self.assertEqual(initial["body"]["tools"][0]["input_schema"],TOOLS[0]["function"]["parameters"])
            self.assertEqual(initial["body"]["tool_choice"],dict(type="any"))
            self.assertEqual(followup["body"]["messages"][-1],dict(role="user",content=[
                dict(type="tool_result",tool_use_id="toolu-one",content='{"observed":true}')]))
            self.assertNotIn("fixture-key",json.dumps(client.last_receipt))

    def test_gemini_native_envelope_key_header_signature_and_matching_call_id(self):
        part=dict(functionCall=dict(id="g-one",name="observe",args=dict(what="self")),thoughtSignature="opaque-signed-turn")
        first=dict(modelVersion="fixture-gemini",usageMetadata=dict(promptTokenCount=5,candidatesTokenCount=2),
            candidates=[dict(finishReason="STOP",content=dict(role="model",parts=[part]))])
        final=dict(candidates=[dict(finishReason="STOP",content=dict(role="model",parts=[
            dict(functionCall=dict(name="final_reply",args=REPLY))]))])
        with FakeHTTP([first,final]) as server,patch.dict(os.environ,dict(FIXTURE_GEMINI_KEY="fixture-google-key")):
            client=DirectModel("gemini","fixture-gemini",server.base+"/v1beta",api_key_env="FIXTURE_GEMINI_KEY")
            self.assertEqual(self.generate(client,lambda *_:dict(observed=True)),REPLY)
            initial, followup=server.requests
            self.assertEqual(initial["path"],"/v1beta/models/fixture-gemini:generateContent")
            self.assertEqual(initial["headers"]["x-goog-api-key"],"fixture-google-key")
            self.assertNotIn("key=",initial["path"])
            self.assertEqual(initial["body"]["tools"][0]["functionDeclarations"][0]["parametersJsonSchema"],TOOLS[0]["function"]["parameters"])
            self.assertEqual(followup["body"]["contents"][-2]["parts"],[part])
            self.assertEqual(followup["body"]["contents"][-1],dict(role="user",parts=[
                dict(functionResponse=dict(name="observe",id="g-one",response=dict(observed=True)))]))
            self.assertNotIn("fixture-google-key",json.dumps(client.last_receipt))

    def test_cancel_while_http_waits_discards_late_tool_call_without_dispatch(self):
        arrived, cancel, release = threading.Event(),threading.Event(),threading.Event()
        def slow(_):
            arrived.set();release.wait(3)
            return chat(("late-write","observe",dict(what="self")))
        with FakeHTTP([slow]) as server:
            client=DirectModel("local","fixture",server.base+"/v1")
            dispatches=[]
            canceller=threading.Thread(target=lambda:(arrived.wait(2),cancel.set()),daemon=True)
            canceller.start()
            try:
                started=time.monotonic()
                with self.assertRaises(Cancelled) as caught:self.generate(client,lambda *a:dispatches.append(a),cancelled=cancel.is_set)
                self.assertLess(time.monotonic()-started,1)
                self.assertEqual(caught.exception.receipt["outcome"],"cancelled")
                self.assertEqual(dispatches,[])
                self.assertEqual(len(server.requests),1)
            finally:release.set();canceller.join(timeout=2)

    def test_cancel_between_tools_keeps_first_return_and_never_dispatches_second(self):
        cancel=threading.Event();seen=[]
        with FakeHTTP([chat(("one","observe",dict(what="self")),("two","observe",dict(what="self")))]) as server:
            client=DirectModel("local","fixture",server.base+"/v1")
            def dispatch(name,args):seen.append(name);cancel.set();return dict(observed=True)
            with self.assertRaises(Cancelled):self.generate(client,dispatch,cancelled=cancel.is_set)
            self.assertEqual(seen,["observe"])
            self.assertEqual(client.last_receipt["tools"],[dict(id="one",name="observe",outcome="returned")])
            self.assertEqual(len(server.requests),1)

    def test_unknown_tool_outcome_halts_without_next_model_call_or_replay(self):
        for form in ("exception","attribute","value"):
            with self.subTest(form=form),FakeHTTP([chat(("write","observe",dict(what="self")))]) as server:
                client=DirectModel("local","fixture",server.base+"/v1");seen=[]
                def dispatch(name,args):
                    seen.append(name)
                    if form=="value":return dict(outcomeUnknown=True)
                    if form=="exception":raise ToolOutcomeUnknown("already reserved, outcome unknown")
                    error=RuntimeError("dispatcher unknown");error.outcome_unknown=True;raise error
                with self.assertRaises(ModelError) as caught:self.generate(client,dispatch)
                self.assertEqual(caught.exception.reason,"tool_outcome_unknown")
                self.assertEqual(seen,["observe"])
                self.assertEqual(len(server.requests),1)
                self.assertEqual(client.last_receipt["tools"][0]["outcome"],"unknown")

    def test_provider_failure_retains_bounded_actual_error_without_fallback(self):
        with FakeHTTP([(401,dict(error=dict(type="authentication_error",code=401,
                message="Provider key rejected. api_key=fixture-private-secret")))]) as server:
            client=DirectModel("openai","fixture",server.base+"/v1",api_key_env="FIXTURE_OPENAI_KEY")
            with patch.dict(os.environ,dict(FIXTURE_OPENAI_KEY="fixture-secret")):
                with self.assertRaises(ModelError) as caught:self.generate(client)
            self.assertEqual(caught.exception.reason,"provider_http_error")
            self.assertEqual(caught.exception.receipt["httpStatus"],401)
            self.assertEqual(caught.exception.receipt["providerError"],dict(type="authentication_error",code=401,message="Provider key rejected. api_key=[redacted]"))
            self.assertEqual(len(server.requests),1)
            self.assertNotIn("fixture-private-secret",json.dumps(client.last_receipt))

    def test_unknown_malformed_or_mixed_final_batch_is_rejected_before_any_tool(self):
        invalid=[chat(("x","not_registered",{})),
            chat(("x","observe",dict(what="self")),("f","final_reply",REPLY)),
            chat(("x","observe",dict(what="other"))),
            chat(("x","final_reply",dict(REPLY,extra=True))),
            chat(("x","final_reply",dict(text="incomplete"))),
            chat(("x","final_reply",dict(REPLY,count=True)))]
        for response in invalid:
            with self.subTest(response=response),FakeHTTP([response]) as server:
                client=DirectModel("local","fixture",server.base+"/v1");seen=[]
                with self.assertRaises(ModelError):self.generate(client,lambda *a:seen.append(a))
                self.assertEqual(seen,[])
                self.assertEqual(len(server.requests),1)

    def test_same_call_id_returns_cached_receipt_but_changed_identity_never_reexecutes(self):
        for changed in (False,True):
            with self.subTest(changed=changed):
                second=chat(("same","final_reply",REPLY)) if changed else chat(("same","observe",dict(what="self")))
                with FakeHTTP([chat(("same","observe",dict(what="self"))),second,chat(("done","final_reply",REPLY))]) as server:
                    client=DirectModel("local","fixture",server.base+"/v1");seen=[]
                    if changed:
                        with self.assertRaises(ModelError) as caught:self.generate(client,lambda *a:seen.append(a) or {})
                        self.assertEqual(caught.exception.reason,"tool_call_identity_changed")
                    else:self.assertEqual(self.generate(client,lambda *a:seen.append(a) or dict(observed=True)),REPLY)
                    self.assertEqual(len(seen),1)

    def test_explicit_timeout_and_response_bound_do_not_dispatch_or_retry(self):
        with FakeHTTP([b" "*2000]) as server:
            client=DirectModel("local","fixture",server.base+"/v1",max_response_bytes=128)
            with self.assertRaises(ModelError) as caught:self.generate(client)
            self.assertEqual(caught.exception.reason,"provider_response_bound")
            self.assertEqual(len(server.requests),1)
        arrived,release=threading.Event(),threading.Event()
        def wait(_):arrived.set();release.wait(2);return chat(("late","final_reply",REPLY))
        with FakeHTTP([wait]) as server:
            client=DirectModel("local","fixture",server.base+"/v1",timeout=.1)
            try:
                with self.assertRaises(ModelError) as caught:self.generate(client)
                self.assertEqual(caught.exception.reason,"timeout")
                self.assertEqual(len(server.requests),1)
            finally:release.set()

    def test_round_bound_missing_key_and_configuration_fail_closed(self):
        with FakeHTTP([chat(("one","observe",dict(what="self")))]) as server:
            client=DirectModel("local","fixture",server.base+"/v1",max_tool_rounds=1)
            with self.assertRaises(ModelError) as caught:self.generate(client)
            self.assertEqual(caught.exception.reason,"tool_round_bound")
            self.assertEqual(len(server.requests),1)
        with FakeHTTP([]) as server:
            client=DirectModel("gemini","fixture",server.base+"/v1beta",api_key_env="UNSET_FIXTURE_MODEL_KEY")
            with patch.dict(os.environ,{},clear=True):
                with self.assertRaises(ModelError) as caught:self.generate(client)
            self.assertEqual(caught.exception.reason,"provider_key_unavailable")
            self.assertEqual(server.requests,[])
        for kwargs in (dict(provider="codex",model="fixture",base_url="http://127.0.0.1/v1"),
                dict(provider="local",model="fixture",base_url="https://remote.example/v1"),
                dict(provider="local",model="fixture",base_url="http://user:secret@127.0.0.1/v1"),
                dict(provider="claude",model="fixture",base_url="https://api.anthropic.com/v1")):
            with self.subTest(kwargs=kwargs),self.assertRaises(ModelError):DirectModel(**kwargs)


class ActiveContextTests(unittest.TestCase):
    @staticmethod
    def text(value='The recorded observations are available.'):
        return dict(choices=[dict(finish_reason='stop',message=dict(role='assistant',content=value))])

    @staticmethod
    def tools(*names):
        return [dict(type='function',function=dict(name=name,description='Synthetic typed read or action',
            parameters=dict(type='object',properties=dict(key=dict(type='string')),
                            required=['key'],additionalProperties=False))) for name in names]

    def test_109_reads_keep_full_journal_but_supersede_redundant_api_bodies_after_restart(self):
        names=['session_catalog']*39+['read_observation']*70
        responses=[chat((f'read-{index}',name,dict(key='same-bound-query'))) for index,name in enumerate(names)]
        human='Exact owner intent, never clipped: '+('original human words '*900)
        with tempfile.TemporaryDirectory() as folder,FakeHTTP([*responses,self.text(),self.text('Continue naturally.')]) as server:
            path=Path(folder)/'conversation.json';dispatched=[]
            def read(name,args):
                dispatched.append((name,args))
                return dict(ok=True,result=dict(binding='aw-1:87',observedAt=len(dispatched),
                    facts=('complete catalog mechanics '*600 if name=='session_catalog' else 'complete own observation '*300)))
            client=DirectModel('local','fixture',server.base+'/v1')
            client.generate('Current facts, not a historical freshness claim.',[dict(role='user',content=human)],None,
                self.tools(*set(names)),read,natural=True,transcript_path=path,conversation_key='aw-1:87:chat:owner',
                request_id='owner-one',read_only_tools=set(names))
            original_raw=path.read_bytes();original=json.loads(original_raw)
            raw_results=[row for row in original['messages'] if row['role']=='tool']
            self.assertEqual(len(raw_results),109)
            self.assertTrue(all('complete ' in row['content'] for row in raw_results))
            self.assertNotIn('historicalRead',original_raw.decode())
            # Computing the active view does not write or replace canonical bytes.
            client._active_conversation(original['messages'],set(names),original['unknown'],original['pending'])
            self.assertEqual(path.read_bytes(),original_raw)
            recreated=DirectModel('local','fixture',server.base+'/v1')
            recreated.generate('Fresh current system facts.',[dict(role='user',content='Keep the original intent.')],None,
                self.tools(*set(names)),lambda *_:self.fail('History must not be redispatched'),natural=True,
                transcript_path=path,conversation_key='aw-1:87:chat:owner',request_id='owner-two',read_only_tools=set(names))
            active=server.requests[-1]['body']['messages']
            self.assertEqual(active[0],dict(role='system',content='Fresh current system facts.'))
            self.assertEqual(active[1],dict(role='user',content=human))
            self.assertEqual(active[-1],dict(role='user',content='Keep the original intent.'))
            calls=[call['id'] for row in active for call in row.get('tool_calls',[])]
            results=[row for row in active if row['role']=='tool']
            self.assertEqual(calls,[row['tool_call_id'] for row in results])
            markers=[json.loads(row['content']) for row in results if json.loads(row['content']).get('historicalRead')]
            self.assertEqual(len(markers),107)
            for marker in markers:
                old=original['messages'][marker['original']['transcriptMessage']]['content']
                self.assertEqual(marker['resultSHA256'],hashlib.sha256(old.encode()).hexdigest())
                latest=original['messages'][marker['supersededBy']['transcriptMessage']]
                self.assertEqual(latest['tool_call_id'],marker['supersededBy']['toolCallId'])
            kept=[json.loads(row['content'])['result']['observedAt'] for row in results
                  if not json.loads(row['content']).get('historicalRead')]
            self.assertEqual(kept,[39,109])
            raw_size=len(json.dumps(original['messages'],ensure_ascii=False).encode())
            self.assertLess(len(json.dumps(active,ensure_ascii=False).encode()),raw_size/3)
            final=json.loads(path.read_text())
            self.assertEqual(final['messages'][:len(original['messages'])],original['messages'])
            self.assertEqual(final['seen']['owner-one'],original['seen']['owner-one'])
            self.assertEqual(dispatched,[(name,dict(key='same-bound-query')) for name in names])
            self.assertEqual(recreated.last_receipt['httpRequests'],1)
            self.assertEqual(recreated.last_receipt['contextProjection']['supersededReadResults'],107)

    def test_mutations_and_unresolved_read_custody_remain_verbatim(self):
        sequence=[('write','mutate','action'),('old','observe','ordinary'),('pending','observe','pending'),
                  ('fresh','observe','ordinary'),('settled','observe','pending')]
        large='retained raw facts '*500
        with tempfile.TemporaryDirectory() as folder,FakeHTTP([
                *[chat((identity,name,dict(key=key))) for identity,name,key in sequence],self.text(),self.text()]) as server:
            path=Path(folder)/'conversation.json'
            def dispatch(name,args):
                if name=='mutate':return dict(transportWritten=True,gameEffectClaimed=False,receipt=large)
                if args['key']=='pending' and not getattr(self,'pending_read',False):
                    self.pending_read=True
                    return dict(ok=True,result=dict(pendingActionIds=['issued-once'],raw=large))
                return dict(ok=True,result=dict(raw=large))
            kwargs=dict(natural=True,transcript_path=path,conversation_key='same-body',read_only_tools={'observe'})
            client=DirectModel('local','fixture',server.base+'/v1')
            client.generate('Facts.',[dict(role='user',content='One explicit action.')],None,
                self.tools('observe','mutate'),dispatch,request_id='first',**kwargs)
            original=json.loads(path.read_text())
            fresh=DirectModel('local','fixture',server.base+'/v1')
            fresh.generate('New facts.',[dict(role='user',content='Inspect the receipts.')],None,
                self.tools('observe','mutate'),lambda *_:self.fail('No action replay'),request_id='second',**kwargs)
            results={row['tool_call_id']:row['content'] for row in server.requests[-1]['body']['messages'] if row['role']=='tool'}
            originals={row['tool_call_id']:row['content'] for row in original['messages'] if row['role']=='tool'}
            self.assertEqual(results['write'],originals['write'])
            self.assertEqual(results['pending'],originals['pending'])
            self.assertTrue(json.loads(results['old'])['historicalRead'])
            self.assertEqual(results['fresh'],originals['fresh'])
            self.assertEqual(results['settled'],originals['settled'])
            self.assertEqual(json.loads(path.read_text())['messages'][:len(original['messages'])],original['messages'])

    def test_native_claude_and_gemini_pairs_and_opaque_parts_survive_projection(self):
        for provider in ('claude','gemini'):
            with self.subTest(provider=provider),tempfile.TemporaryDirectory() as folder:
                def call(identifier):
                    if provider=='claude':return dict(stop_reason='tool_use',content=[dict(type='tool_use',id=identifier,name='observe',input=dict(key='same'))])
                    return dict(candidates=[dict(finishReason='STOP',content=dict(role='model',parts=[
                        dict(functionCall=dict(id=identifier,name='observe',args=dict(key='same')),thoughtSignature='opaque-'+identifier)]))])
                final=(dict(stop_reason='end_turn',content=[dict(type='text',text='Observed.')]) if provider=='claude' else
                       dict(candidates=[dict(finishReason='STOP',content=dict(role='model',parts=[dict(text='Observed.')]))]))
                with FakeHTTP([call('first'),call('latest'),final]) as server,patch.dict(os.environ,{'FIXTURE_KEY':'test-only'}):
                    client=DirectModel(provider,'fixture',server.base+('/v1' if provider=='claude' else '/v1beta'),api_key_env='FIXTURE_KEY')
                    path=Path(folder)/'conversation.json'
                    client.generate('Facts.',[dict(role='user',content='Read twice.')],None,self.tools('observe'),
                        lambda *_:dict(facts='full facts '*1000),natural=True,transcript_path=path,
                        conversation_key='bound',request_id='owner',read_only_tools={'observe'})
                    wire=server.requests[-1]['body']['messages' if provider=='claude' else 'contents']
                    original=json.loads(path.read_text())['messages']
                    if provider=='claude':
                        first=json.loads(wire[2]['content'][0]['content'])
                        self.assertEqual(wire[2]['content'][0]['tool_use_id'],'first')
                        self.assertEqual(wire[4]['content'][0],original[4]['content'][0])
                    else:
                        first=wire[2]['parts'][0]['functionResponse']['response']
                        self.assertEqual(wire[2]['parts'][0]['functionResponse']['id'],'first')
                        self.assertEqual(wire[1]['parts'],original[1]['parts'])
                        self.assertEqual(wire[3]['parts'][0]['thoughtSignature'],'opaque-latest')
                        self.assertEqual(wire[4]['parts'],original[4]['parts'])
                    self.assertTrue(first['historicalRead'])
                    self.assertIn('full facts',json.dumps(original[2]))

    def test_native_unconfirmed_read_phases_are_pinned_even_after_newer_read(self):
        for phase in ('CANCEL_SENT','ACCEPT_SENT','waiting_server','outcome_unconfirmed'):
            with self.subTest(phase=phase),FakeHTTP([
                    chat(('old','observe',dict(key='native-state'))),
                    chat(('latest','observe',dict(key='native-state'))),self.text()]) as server:
                client=DirectModel('local','fixture',server.base+'/v1');count=[0]
                def read(*_):
                    count[0]+=1
                    return dict(ok=True,result=dict(lastBuff=dict(phase=phase if count[0]==1 else 'completed'),
                        raw='native action evidence '*500))
                client.generate('Facts.',[dict(role='user',content='Inspect, without retrying.')],None,
                    self.tools('observe'),read,natural=True,conversation_key='bound',read_only_tools={'observe'})
                results=[row for row in server.requests[-1]['body']['messages'] if row['role']=='tool']
                self.assertEqual(json.loads(results[0]['content'])['result']['lastBuff']['phase'],phase)
                self.assertFalse(json.loads(results[0]['content']).get('historicalRead',False))
                self.assertEqual(json.loads(results[1]['content'])['result']['lastBuff']['phase'],'completed')

    def test_pending_unknown_distinct_queries_and_small_results_are_not_superseded(self):
        client=DirectModel('local','fixture','http://127.0.0.1/v1');client.last_receipt={}
        rows=[]
        for identity,key,result in [('old','same',dict(facts='a'*2000)),('new','same',dict(facts='b'*2000)),
                                    ('page','another-page',dict(facts='c'*2000)),('small','same',dict(ok=True))]:
            rows.extend([chat((identity,'observe',dict(key=key)))['choices'][0]['message'],
                         dict(role='tool',tool_call_id=identity,content=json.dumps(result))])
        self.assertEqual(client._active_conversation(rows,{'observe'},{},dict(calls=['pending'])),rows)
        fingerprint=hashlib.sha256(json.dumps(['observe',dict(key='same')],separators=(',',':')).encode()).hexdigest()
        self.assertEqual(client._active_conversation(rows,{'observe'},{fingerprint:dict(outcomeUnknown=True)},None),rows)
        active=client._active_conversation(rows,{'observe'},{},None)
        self.assertEqual(active[5],rows[5]) # Another query remains available verbatim.
        self.assertEqual(active[-1],rows[-1]) # Latest small response is not inflated.
        self.assertTrue(json.loads(active[1]['content'])['historicalRead'])

    def test_provider_http_failures_retain_safe_reason_and_status_without_raw_dump(self):
        cases=[(dict(error={},code='context_size_exceeded',reason='failed to find free space in KV cache'),
                dict(code='context_size_exceeded',reason='failed to find free space in KV cache')),
               (dict(error='Context size exceeded'),dict(message='Context size exceeded')),
               (dict(error={}),dict(reason='empty_error_object')),
               (dict(detail='No available slot; authorization=private-token'),dict(message='No available slot; authorization=[redacted]')),
               (b'<html>Bearer private-token</html>',dict(reason='non_json_error_response',responseBytes=33))]
        for value,expected in cases:
            with self.subTest(value=value),FakeHTTP([(400,value)]) as server:
                client=DirectModel('local','fixture',server.base+'/v1')
                with self.assertRaises(ModelError) as caught:
                    client.generate('Do not echo this private prompt.',[dict(role='user',content='Original user message.')],None,
                        [],lambda *_:self.fail('HTTP failure must not dispatch'),natural=True,conversation_key='owner')
                self.assertEqual(caught.exception.reason,'provider_http_error')
                self.assertEqual(caught.exception.receipt['httpStatus'],400)
                self.assertEqual(caught.exception.receipt['providerError'],expected)
                self.assertEqual(caught.exception.receipt['toolCalls'],0)
                self.assertEqual(len(server.requests),1)
                self.assertNotIn('private-token',json.dumps(caught.exception.receipt))
                self.assertNotIn('private prompt',json.dumps(caught.exception.receipt))

    def test_one_availability_retry_preserves_the_exact_window_and_never_redispatches_mutation(self):
        for status in (429,503):
            with self.subTest(status=status),FakeHTTP([chat(('once','mutate',dict(key='explicit-action'))),
                    (status,dict(error=dict(code='slot_pressure',message='No available inference slot.'))),self.text()]) as server:
                client=DirectModel('local','fixture',server.base+'/v1');writes=[]
                client.generate('Current facts.',[dict(role='user',content='One explicit operation.')],None,
                    self.tools('mutate'),lambda *args:writes.append(args) or dict(transportWritten=True,replayAllowed=False),
                    natural=True,conversation_key='same-body',request_id='same-owner')
                self.assertEqual(len(writes),1)
                self.assertEqual(len(server.requests),3)
                self.assertEqual(server.requests[1]['body'],server.requests[2]['body'])
                self.assertIn('"transportWritten":true',server.requests[2]['body']['messages'][-1]['content'])
                self.assertEqual(client.last_receipt['httpStatus'],200)
                self.assertTrue(client.last_receipt['availabilityRecovery']['recovered'])
                self.assertEqual(client.last_receipt['availabilityRecovery']['httpStatus'],status)
                self.assertNotIn('providerError',client.last_receipt)

    def test_availability_recovery_is_only_once_and_never_retries_an_unchanged_400(self):
        for status,count in ((503,2),(429,2),(400,1)):
            with self.subTest(status=status),FakeHTTP([(status,dict(error=dict(code='capacity_error')))]*count) as server:
                client=DirectModel('local','fixture',server.base+'/v1')
                with self.assertRaises(ModelError) as caught:
                    client.generate('Facts.',[dict(role='user',content='Original request.')],None,[],
                        lambda *_:self.fail('HTTP failures cannot dispatch'),natural=True,conversation_key='owner')
                self.assertEqual(caught.exception.reason,'provider_http_error')
                self.assertEqual(caught.exception.receipt['httpStatus'],status)
                self.assertEqual(len(server.requests),count)
                if count==2:self.assertEqual(server.requests[0]['body'],server.requests[1]['body'])

    def test_availability_backoff_honors_fresh_owner_interrupt_and_explicit_deadline(self):
        for interrupt in (True,False):
            with self.subTest(interrupt=interrupt),FakeHTTP([(503,dict(error='No available slot.'))]) as server:
                cancel=threading.Event();client=DirectModel('local','fixture',server.base+'/v1',timeout=None if interrupt else .1)
                original_sleep=time.sleep
                def sleep(seconds):
                    if interrupt and client.last_receipt.get('availabilityRecovery'):cancel.set()
                    else:original_sleep(seconds)
                with patch('direct_model.time.sleep',side_effect=sleep),self.assertRaises(ModelError) as caught:
                    client.generate('Facts.',[dict(role='user',content='Original request.')],None,[],
                        lambda *_:self.fail('No dispatch during backoff'),natural=True,conversation_key='owner',cancelled=cancel.is_set)
                self.assertEqual(caught.exception.reason,'cancelled' if interrupt else 'timeout')
                self.assertFalse(caught.exception.receipt['availabilityRecovery']['retryStarted'])
                self.assertEqual(len(server.requests),1)


class LocalPromptBudgetTests(unittest.TestCase):
    def test_tool_aware_sdk_meter_uses_only_fake_websocket_and_bounded_subprocess(self):
        if shutil.which('node') is None:self.skipTest('Node is not installed; SDK mode requires Node22')
        result=subprocess.run(['node',str(Path(__file__).with_name('test_local_prompt_meter.mjs'))],
            capture_output=True,timeout=10)
        self.assertEqual(result.returncode,0,result.stdout.decode()+result.stderr.decode())
        self.assertIn('# tests 3',result.stdout.decode())
        calls=[]
        def runner(argv,**kwargs):
            calls.append((argv,kwargs))
            return type('Reply',(),dict(returncode=0,stdout=json.dumps(dict(model='loaded',tokens=237,
                scope='sdk_tool_aware_template',instanceReference='exact')).encode()))()
        counter=LMStudioPromptCounter('ws://127.0.0.1:1234','loaded',runner=runner)
        self.assertEqual(counter(dict(model='loaded',messages=[],tools=[])),237)
        self.assertEqual(calls[0][1]['timeout'],6)
        self.assertEqual(json.loads(calls[0][1]['input'])['model'],'loaded')
        self.assertEqual(counter.last_measurement['instanceReference'],'exact')
        for url in ('ws://remote.invalid:1234','ws://u:p@127.0.0.1:1234','ws://127.0.0.1:1234/path'):
            with self.assertRaises(ModelError):LMStudioPromptCounter(url,'loaded',runner=runner)

    class Transport(DirectModel):
        """Scripted provider transport only: no socket or real model."""
        def __init__(self, replies, **kwargs):
            super().__init__('local', 'fixture', 'http://127.0.0.1:1/v1', **kwargs)
            self.replies, self.sent = list(replies), []
        def _http(self, payload, cancelled, deadline, progress=None):
            self._check(cancelled, deadline)
            self.sent.append(copy.deepcopy(payload))
            self.last_receipt['httpRequests'] += 1
            value = self.replies.pop(0)
            if isinstance(value, tuple):
                self.last_receipt.update(httpStatus=value[0], providerError=value[1])
                raise ModelError('provider_http_error')
            return value

    def exchange(self, client, dispatch, path=None, **kwargs):
        return client.generate('Exact current SYSTEM; do not alter.',
            [dict(role='user', content='Original owner goal, with every constraint.')], None,
            ActiveContextTests.tools('observe', 'mutate'), dispatch, natural=True,
            transcript_path=path, conversation_key='body:87:owner', request_id='same-request',
            read_only_tools={'observe'}, **kwargs)

    def test_late_overflow_compacts_history_without_reentering_completed_mutation(self):
        replies = [chat(('r0', 'observe', dict(key='old'))),
            chat(('r1', 'observe', dict(key='custody'))), chat(('r2', 'observe', dict(key='recent'))),
            chat(('write', 'mutate', dict(key='authorized'))),
            (400, dict(code=500, message='Context size has been exceeded.')),
            chat(('r3','observe',dict(key='continuation'))), ActiveContextTests.text()]
        client = self.Transport(replies, local_prompt_budget_bytes=200000, local_max_output_tokens=4096)
        writes = []
        def dispatch(name, args):
            if name == 'mutate':
                writes.append(args)
                return dict(ok=True, operationId='physical-write-1', commandWritten=True,
                    outcomeUnknown=False, replayAllowed=False, gameEffectClaimed=False)
            return dict(ok=True, result=dict(raw=args['key']*2000,
                pendingActionIds=['unresolved-original'] if args['key'] == 'custody' else []))
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder)/'canonical.json'
            self.exchange(client, dispatch, path)
            self.assertEqual(writes, [dict(key='authorized')])
            before, after = client.sent[4:6]
            self.assertEqual(before['messages'][0], after['messages'][0])
            self.assertEqual(before['tools'], after['tools'])
            self.assertEqual(after['max_tokens'], 4096)
            old = {r['tool_call_id']:r for r in before['messages'] if r['role']=='tool'}
            new = {r['tool_call_id']:r for r in after['messages'] if r['role']=='tool'}
            marker = json.loads(new['r0']['content'])['hostArchivedRead']
            self.assertEqual(marker['resultSHA256'], hashlib.sha256(old['r0']['content'].encode()).hexdigest())
            for identity in ('r1', 'r2', 'write'): self.assertEqual(old[identity], new[identity])
            for left, right in zip(before['messages'], after['messages']):
                if left['role'] != 'tool': self.assertEqual(left, right)
            canonical = json.loads(path.read_text())
            originals={r['tool_call_id']:r for r in canonical['messages'] if r['role']=='tool'}
            self.assertEqual({identity:originals[identity] for identity in old},old)
            self.assertNotIn('hostArchivedRead', json.dumps(canonical['messages']))
            self.assertEqual(canonical['contextProjections'][-1]['reason'], 'retained_overflow_projection')
            self.assertTrue(canonical['compactHistoricalReads'])
            self.assertIn('hostArchivedRead',next(r['content'] for r in client.sent[-1]['messages'] if r.get('tool_call_id')=='r0'))
            self.assertEqual(client.last_receipt['toolCalls'], 5)
            self.assertTrue(client.last_receipt['contextOverflowRecovery']['recovered'])
            self.assertFalse(client.last_receipt['promptBudget']['tokenCountKnown'])
            # Reopening the same request never starts a second mutation exchange.
            reopened = self.Transport([], local_prompt_budget_bytes=200000)
            with self.assertRaises(ModelError) as caught: self.exchange(reopened, dispatch, path)
            self.assertEqual(caught.exception.reason, 'conversation_request_already_entered')
            self.assertEqual(writes, [dict(key='authorized')])
            restarted=self.Transport([ActiveContextTests.text()],local_prompt_budget_bytes=200000)
            restarted.generate('Exact current SYSTEM; do not alter.',
                [dict(role='user',content='Later genuine owner clarification.')],None,
                ActiveContextTests.tools('observe','mutate'),lambda *_:self.fail('History must not redispatch'),
                natural=True,transcript_path=path,conversation_key='body:87:owner',
                request_id='clarification',read_only_tools={'observe'})
            wire=restarted.sent[0]['messages']
            self.assertEqual([r['content'] for r in wire if r['role']=='user'],
                ['Original owner goal, with every constraint.','Later genuine owner clarification.'])
            self.assertEqual(next(r for r in wire if r.get('tool_call_id')=='write'),old['write'])
            self.assertEqual(json.loads(path.read_text())['messages'][:len(canonical['messages'])],canonical['messages'])
            self.assertEqual(restarted.last_receipt['promptBudget']['reason'],'retained_overflow_projection')

    def test_configured_budget_uses_only_old_reads_and_preserves_latest_pairs(self):
        client = self.Transport([], local_prompt_budget_bytes=4500)
        client.last_receipt = {}
        rows = [dict(role='user', content='Exact goal')]
        for identity in ('old', 'recent1', 'recent2'):
            rows += [chat((identity, 'observe', dict(key=identity)))['choices'][0]['message'],
                dict(role='tool', tool_call_id=identity, content=json.dumps(dict(facts='x'*1500)))]
        original = copy.deepcopy(rows)
        payload, audit = client._local_prompt('System', rows, [], {'observe'}, {}, None, canonical=rows)
        self.assertLessEqual(audit['afterBytes'], 4500)
        self.assertEqual(len(audit['archivedReadResults']), 1)
        self.assertEqual(payload['messages'][-4:], rows[-4:])
        self.assertEqual(rows, original)

    def test_second_overflow_halts_same_request_without_a_second_recovery(self):
        error=(400,dict(code='context_length_exceeded',message='Context size has been exceeded.'))
        client=self.Transport([chat(('r0','observe',dict(key='old'))),
            chat(('r1','observe',dict(key='middle'))),chat(('write','mutate',dict(key='once'))),error,error],
            local_prompt_budget_bytes=200000)
        writes=[]
        with tempfile.TemporaryDirectory() as folder, self.assertRaises(ModelError) as caught:
            self.exchange(client,lambda name,args: writes.append(args) or dict(commandWritten=True)
                if name=='mutate' else dict(facts='x'*3000),Path(folder)/'canonical.json')
        self.assertEqual(caught.exception.reason,'provider_http_error')
        self.assertEqual(writes,[dict(key='once')])
        self.assertEqual(len(client.sent),5)
        self.assertTrue(client.last_receipt['contextOverflowRecovery']['retryStarted'])
        self.assertFalse(client.last_receipt['contextOverflowRecovery']['recovered'])

    def test_protected_floor_after_write_fails_without_retry_or_lost_receipt(self):
        client = self.Transport([chat(('write', 'mutate', dict(key='once')))], local_prompt_budget_bytes=4000)
        writes = []
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder)/'canonical.json'
            with self.assertRaises(ModelError) as caught:
                self.exchange(client, lambda *_: writes.append('once') or dict(
                    outcomeUnknown=False, commandWritten=True, operationId='keep-exact', raw='receipt'*1000), path)
            self.assertEqual(caught.exception.reason, 'local_prompt_protected_floor_exceeds_budget')
            self.assertEqual(writes, ['once'])
            self.assertEqual(len(client.sent), 1)
            state = json.loads(path.read_text())
            self.assertIsNone(state['pending'])
            self.assertIn('keep-exact', state['messages'][-1]['content'])
            self.assertEqual(state['messages'][-1]['tool_call_id'], 'write')
            self.assertEqual(state['contextProjections'][-1]['archivedReadResults'], [])

    def test_overflow_without_safe_reduction_never_retries(self):
        for error in [(400, dict(message='Context size has been exceeded.')),
                      (400, dict(message='Invalid schema')), (500, dict(message='Context size has been exceeded.'))]:
            with self.subTest(error=error):
                client = self.Transport([error], local_prompt_budget_bytes=200000)
                with tempfile.TemporaryDirectory() as folder, self.assertRaises(ModelError):
                    self.exchange(client, lambda *_:self.fail('no tools'), Path(folder)/'canonical.json')
                self.assertEqual(len(client.sent), 1)
                self.assertFalse(client.last_receipt.get('contextOverflowRecovery', {}).get('retryStarted', False))

    def test_exact_payload_counter_is_optional_and_never_estimated_from_bytes(self):
        client = self.Transport([], local_prompt_token_limit=100,
            local_token_counter=lambda p:80 if 'hostArchivedRead' in json.dumps(p) else 200)
        client.last_receipt = {}
        rows=[]
        for identity in ('old', 'recent1', 'recent2'):
            rows += [chat((identity,'observe',dict(key=identity)))['choices'][0]['message'],
                dict(role='tool',tool_call_id=identity,content=json.dumps(dict(facts='x'*1500)))]
        _, audit = client._local_prompt('System', rows, [], {'observe'}, {}, None, canonical=rows)
        self.assertEqual((audit['beforeTokens'], audit['afterTokens']), (200, 80))
        self.assertTrue(audit['tokenCountKnown'])
        for counter in (lambda _:True, lambda _:-1, lambda _:None):
            client.local_token_counter=counter
            with self.assertRaises(ModelError): client._local_prompt('System',rows,[],{'observe'},{},None,canonical=rows)
        for kwargs in (dict(local_prompt_token_limit=100),dict(local_prompt_budget_bytes=True),
                       dict(local_max_output_tokens=False)):
            with self.assertRaises(ModelError): self.Transport([],**kwargs)
        with self.assertRaises(ModelError):
            DirectModel('openai','fixture','https://example.invalid/v1',api_key_env='FIXTURE',local_prompt_budget_bytes=5000)

    def test_default_local_and_other_provider_payloads_remain_unchanged(self):
        local = self.Transport([])
        self.assertNotIn('max_tokens', local._payload('System',[],[],natural=True))
        remote=DirectModel('openai','fixture','https://example.invalid/v1',api_key_env='FIXTURE')
        self.assertNotIn('max_tokens',remote._payload('System',[],[],natural=True))

    def test_archival_requires_durable_storage_before_any_http_or_dispatch(self):
        client=self.Transport([],local_prompt_budget_bytes=10000)
        with self.assertRaises(ModelError) as caught: self.exchange(client,lambda *_:self.fail('no dispatch'))
        self.assertEqual(caught.exception.reason,'local_prompt_budget_requires_durable_transcript')
        self.assertEqual(client.sent,[])

    def test_completed_receipts_inside_old_reconciliation_reads_are_not_archived(self):
        client=self.Transport([],local_prompt_budget_bytes=1000)
        client.last_receipt={}
        for evidence in (dict(lastSpellOperation=dict(phase='effect_observed',spellId=9474)),
                         dict(receipt=dict(operationId='exact-settled',outcome='completed')),
                         dict(commandWritten=False,knownNotDispatched=True)):
            rows=[]
            for identity in ('old','recent1','recent2'):
                rows += [chat((identity,'observe',dict(key=identity)))['choices'][0]['message'],
                    dict(role='tool',tool_call_id=identity,content=json.dumps(dict(facts='x'*1500,**evidence)))]
            with self.assertRaises(ModelError):
                client._local_prompt('System',rows,[],{'observe'},{},None,canonical=rows)
            self.assertEqual(client.last_receipt['promptBudget']['archivedReadResults'],[])

    def test_auxiliary_assistant_reasoning_archives_only_in_opt_in_api_copy(self):
        client=self.Transport([],local_prompt_budget_bytes=200000);client.last_receipt={}
        rows=[dict(role='user',content='Exact original goal'),
            dict(role='assistant',content='Historical discussion',reasoning_content='Old auxiliary thinking '*100)]
        for identity in ('old','recent1','recent2'):
            rows += [chat((identity,'observe',dict(key=identity)))['choices'][0]['message'],
                dict(role='tool',tool_call_id=identity,content=json.dumps(dict(facts='x'*1500)))]
        original=copy.deepcopy(rows)
        payload,audit=client._local_prompt('Exact SYSTEM',rows,[],{'observe'},{},None,canonical=rows)
        self.assertNotIn('reasoning_content',payload['messages'][2])
        self.assertEqual(payload['messages'][3:],rows[2:])
        self.assertEqual(rows,original)
        self.assertEqual(audit['archivedAssistantReasoning'][0]['sha256'],hashlib.sha256(rows[1]['reasoning_content'].encode()).hexdigest())

    def test_duplicate_receipts_and_current_two_batches_are_protected_at_both_stages(self):
        client=self.Transport([],local_prompt_budget_bytes=200000);client.last_receipt={}
        rows=[dict(role='user',content='Exact original goal')]
        for identity,key,result in [('receipt0','receipt-query',dict(receipt=dict(phase='completed',spellId=9474),raw='r'*2000)),
                ('receipt1','receipt-query',dict(receipt=dict(phase='completed',spellId=9474),raw='s'*2000)),
                ('recent0','same-read',dict(facts='a'*2000)),('recent1','same-read',dict(facts='b'*2000))]:
            rows += [dict(chat((identity,'observe',dict(key=key)))['choices'][0]['message'],reasoning_content='Recent reasoning'),
                dict(role='tool',tool_call_id=identity,content=json.dumps(result))]
        original=copy.deepcopy(rows)
        ordinary=client._active_conversation(rows,{'observe'},{},None)
        self.assertTrue(json.loads(ordinary[2]['content'])['historicalRead'])
        self.assertTrue(json.loads(ordinary[6]['content'])['historicalRead'])
        protected=client._active_conversation(rows,{'observe'},{},None,protect_local=True)
        self.assertEqual(protected,rows)
        # Even an already-projected caller cannot smuggle the older markers
        # through the budget stage in place of canonical protected evidence.
        payload,audit=client._local_prompt('System',ordinary,[],{'observe'},{},None,canonical=rows,aggressive=True)
        semantics=copy.deepcopy(rows)
        for message in semantics:
            if message['role']=='assistant':message.pop('reasoning_content',None)
        self.assertEqual(payload['messages'][1:],semantics)
        self.assertEqual(audit['archivedReadResults'],[])
        self.assertEqual(rows,original)

    def test_protected_mixed_batches_drop_only_auxiliary_keys_and_pass_real_sdk_input(self):
        if shutil.which('node') is None:self.skipTest('Node required for SDK input converter')
        client=self.Transport([],local_prompt_budget_bytes=200000);client.last_receipt={}
        rows=[dict(role='user',content='Exact latest owner goal')]
        for group in ('old','recent1','recent2'):
            native=chat((group+'-read','observe',dict(key='receipt')),
                (group+'-write','mutate',dict(key='exact-'+group)))['choices'][0]['message']
            native.update(content='Exact visible text '+group,reasoning_content='Private auxiliary thinking '*100,
                reasoning='Other auxiliary provider thinking')
            rows.extend([native,
                dict(role='tool',tool_call_id=group+'-read',content=json.dumps(dict(receipt=dict(phase='completed',spellId=9474)))),
                dict(role='tool',tool_call_id=group+'-write',content=json.dumps(dict(commandWritten=True,
                    operationId='exact-'+group,replayAllowed=False)))])
        original=copy.deepcopy(rows)
        active=client._active_conversation(rows,{'observe'},{},None,protect_local=True)
        self.assertEqual(active,rows)
        payload,audit=client._local_prompt('Exact SYSTEM',active,ActiveContextTests.tools('observe','mutate'),
            {'observe'},{},None,canonical=rows,aggressive=True)
        expected=copy.deepcopy(rows)
        for message in expected:
            if message['role']=='assistant':
                message.pop('reasoning_content');message.pop('reasoning')
        self.assertEqual(payload['messages'][1:],expected)
        self.assertEqual(rows,original)
        self.assertEqual(audit['archivedReadResults'],[])
        self.assertEqual(len(audit['archivedAssistantReasoning']),6)
        for reference in audit['archivedAssistantReasoning']:
            raw=rows[reference['transcriptMessage']][reference['field']].encode()
            self.assertEqual((reference['bytes'],reference['sha256']),(len(raw),hashlib.sha256(raw).hexdigest()))
        script="import {sdkInput} from './dev/local_prompt_meter.mjs';import fs from 'node:fs';sdkInput(JSON.parse(fs.readFileSync(0,'utf8')));console.log('compatible');"
        checked=subprocess.run(['node','--input-type=module','-e',script],input=json.dumps(payload).encode(),
            cwd=Path(__file__).resolve().parent.parent,capture_output=True,timeout=5)
        self.assertEqual(checked.returncode,0,checked.stderr.decode())
        self.assertEqual(checked.stdout.strip(),b'compatible')


if __name__=="__main__":unittest.main()
