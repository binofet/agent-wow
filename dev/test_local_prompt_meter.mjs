import {test} from 'node:test';
import assert from 'node:assert/strict';
import {countPayload,sdkInput} from './local_prompt_meter.mjs';

const payload={model:'loaded-exact',messages:[{role:'system',content:'Original system'},
  {role:'user',content:'Original goal'},{role:'assistant',content:'Inspecting',tool_calls:[
    {id:'read-1',type:'function',function:{name:'inspect',arguments:'{"path":["self"]}'}}]},
  {role:'tool',tool_call_id:'read-1',content:'{"known":true}'}],
  tools:[{type:'function',function:{name:'inspect',description:'Exact tool description',
    parameters:{type:'object',properties:{path:{type:'array',items:{type:'string'}}},required:['path']}}}]};
function fake(mode='ok') {
  const sent=[];let info=0;
  class Socket {
    constructor(url){this.listeners={};assert.equal(url,'ws://127.0.0.1:1234/llm');queueMicrotask(()=>this.emit('open',{}));}
    addEventListener(name,callback){this.listeners[name]=callback;}
    emit(name,value){this.listeners[name]?.(value);}
    close(){}
    send(raw){
      const frame=JSON.parse(raw);sent.push(frame);
      if(frame.authVersion){queueMicrotask(()=>this.emit('message',{data:JSON.stringify({success:true})}));return;}
      if(mode==='timeout')return;
      let result;
      if(frame.endpoint==='getModelInfo'){
        info++;result={identifier:mode==='wrong'?'other':payload.model,deviceIdentifier:null,
          instanceReference:mode==='changed'&&info===2?'new':'immutable-instance'};
        assert.deepEqual(frame.parameter.specifier,{type:'query',query:{identifier:payload.model,deviceIdentifier:null}});
      } else if(frame.endpoint==='applyPromptTemplate'){
        assert.deepEqual(frame.parameter.opts,{toolDefinitions:payload.tools});
        assert.deepEqual(frame.parameter.history,sdkInput(payload).history);
        assert.deepEqual(frame.parameter.specifier,{type:'instanceReference',instanceReference:'immutable-instance'});
        result={formatted:'FULL SYSTEM + USER + TOOL DEFINITIONS + CALL/RESULT TEMPLATE'};
      } else if(frame.endpoint==='countTokens'){
        assert.equal(frame.parameter.inputString,'FULL SYSTEM + USER + TOOL DEFINITIONS + CALL/RESULT TEMPLATE');
        result={tokenCount:mode==='bad-count'?-1:237};
      } else throw Error('forbidden endpoint '+frame.endpoint);
      queueMicrotask(()=>this.emit('message',{data:JSON.stringify({type:'rpcResult',callId:frame.callId,result})}));
    }
  }
  return {Socket,sent};
}
test('full tool-aware SDK render/count uses immutable loaded instance only',async()=>{
  const {Socket,sent}=fake();const before=JSON.stringify(payload);
  const result=await countPayload('ws://127.0.0.1:1234',payload,{WebSocketClass:Socket});
  assert.equal(result.tokens,237);assert.equal(result.scope,'sdk_tool_aware_template');
  assert.equal(JSON.stringify(payload),before);
  assert.deepEqual(sent.filter(x=>x.endpoint).map(x=>x.endpoint),['getModelInfo','applyPromptTemplate','countTokens','getModelInfo']);
  const history=sdkInput(payload).history.messages;
  assert.equal(history[2].content[1].toolCallRequest.id,'read-1');
  assert.equal(history[3].content[0].toolCallId,'read-1');
});
test('no host fallback, loading, remote link, or changed-instance count',async()=>{
  for(const mode of ['wrong','changed','bad-count','timeout']){
    const {Socket}=fake(mode);
    await assert.rejects(countPayload('ws://127.0.0.1:1234',payload,{WebSocketClass:Socket,timeoutMs:5}));
  }
  for(const base of ['ws://example.com:1234','wss://127.0.0.1:1234','ws://user:secret@127.0.0.1:1234','ws://127.0.0.1:1234/path']){
    await assert.rejects(countPayload(base,payload,{WebSocketClass:class{constructor(){throw Error('must not connect');}}}));
  }
});
test('unsupported multimodal or opaque reasoning input fails rather than undercounting',()=>{
  for(const extra of [{content:[]},{reasoning_content:'opaque retained reasoning'}]){
    const p=structuredClone(payload);Object.assign(p.messages[2],extra);assert.throws(()=>sdkInput(p));
  }
});
