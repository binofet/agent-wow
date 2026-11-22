"""Run the connected mage's accepted Lua supply policy through installed RPCs.

These are the existing worker's configured client identities, not recipient,
rank, stock, quantity, timing or trade-completion choices.
"""
import copy
import hashlib
import importlib.machinery
import importlib.util
import re
import time
from pathlib import Path

ROOT=Path(__file__).resolve().parent.parent
_loader=importlib.machinery.SourceFileLoader('party_mana_runtime',str(ROOT/'dev/task-runtime'))
_spec=importlib.util.spec_from_loader(_loader.name,_loader)
runtime=importlib.util.module_from_spec(_spec)
_loader.exec_module(runtime)
MIRA=8087
MIRA_GUID=4
OWNER_GUID=3
RECIPIENTS={8086:1,8402:8}
VALUE_BYTES=8*1024*1024


def _identity(view,guid):
    actions=view.get('actions',{})
    sid=actions.get('sessionId')
    if (not isinstance(sid,str) or not re.fullmatch(r'[A-Za-z0-9._-]{1,128}',sid)
            or str(actions.get('selfGuid'))!=str(guid)):
        raise runtime.Error('current_supply_body_required')
    return dict(sessionId=sid,characterGuid=str(guid))


def _catalog(rpc,port,identity):
    catalog=rpc(port,'session.describe',{})
    if (catalog.get('sessionId')!=identity['sessionId']
            or str(catalog.get('characterGuid'))!=identity['characterGuid']
            or not isinstance(catalog.get('methods'),list)
            or not isinstance(catalog.get('messages'),dict)):
        raise runtime.Error('current_installed_supply_catalog_required')
    return catalog


def _metadata(rpc,mage,observations):
    toolkit=mage.get('spell_toolkit')
    if toolkit is None:toolkit=rpc(MIRA,'buddy-actions.spell_toolkit',{})
    if (toolkit.get('sessionId')!=mage['actions']['sessionId']
            or str(toolkit.get('selfGuid'))!=str(MIRA_GUID)):
        raise runtime.Error('current_supply_spellbook_required')
    # Collect every projected creation entry. Lua owns family/rank/eligibility.
    entries={int(row['createdItemEntry']) for row in toolkit.get('spells',[])
        if isinstance(row,dict) and row.get('createdItemEntry')}
    for view in observations.values():
        entries.update(int(row['entry']) for bag in view.get('inventory',{}).get('inventoryBags',[])
            for row in bag.get('slots',[]) if isinstance(row,dict) and row.get('itemGuid') and row.get('entry'))
    entries=sorted(entries)
    rows={}
    for start in range(0,len(entries),6): # The existing lookup RPC admits six IDs.
        reply=rpc(MIRA,'buddy-trade.lookup_items',{'itemIds':entries[start:start+6]})
        if reply.get('sessionId')!=mage['actions']['sessionId']:
            raise runtime.Error('supply_template_body_changed')
        for row in reply.get('items',[]):
            if isinstance(row,dict) and row.get('itemId'):
                rows[str(int(row['itemId']))]=copy.deepcopy(row)
    return toolkit,rows


def _source(rpc,identity):
    view=rpc(MIRA,'session.policy_source',identity)
    source,revision,generation=view.get('source'),view.get('revision'),str(view.get('generation',''))
    limit=view.get('sourceLimitBytes',runtime.MAX_SOURCE)
    if (view.get('sessionId')!=identity['sessionId'] or str(view.get('characterGuid'))!=identity['characterGuid']
            or not isinstance(source,str) or not source or not re.fullmatch('[a-f0-9]{64}',str(revision))
            or hashlib.sha256(source.encode()).hexdigest()!=revision or not re.fullmatch('[1-9][0-9]*',generation)
            or type(limit) is not int or limit<=0 or len(source.encode())>limit):
        raise runtime.Error('current_accepted_supply_source_required')
    return source,dict(identity,revision=revision,generation=generation),limit


def _current_source(rpc,binding):
    identity={key:binding[key] for key in ('sessionId','characterGuid')}
    view=rpc(MIRA,'session.attach',identity)
    policy=view.get('policy',{})
    if (view.get('sessionId')!=binding['sessionId'] or str(view.get('characterGuid'))!=binding['characterGuid']
            or policy.get('revision')!=binding['revision'] or str(policy.get('generation'))!=binding['generation']):
        raise runtime.Error('supply_policy_changed_before_dispatch')


def _validate_action(action,catalog,identity):
    if (not isinstance(action,dict) or not isinstance(action.get('id'),str) or not action['id']
            or not isinstance(action.get('method'),str) or not isinstance(action.get('params'),dict)):
        raise runtime.Error('typed_supply_action_required')
    rows=[row for row in catalog['methods'] if row.get('name')==action['method']]
    if len(rows)!=1 or rows[0].get('effect') not in ('mutation','unspecified'):
        raise runtime.Error('supply_method_not_advertised')
    schema=catalog['messages'].get(rows[0].get('inputType'),{})
    fields={row['name']:row for row in schema.get('fields',[]) if isinstance(row,dict) and isinstance(row.get('name'),str)}
    params=action['params']
    if not fields or set(params)-set(fields) or params.get('sessionId')!=identity['sessionId']:
        raise runtime.Error('installed_supply_request_contract_required')
    if 'selfGuid' in params and str(params['selfGuid'])!=identity['characterGuid']:
        raise runtime.Error('supply_action_body_changed')
    for key,value in params.items():
        kind=fields[key].get('kind')
        if fields[key].get('repeated') or fields[key].get('map'):
            raise runtime.Error('unsupported_supply_request_shape')
        if kind=='string':valid=isinstance(value,str)
        elif kind in ('uint64','fixed64'):
            valid=isinstance(value,str) and re.fullmatch(r'0|[1-9][0-9]{0,19}',value) and int(value)<2**64
        elif kind in ('uint32','fixed32'):
            valid=type(value) in (int,float) and int(value)==value and 0<=value<2**32
        elif kind=='bool':valid=type(value) is bool
        else:valid=False
        if not valid:raise runtime.Error('installed_supply_field_type_required:'+key)


