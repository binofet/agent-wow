"""Direct provider tests use only a local fake HTTP server and typed callbacks."""
import copy
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import sys
import threading
import tempfile
import time
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
from direct_model import Cancelled, DirectModel, ModelError, ToolOutcomeUnknown


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


if __name__=="__main__":unittest.main()
