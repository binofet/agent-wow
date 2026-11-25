// LM Studio public SDK wire: getModelInfo, applyPromptTemplate, countTokens only.
// Exact locally loaded identifier; never model(), load, predict, download or fallback.
import fs from 'node:fs';
import crypto from 'node:crypto';
import {pathToFileURL} from 'node:url';

export function sdkInput(payload) {
  if (!payload || typeof payload.model !== 'string' || !Array.isArray(payload.messages) || !Array.isArray(payload.tools))
    throw Error('unsupported_prompt_shape');
  const names = new Map();
  const messages = payload.messages.map(message => {
    if (!['system','user','assistant','tool'].includes(message.role)) throw Error('unsupported_message_role');
    if (Object.entries(message).some(([key,value])=>!['role','content','tool_calls','tool_call_id'].includes(key) && value!=null && value!==''))
      throw Error('unsupported_message_metadata');
    const content = [];
    if (message.role === 'tool') {
      if (typeof message.content !== 'string' || !names.has(message.tool_call_id)) throw Error('unpaired_tool_result');
      content.push({type:'toolCallResult',content:message.content,toolCallId:message.tool_call_id,name:names.get(message.tool_call_id)});
    } else {
      if (message.content != null) {
        if (typeof message.content !== 'string') throw Error('unsupported_multimodal_prompt');
        content.push({type:'text',text:message.content});
      }
      for (const call of message.tool_calls ?? []) {
        if (message.role !== 'assistant' || call.type !== 'function' || typeof call.id !== 'string') throw Error('invalid_tool_call');
        const args = JSON.parse(call.function.arguments);
        if (!args || Array.isArray(args) || typeof args !== 'object') throw Error('invalid_tool_arguments');
        names.set(call.id,call.function.name);
        content.push({type:'toolCallRequest',toolCallRequest:{type:'function',id:call.id,name:call.function.name,arguments:args}});
      }
    }
    return {role:message.role,content};
  });
  return {history:{messages},tools:payload.tools};
}

export async function countPayload(base, payload, {WebSocketClass=globalThis.WebSocket, timeoutMs=4000}={}) {
  const url = new URL(base);
  if (url.protocol !== 'ws:' || !['127.0.0.1','localhost','[::1]'].includes(url.hostname) ||
      !/:\d+$/.test(base) || url.username || url.password || url.search || url.hash || url.pathname !== '/' || base.endsWith('/') ||
      typeof WebSocketClass !== 'function') throw Error('explicit_loopback_sdk_and_node22_required');
  const input = sdkInput(payload);
  const socket = new WebSocketClass(`${base}/llm`);
  let next=0, authOK=false, failed=false, authResolve, authReject;
  const pending=new Map();
  const auth=new Promise((resolve,reject)=>{authResolve=resolve;authReject=reject;});
  const fail=()=>{failed=true;authReject(Error('sdk_unavailable'));for(const p of pending.values())p.reject(Error('sdk_unavailable'));pending.clear();socket.close();};
  const timer=setTimeout(fail,timeoutMs);
  socket.addEventListener('open',()=>socket.send(JSON.stringify({authVersion:1,
    clientIdentifier:`guest:${crypto.randomBytes(18).toString('base64')}`,clientPasskey:crypto.randomBytes(18).toString('base64')})));
  socket.addEventListener('error',fail);
  socket.addEventListener('close',()=>{if(pending.size || !authOK)fail();});
  socket.addEventListener('message',event=>{
    let value;try{value=JSON.parse(String(event.data));}catch{fail();return;}
    if(!authOK){if(value.success===true){authOK=true;authResolve();}else fail();return;}
    const call=pending.get(value.callId);if(!call)return;
    pending.delete(value.callId);
    if(value.type==='rpcResult')call.resolve(value.result);else call.reject(Error('sdk_rpc_refused'));
  });
  const rpc=(endpoint,parameter)=>new Promise((resolve,reject)=>{
    if(failed){reject(Error('sdk_unavailable'));return;}
    const callId=next++;pending.set(callId,{resolve,reject});
    socket.send(JSON.stringify({type:'rpcCall',endpoint,callId,parameter}));
  });
  try {
    await auth;
    const query={type:'query',query:{identifier:payload.model,deviceIdentifier:null}};
    const before=await rpc('getModelInfo',{specifier:query,throwIfNotFound:false});
    if(!before || before.identifier!==payload.model || before.deviceIdentifier!==null || typeof before.instanceReference!=='string')
      throw Error('exact_local_loaded_model_required');
    const specifier={type:'instanceReference',instanceReference:before.instanceReference};
    const rendered=await rpc('applyPromptTemplate',{specifier,history:input.history,
      predictionConfigStack:{layers:[]},opts:{toolDefinitions:input.tools}});
    if(typeof rendered?.formatted!=='string')throw Error('sdk_render_invalid');
    const count=await rpc('countTokens',{specifier,inputString:rendered.formatted});
    const after=await rpc('getModelInfo',{specifier:query,throwIfNotFound:false});
    if(!after || after.identifier!==before.identifier || after.instanceReference!==before.instanceReference || after.deviceIdentifier!==null)
      throw Error('loaded_model_changed');
    if(!Number.isSafeInteger(count?.tokenCount) || count.tokenCount<0)throw Error('sdk_count_invalid');
    return {tokens:count.tokenCount,model:before.identifier,instanceReference:before.instanceReference,
      scope:'sdk_tool_aware_template',renderedBytes:Buffer.byteLength(rendered.formatted),
      renderedSHA256:crypto.createHash('sha256').update(rendered.formatted).digest('hex')};
  } finally {clearTimeout(timer);socket.close();}
}

if(process.argv[1] && import.meta.url===pathToFileURL(process.argv[1]).href){
  try {
    const raw=fs.readFileSync(0);if(raw.length>8*1024*1024)throw Error('prompt_bound');
    console.log(JSON.stringify(await countPayload(process.argv[2],JSON.parse(raw))));
  } catch {console.error('local_prompt_meter_unavailable_or_unsupported');process.exitCode=1;}
}
