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

function meterError(code, phase) {
  const error = Error(code);error.meterCode=code;error.meterPhase=phase;return error;
}

export async function countPayload(base, payload, {WebSocketClass=globalThis.WebSocket, timeoutMs=10000,onPhase=()=>{}}={}) {
  const started=performance.now();let phase='input';
  const mark=value=>{phase=value;onPhase(value);};
  let url;
  try {url=new URL(base);} catch {throw meterError('configuration_invalid',phase);}
  if (url.protocol !== 'ws:' || !['127.0.0.1','localhost','[::1]'].includes(url.hostname) ||
      !/:\d+$/.test(base) || url.port==='0' || url.username || url.password || url.search || url.hash || url.pathname !== '/' || base.endsWith('/') ||
      !Number.isSafeInteger(timeoutMs) || timeoutMs<=0 || timeoutMs>30000)
    throw meterError('configuration_invalid',phase);
  let input;try {input=sdkInput(payload);}catch {throw meterError('input_invalid',phase);}
  if(typeof WebSocketClass!=='function')throw meterError('runtime_unsupported',phase);
  mark('connect');
  let socket;try {socket=new WebSocketClass(`${base}/llm`);}catch {throw meterError('sdk_connection_failed',phase);}
  let next=0, authOK=false, failure=null, authResolve, authReject;
  const pending=new Map();
  const auth=new Promise((resolve,reject)=>{authResolve=resolve;authReject=reject;});
  const fail=code=>{
    if(failure)return;
    failure=meterError(code,phase);authReject(failure);
    for(const p of pending.values())p.reject(failure);
    pending.clear();try{socket.close();}catch{}
  };
  const timer=setTimeout(()=>fail('sdk_timeout'),timeoutMs);
  socket.addEventListener('open',()=>{
    try {socket.send(JSON.stringify({authVersion:1,
      clientIdentifier:`guest:${crypto.randomBytes(18).toString('base64')}`,clientPasskey:crypto.randomBytes(18).toString('base64')}));}
    catch {fail('sdk_connection_failed');}
  });
  socket.addEventListener('error',()=>fail('sdk_connection_failed'));
  socket.addEventListener('close',()=>{if(pending.size || !authOK)fail('sdk_connection_failed');});
  socket.addEventListener('message',event=>{
    let value;try{value=JSON.parse(String(event.data));}catch{fail('sdk_protocol_invalid');return;}
    if(!value || typeof value!=='object' || Array.isArray(value)){fail('sdk_protocol_invalid');return;}
    if(!authOK){if(value.success===true){authOK=true;authResolve();}else fail('sdk_connection_failed');return;}
    const call=pending.get(value.callId);if(!call)return;
    pending.delete(value.callId);
    if(value.type==='rpcResult')call.resolve(value.result);else call.reject(meterError('sdk_rpc_failed',phase));
  });
  const rpc=(endpoint,parameter)=>new Promise((resolve,reject)=>{
    if(failure){reject(failure);return;}
    const callId=next++;pending.set(callId,{resolve,reject});
    socket.send(JSON.stringify({type:'rpcCall',endpoint,callId,parameter}));
  });
  try {
    await auth;
    const query={type:'query',query:{identifier:payload.model,deviceIdentifier:null}};
    mark('model_lookup');
    const before=await rpc('getModelInfo',{specifier:query,throwIfNotFound:false});
    if(!before || before.identifier!==payload.model || before.deviceIdentifier!==null || typeof before.instanceReference!=='string')
      throw meterError('loaded_model_unavailable',phase);
    const specifier={type:'instanceReference',instanceReference:before.instanceReference};
    mark('render');
    const rendered=await rpc('applyPromptTemplate',{specifier,history:input.history,
      predictionConfigStack:{layers:[]},opts:{toolDefinitions:input.tools}});
    if(typeof rendered?.formatted!=='string')throw meterError('sdk_render_invalid',phase);
    mark('count');
    const count=await rpc('countTokens',{specifier,inputString:rendered.formatted});
    mark('model_recheck');
    const after=await rpc('getModelInfo',{specifier:query,throwIfNotFound:false});
    if(!after || after.identifier!==before.identifier || after.instanceReference!==before.instanceReference || after.deviceIdentifier!==null)
      throw meterError('loaded_model_changed',phase);
    if(!Number.isSafeInteger(count?.tokenCount) || count.tokenCount<0)throw meterError('sdk_count_invalid','count');
    return {tokens:count.tokenCount,model:before.identifier,instanceReference:before.instanceReference,
      scope:'sdk_tool_aware_template',renderedBytes:Buffer.byteLength(rendered.formatted),
      renderedSHA256:crypto.createHash('sha256').update(rendered.formatted).digest('hex'),
      sdkElapsedMs:Math.round(performance.now()-started),sdkTimeoutMs:timeoutMs};
  } catch(error) {
    if(error?.meterCode)throw error;
    throw meterError('sdk_failed',phase);
  } finally {clearTimeout(timer);try{socket.close();}catch{}}
}

if(process.argv[1] && import.meta.url===pathToFileURL(process.argv[1]).href){
  let phase='input';
  const onPhase=value=>{phase=value;console.error(JSON.stringify({meterPhase:value}));};
  onPhase(phase);
  try {
    const raw=fs.readFileSync(0);if(raw.length>8*1024*1024)throw meterError('input_invalid',phase);
    console.log(JSON.stringify(await countPayload(process.argv[2],JSON.parse(raw),
      {timeoutMs:process.argv[3]===undefined?10000:Number(process.argv[3]),onPhase})));
  } catch(error) {
    console.log(JSON.stringify({error:{code:error?.meterCode??(phase==='input'?'input_invalid':'sdk_failed'),
      phase:error?.meterPhase??phase}}));process.exitCode=1;
  }
}