def _evaluate(rpc,observations,state,bridge,now_ms,query=None):
    mage=observations[MIRA];identity=_identity(mage,MIRA_GUID)
    source,binding,limit=_source(rpc,identity)
    views=copy.deepcopy({str(port):view for port,view in observations.items()})
    identities={MIRA:identity}
    for port,guid in RECIPIENTS.items():
        if port in observations:identities[port]=_identity(observations[port],guid)
    catalogs={str(port):_catalog(rpc,port,body) for port,body in identities.items()}
    toolkit,metadata=_metadata(rpc,mage,observations);views[str(MIRA)]['spell_toolkit']=toolkit
    wrapped=('local policy=(function()\n'+source+'\nend)()\nreturn {decide=function(input) '
        'if type(policy.mana_supply_plan)~="function" then return {available=false} end '
        'return {available=true,decision=policy.mana_supply_plan(input)} end}\n')
    evaluator=bridge or runtime.Bridge(ROOT/'data/bin/luapolicy',ROOT/'modules',
        source_bytes=max(limit,len(wrapped.encode())),value_bytes=VALUE_BYTES,execution_ms=1000)
    result=evaluator.evaluate(wrapped,dict(observations=views,metadata=metadata,
        catalogs={port:value['methods'] for port,value in catalogs.items()},
        catalogMessages={port:value['messages'] for port,value in catalogs.items()},
        bindings=dict(magePort=str(MIRA),mageGuid=str(MIRA_GUID),ownerGuid=str(OWNER_GUID),
            recipients=[dict(port=str(port),guid=str(guid)) for port,guid in RECIPIENTS.items()]),
        memory=copy.deepcopy(state.get('memory',{})),last=copy.deepcopy(state.get('last')),
        nowMs=time.monotonic()*1000 if now_ms is None else now_ms,query=query,request=copy.deepcopy(state.get('request'))))
    _current_source(rpc,binding)
    if not isinstance(result,dict) or result.get('available') is not True:
        raise runtime.Error('mana_supply_policy_export_unavailable')
    return result.get('decision'),binding,identities


def supply_space_pressure(rpc,observations,*,bridge=None):
    """Read the same Lua stock/capacity decision for the existing bag worker."""
    if MIRA not in observations:return {}
    try:
        decision,_,identities=_evaluate(rpc,observations,{},bridge,None,'space_pressure')
        pressure=decision.get('bagPressure') if isinstance(decision,dict) else None
        if (not isinstance(pressure,dict) or set(pressure)-{str(port) for port in identities}
                or any(type(value) is not bool for value in pressure.values())):
            raise runtime.Error('bounded_supply_space_decision_required')
        return pressure
    except (runtime.Error,OSError,ValueError,TypeError,KeyError):
        return {} # Unavailable policy never authorizes water-driven destruction.


def maintain_supply(rpc,observations,state,*,bridge=None,now_ms=None):
    """One pure Lua turn and at most one selected mutation; no Python selector."""
    state.pop('spacePort',None)
    try:
        if MIRA not in observations:
            state['phase']='waiting_for_current_mage';return state['phase']
        decision,binding,identities=_evaluate(rpc,observations,state,bridge,now_ms)
        if (not isinstance(decision,dict) or not isinstance(decision.get('memory'),dict)
                or not isinstance(decision.get('phase'),str) or not re.fullmatch('[A-Za-z0-9_]{1,128}',decision['phase'])):
            raise runtime.Error('bounded_supply_decision_required')
        if decision.get('spacePort') is not None and decision['spacePort'] not in {str(port) for port in identities}:
            raise runtime.Error('supply_space_observed_client_required')
        action=decision.get('action')
        if action is not None:
            port=int(action.get('port',0))
            if port not in identities:raise runtime.Error('supply_action_observed_client_required')
            current=rpc(port,'buddy-actions.status',{})
            if current.get('sessionId')!=identities[port]['sessionId'] or str(current.get('selfGuid'))!=identities[port]['characterGuid']:
                raise runtime.Error('supply_body_changed_before_dispatch')
            # Read the INSTALLED schema again at the actual mutation boundary.
            catalog=_catalog(rpc,port,identities[port]);_validate_action(action,catalog,identities[port])
            _current_source(rpc,binding)
        state['memory']=decision['memory'] # Commit authored reservation before write.
        state['phase']=decision['phase'];state.pop('error',None)
        state.pop('spacePort',None)
        if decision.get('spacePort') is not None:state['spacePort']=decision['spacePort']
        if action is not None:
            state['last']=dict(id=action['id'],outcome='transport_unconfirmed')
            reply=rpc(int(action['port']),action['method'],action['params'])
            state['last']=dict(id=action['id'],outcome='rpc_result',result=reply)
        return state['phase']
    except Exception as error:
        # Admission/schema failures above commit no proposal and send nothing.
        # A failed write keeps the Lua-issued private uncertainty, never replay.
        state['error']=str(error);state['phase']='water_supply_rpc_unconfirmed' if (state.get('last') or {}).get('outcome')=='transport_unconfirmed' else 'water_supply_unavailable'
        return state['phase']
