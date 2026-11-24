"""Pure town destination decisions from the connected body's accepted Lua."""
import hashlib
import importlib.machinery
import importlib.util
import math
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parent.parent
_loader = importlib.machinery.SourceFileLoader('town_policy_runtime', str(ROOT/'dev/task-runtime'))
_spec = importlib.util.spec_from_loader(_loader.name, _loader)
runtime = importlib.util.module_from_spec(_spec)
_loader.exec_module(runtime)
Error = runtime.Error
NATIVE_CLASS_PURCHASE_CAP=1000000  # Existing buddy-training.Buy mechanical limit.


def rank_vendors(rpc, state, candidates, scope, *, bridge=None):
    return rank_services(rpc,state,candidates,scope,'vendor',bridge=bridge)


def rank_trainers(rpc, state, self_guid, candidates, scope='nearby', *, bridge=None):
    return rank_services(rpc,dict(state,selfGuid=str(self_guid)),candidates,scope,'trainer',bridge=bridge)


def class_training(rpc, state, self_guid, spells, eligibility, attempted, reserve_copper=None, *, bridge=None):
    """One accepted Lua service choice, bound to the current native offer."""
    facts=dict(kind='class_training',copperKnown=state.get('copperKnown') is True,
        copper=state.get('copper',0),nativePurchaseCap=NATIVE_CLASS_PURCHASE_CAP,reserveCopper=reserve_copper,
        spells=spells,eligibility=eligibility,attempted={str(value):True for value in attempted})
    return _training_plan(rpc,state,self_guid,facts,NATIVE_CLASS_PURCHASE_CAP,bridge)


def profession_training(rpc,state,self_guid,own,primary_goals,secondary_goals,attempted,reserved_copper,goal_budget,*,budget_only=False,bridge=None):
    facts=dict(kind='profession_training',copperKnown=state.get('copperKnown') is True,copper=state.get('copper',0),
        nativePurchaseCap=10000,spells=[] if budget_only else state.get('offer',{}).get('spells',[]),eligibility={},
        attempted={str(value):True for value in attempted},primaryGoals=list(primary_goals),secondaryGoals=list(secondary_goals),
        levelKnown=own.get('levelKnown') is True,level=own.get('level',0),reservedCopper=reserved_copper,goalBudgetCopper=goal_budget,
        professions=dict(known=state.get('professionsKnown') is True,primary=state.get('primaryProfessions',[]),
            secondary=state.get('secondaryProfessions',[]),freePrimarySlotsKnown=state.get('freePrimarySlotsKnown') is True,
            freePrimarySlots=state.get('freePrimarySlots',0)))
    return _training_plan(rpc,state,self_guid,facts,10000,bridge)


def _training_plan(rpc,state,self_guid,facts,native_cap,bridge):
    spells=facts['spells'];consumer=facts['kind']
    identity=dict(sessionId=state['sessionId'],characterGuid=str(self_guid))
    view=rpc.call('session.policy_source',identity)
    source,revision,generation=view.get('source'),view.get('revision'),str(view.get('generation',''))
    limit=view.get('sourceLimitBytes',runtime.MAX_SOURCE)
    if (view.get('sessionId')!=identity['sessionId'] or str(view.get('characterGuid'))!=identity['characterGuid']
            or not isinstance(source,str) or not source or not re.fullmatch('[a-f0-9]{64}',str(revision))
            or hashlib.sha256(source.encode()).hexdigest()!=revision or not re.fullmatch('[1-9][0-9]{0,19}',generation)
            or int(generation)>=2**64
            or type(limit) is not int or limit<=0 or len(source.encode())>limit):
        raise Error('current_class_training_source_required')
    wrapped=('local policy=(function()\n'+source+'\nend)()\nreturn {decide=function(input) '
        'if type(policy.service_destination)~="function" then return {available=false} end '
        'return {available=true,decision=policy.service_destination(input)} end}\n')
    evaluator=bridge or runtime.Bridge(ROOT/'data/bin/luapolicy',ROOT/'modules',
        source_bytes=max(limit,len(wrapped.encode())),value_bytes=8*1024*1024,execution_ms=1000)
    result=evaluator.evaluate(wrapped,facts)
    class_training_current(rpc,state,self_guid,dict(policyRevision=revision,policyGeneration=generation),
        offer=bool(spells),profession=consumer=='profession_training')
    decision=result.get('decision') if isinstance(result,dict) and result.get('available') is True else None
    if (not isinstance(decision,dict) or decision.get('consumer')!=consumer
            or any(type(decision.get(key)) is not int or not 0<=decision[key]<=native_cap
                   for key in ('budgetCopper','purchaseCapCopper'))
            or type(decision.get('reserveCopper')) is not int or not 0<=decision['reserveCopper']<=NATIVE_CLASS_PURCHASE_CAP
            or decision['budgetCopper']>decision['purchaseCapCopper']
            or decision['budgetCopper']+decision['reserveCopper']>state.get('copper',0) and decision['budgetCopper']>0):
        raise Error('accepted_'+consumer+'_mode_required')
    if consumer=='profession_training' and (decision['budgetCopper']>facts['goalBudgetCopper']-facts['reservedCopper']
            or decision['purchaseCapCopper']>facts['goalBudgetCopper']-facts['reservedCopper']):
        raise Error('profession_training_goal_budget_exceeded')
    if 'nextCost' in decision and (type(decision['nextCost']) is not int
            or not decision['budgetCopper']<decision['nextCost']<=decision['purchaseCapCopper']
            or not any(row.get('costCopper',0)==decision['nextCost'] for row in spells)):
        raise Error('class_training_next_cost_must_be_observed')
    if 'spellId' in decision:
        matches=[row for row in spells if row.get('spellId')==decision['spellId']]
        if (len(matches)!=1 or type(decision['spellId']) is not int or not 0<decision['spellId']<2**32
                or type(decision.get('costCopper')) is not int or decision['costCopper']!=matches[0].get('costCopper',0)
                or not 0<=decision['costCopper']<=decision['budgetCopper']):
            raise Error('class_training_must_select_exact_offered_spell')
        if consumer=='profession_training' and (decision.get('skillId')!=matches[0].get('professionSkillId')
                or decision.get('learnedSpellId')!=matches[0].get('learnedSpellId')
                or decision['skillId'] not in facts['primaryGoals']+facts['secondaryGoals']):
            raise Error('profession_training_must_match_bound_goal_and_offer')
    return dict(decision,policyRevision=revision,policyGeneration=generation)


def class_training_current(rpc,state,self_guid,plan,*,offer=True,profession=False):
    observed=rpc.call('buddy-training.state',{})
    expected_offer,observed_offer=state.get('offer',{}),observed.get('offer',{})
    current=rpc.call('session.attach',dict(sessionId=state['sessionId'],characterGuid=str(self_guid)))
    policy=current.get('policy',{})
    if (current.get('sessionId')!=state['sessionId'] or str(current.get('characterGuid'))!=str(self_guid)
            or policy.get('revision')!=plan['policyRevision'] or str(policy.get('generation'))!=plan['policyGeneration']
            or policy.get('faulted',False) is not False or observed.get('sessionId')!=state['sessionId']
            or observed.get('revision')!=state.get('revision') or observed.get('copperKnown')!=state.get('copperKnown')
            or observed.get('copper',0)!=state.get('copper',0)
            or profession and any(observed.get(key)!=state.get(key) for key in ('professionsKnown','professionsRevision',
                'primaryProfessions','secondaryProfessions','freePrimarySlotsKnown','freePrimarySlots'))
            or offer and (any(observed_offer.get(key)!=expected_offer.get(key)
                             for key in ('revision','npcGuid','trainerType','spells'))
                or int(observed_offer.get('expiresInMs',0))<=0)):
        raise Error('class_training_body_source_or_offer_changed')


def rank_services(rpc, state, candidates, scope, kind, *, bridge=None):
    identity = dict(sessionId=state['sessionId'], characterGuid=str(state['selfGuid']))
    distances, facts = {}, []
    for candidate in candidates:
        distance, guid = candidate[:2]
        if (type(guid) is not int or not 0 < guid < 2**64 or guid in distances
                or type(distance) not in (int, float) or not math.isfinite(distance) or distance < 0):
            raise Error('observed_'+kind+'_candidates_required')
        distances[guid] = distance
        fact=dict(guid=str(guid),distance=distance)
        if kind=='trainer':
            if len(candidate)!=3 or type(candidate[2]) is not bool:raise Error('observed_trainer_interaction_required')
            fact['interactable']=candidate[2]
        facts.append(fact)
    if (kind not in ('vendor','trainer') or len(distances)>(97 if kind=='trainer' else 96)
            or scope not in (('nearby','interactable','travel') if kind=='trainer' else ('nearby','interactable'))):
        raise Error('bounded_'+kind+'_candidates_required')
    view = rpc.call('session.policy_source', identity)
    source, revision, generation = view.get('source'), view.get('revision'), str(view.get('generation', ''))
    limit = view.get('sourceLimitBytes', runtime.MAX_SOURCE)
    if (view.get('sessionId')!=identity['sessionId'] or str(view.get('characterGuid'))!=identity['characterGuid']
            or not isinstance(source,str) or not source or not re.fullmatch('[a-f0-9]{64}',str(revision))
            or hashlib.sha256(source.encode()).hexdigest()!=revision or not re.fullmatch('[1-9][0-9]*',generation)
            or type(limit) is not int or limit<=0 or len(source.encode())>limit):
        raise Error('current_town_policy_source_required')
    wrapped=('local policy=(function()\n'+source+'\nend)()\nreturn {decide=function(input) '
        'if type(policy.service_destination)~="function" then return {available=false} end '
        'return {available=true,decision=policy.service_destination(input)} end}\n')
    bridge = bridge or runtime.Bridge(ROOT/'data/bin/luapolicy', ROOT/'modules',
        source_bytes=max(limit,len(wrapped.encode())), value_bytes=8*1024*1024, execution_ms=1000)
    result = bridge.evaluate(wrapped, dict(kind=kind,scope=scope,candidates=facts))
    current = rpc.call('session.attach', identity)
    policy = current.get('policy', {})
    observation = rpc.call('buddy-inventory.state' if kind=='vendor' else 'buddy-training.state', {})
    revision_field='inventoryRevision' if kind=='vendor' else 'revision'
    if (current.get('sessionId')!=identity['sessionId'] or str(current.get('characterGuid'))!=identity['characterGuid']
            or policy.get('revision')!=revision or str(policy.get('generation'))!=generation
            or observation.get('sessionId')!=identity['sessionId']
            or kind=='vendor' and str(observation.get('selfGuid'))!=identity['characterGuid']
            or str(observation.get(revision_field))!=str(state.get(revision_field))):
        raise Error('town_policy_body_or_observation_changed')
    if not isinstance(result,dict) or result.get('available') is not True:
        raise Error('town_service_destination_export_unavailable')
    decision = result.get('decision')
    vendors = decision.get(kind+'s') if isinstance(decision,dict) else None
    if vendors == {}: vendors = []  # The Lua bridge represents an empty table as an object.
    if (not isinstance(vendors,list) or len(vendors)>len(distances)
            or any(not isinstance(guid,str) or guid not in {str(g) for g in distances} for guid in vendors)
            or len(set(vendors))!=len(vendors)):
        raise Error('lua_must_select_current_observed_'+kind+'s')
    return [(distances[int(guid)],int(guid)) for guid in vendors]
