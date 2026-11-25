"""Read-only, body-bound gear checks using the character's accepted Lua policy.

inspect(rpc, session_id=..., character_guid=..., item_entry=..., item_guid=None)
returns upgrade/not_upgrade/unknown. It never equips, changes policy or ranks
items itself. check_inventory accepts already captured facts for code callers.
"""
import argparse
import copy
import hashlib
import importlib.machinery
import importlib.util
import json
import math
from pathlib import Path
import re

ROOT=Path(__file__).resolve().parents[1]

def _uint(value, maximum=2**64-1):
    if isinstance(value,str) and re.fullmatch(r'0|[1-9][0-9]*',value):
        value=int(value)
    if type(value) is not int or not 0<=value<=maximum:
        raise ValueError('unsigned_integer_required')
    return value

def _unknown(result, reason):
    result.update(status='unknown',reason=reason,missingFacts=[reason])
    return result

def _equipment(inventory):
    rows=inventory.get('equipment')
    if inventory.get('equipmentKnown') is not True or not _uint(inventory.get('equipmentRevision',0)) or not isinstance(rows,list) or len(rows)!=19:
        return None
    found={}
    guids=set()
    try:
        for row in rows:
            slot=_uint(row.get('slot',0),18)
            if row.get('slotKnown') is not True or slot in found:
                return None
            guid=_uint(row.get('itemGuid',0))
            entry=_uint(row.get('entry',0),2**32-1)
            if guid==0 and entry!=0 or guid!=0 and entry==0 or guid and guid in guids:
                return None
            if guid:
                guids.add(guid)
            found[slot]=row
    except (ValueError,AttributeError):
        return None
    return found

def _skills(world):
    if world.get('suppliedSheetSkillsComplete') is True:
        return {_uint(key,65535):_uint(value,65535) for key,value in world['suppliedSheetSkills'].items()}
    rows=world.get('selfSkills')
    if world.get('selfSkillsKnown') is not True or not _uint(world.get('selfSkillsRevision',0)) or not isinstance(rows,list) or len(rows)!=128:
        return None
    out={}
    seen=set()
    try:
        for row in rows:
            slot=_uint(row.get('slot',0),127)
            skill=_uint(row.get('skillId',0),65535)
            value=_uint(row.get('value',0),65535)
            maximum=_uint(row.get('maxValue',0),65535)
            if slot in seen or value>maximum or skill in out or skill==0 and (value or maximum):
                return None
            seen.add(slot)
            if skill:
                out[skill]=value
    except (ValueError,AttributeError):
        return None
    return out

def _preview(evaluate, frame):
    try:
        return evaluate(frame)
    except Exception:
        # Read-only policy failure is missing comparison evidence, not a vote.
        return dict(upgrade=False, reason='gear_policy_evaluation_failed')

def _occupied(row):
    return _uint(row.get('itemGuid',0))>0 or _uint(row.get('entry',0),2**32-1)>0

def _destinations(candidate, player, world, equipment, templates):
    """Mechanical supported slots/proficiency facts, matching native gear facts.

    No comparison/score/role weights live here. Unknown special requirements
    never become a positive eligibility assertion.
    """
    for known,key in [('classKnown','classId'),('raceKnown','raceId'),('levelKnown','level')]:
        if player.get(known) is not True or not 0<_uint(player.get(key,0))<= (11 if key!='level' else 2**32-1):
            return [],'actual_class_race_level_required',False
    for key in ('allowableClass','allowableRace','requiredLevel','requiredSkill','requiredSkillRank','requiredSpell','requiredHonorRank','requiredCityRank','requiredReputationFaction','requiredReputationRank','area','map','holiday'):
        if key not in candidate or candidate[key] is None:
            return [],'candidate_requirement_unknown:'+key,False
    if not (int(candidate['allowableClass'])&1<<(player['classId']-1)) or not (int(candidate['allowableRace'])&1<<(player['raceId']-1)):
        return [],'item_class_or_race_restricted',True
    if player['level']<candidate['requiredLevel']:
        return [],'item_required_level_not_met',True
    if any(candidate[key] for key in ('requiredSpell','requiredHonorRank','requiredCityRank','requiredReputationFaction','requiredReputationRank','area','map','holiday')):
        return [],'special_item_requirement_unverified',False
    kind,sub,inv=candidate.get('itemClass'),candidate.get('subclass'),candidate.get('inventoryType')
    # Pinned ItemTemplate::GetSkill / Player::FindEquipSlot, b8c2e189.
    armor={1:0,2:1,3:2,4:3,5:4,20:4,6:5,7:6,8:7,9:8,10:9,16:14,19:18}
    weapon_skills={0:44,1:172,2:45,3:46,4:54,5:160,6:229,7:43,8:55,10:136,13:473,15:173,16:176,18:226,19:228,20:356}
    proficiency=0
    slots=[]
    if kind==4 and inv in (11,12) and sub==0:
        if candidate.get('maxCount') is None:
            return [],'candidate_unique_count_unknown',False
        slots=[10,11] if inv==11 else [12,13]
        if candidate.get('flags',0)&0x80000 or candidate.get('maxCount',0)==1:
            if any(_occupied(equipment[x]) and equipment[x].get('metadataKnown') is not True for x in slots):
                return [],'unique_equipped_other_slot_metadata_unknown',False
            if any(_occupied(equipment[x]) and equipment[x].get('entry')==candidate['itemId'] for x in slots):
                return [],'unique_equipped_item_already_worn',True
    elif kind==4 and inv in armor:
        slots=[armor[inv]]
        proficiency={0:0,1:415,2:414,3:413,4:293}.get(sub)
        if proficiency is None:
            return [],'armor_proficiency_unresolved',False
    elif kind==4 and inv in (14,23):
        slots=[16]
        proficiency=433 if inv==14 and sub==6 else 0
        if inv==14 and sub!=6:
            return [],'shield_template_mismatch',False
        main=templates.get(str(equipment[15].get('entry',0)))
        if _occupied(equipment[15]):
            if (not isinstance(main,dict) or main.get('available') is not True
                    or type(main.get('inventoryType')) is not int
                    or main['inventoryType'] not in (13,17,21)):
                return [],'occupied_mainhand_type_unknown',False
            if main['inventoryType']==17:
                return [],'two_hand_mainhand_blocks_offhand',False
    elif kind==2 and sub in weapon_skills and inv in (13,15,17,21,22,25,26):
        proficiency=weapon_skills[sub]
        slots=[17] if inv in (15,25,26) else [16] if inv==22 else [15]
        if inv==17 and _occupied(equipment[16]):
            return [],'two_hand_requires_offhand_displacement_comparison',False
        ranks=_skills(world)
        main=equipment[15]
        old=templates.get(str(equipment[16].get('entry',0)),{})
        observed_offhand=equipment[16].get('metadataKnown') is True and old.get('available') is True and old.get('itemClass')==2 and old.get('inventoryType') in (13,22)
        dual=ranks is not None and ranks.get(118,0)>0 or observed_offhand
        main_template=templates.get(str(main.get('entry',0)),{})
        main_clear=not _occupied(main) or main.get('metadataKnown') is True and main_template.get('available') is True and main_template.get('inventoryType')!=17
        if inv==13 and dual and main_clear:
            slots=[15,16]
        if inv==22 and (not dual or not main_clear):
            return [],'observed_dual_wield_and_mainhand_context_required',False
    else:
        return [],'physical_equipment_destinations_not_supported',False
    if proficiency or candidate['requiredSkill']:
        ranks=_skills(world)
        if ranks is None:
            return [],'complete_own_skill_baseline_required',False
        if proficiency and ranks.get(proficiency,0)==0:
            return [],'actual_equipment_proficiency_not_met',True
        if candidate['requiredSkill'] and ranks.get(candidate['requiredSkill'],0)<max(1,candidate['requiredSkillRank']):
            return [],'item_required_skill_not_met',True
    return slots,'',True

def check_inventory(inventory, *, session_id, character_guid, item_entry, item_guid=None,
                    world=None, lookup=None, evaluate=None, policy_revision=None):
    """Pure query over captured facts; evaluate invokes only accepted Lua preview."""
    entry=_uint(item_entry,2**32-1)
    guid=str(_uint(character_guid))
    exact=None if item_guid is None else str(_uint(item_guid))
    if not entry or guid=='0' or exact=='0':
        raise ValueError('positive_character_item_identity_required')
    result=dict(status='unknown',sessionId=session_id,characterGuid=guid,itemEntry=entry,
        requestedItemGuid=exact,reason='',missingFacts=[],comparisons=[],gameEffectClaimed=False)
    if inventory.get('sessionId')!=session_id or str(inventory.get('selfGuid'))!=guid:
        return _unknown(result,'inventory_body_mismatch')
    context=inventory.get('gearContext',{})
    revision=context.get('policyRevision')
    if not isinstance(policy_revision,str) or not re.fullmatch('[a-f0-9]{64}',policy_revision) or revision!=policy_revision:
        return _unknown(result,'current_accepted_gear_policy_required')
    result.update(policyRevision=revision,inventoryRevision=inventory.get('inventoryRevision'),equipmentRevision=inventory.get('equipmentRevision'),policyBasis='current_accepted_actor_gear_evaluation; role weights unchanged, spec/talent weighting not inferred')
    equipment=_equipment(inventory)
    if equipment is None:
        return _unknown(result,'complete_own_equipment_baseline_required')
    owned=[row for row in inventory.get('items',[]) if isinstance(row,dict) and (row.get('item') or {}).get('entry')==entry and (exact is None or str(row['item'].get('itemGuid'))==exact)]
    if exact is not None and not owned:
        return _unknown(result,'exact_owned_item_not_observed')
    if owned:
        result['comparisonScope']='owned_instance'
        for row in owned:
            source=copy.deepcopy(row['item'])
            observed_source=None
            if inventory.get('inventoryKnown') is True:
                for bag in inventory.get('inventoryBags',[]):
                    if bag.get('slotKnown') is True and bag.get('contentsKnown') is True and bag.get('bag',0)==source.get('bag',0):
                        for item in bag.get('slots',[]):
                            if item.get('slot',0)==source.get('slot',0):
                                observed_source=item
            source_matches=isinstance(observed_source,dict) and all(str(observed_source.get(k,0))==str(source.get(k,0)) for k in ('itemGuid','entry','stackCount','bag','slot')) and _uint(source.get('itemGuid',0))>0
            native_destinations=(row.get('gearFacts') or {}).get('destinations',[])
            legal=[d for d in native_destinations if d.get('eligible') is True and _uint(d.get('equipmentSlot',0),18) in equipment]
            if not source_matches or row.get('metadataKnown') is not True:
                result['comparisons'].append(dict(status='unknown',candidate=source,reason='owned_source_observation_changed',eligibility='unknown'))
                continue
            if not legal:
                result['comparisons'].append(dict(status='unknown',candidate=source,reason=row.get('gearBlockedReason') or 'observed_legal_destinations_required',eligibility='unknown'))
                continue
            for dest in legal:
                slot=_uint(dest.get('equipmentSlot',0),18)
                worn=equipment[slot]
                if str(worn.get('itemGuid','0'))!=str(dest.get('expectedEquippedItemGuid','0')):
                    result['comparisons'].append(dict(status='unknown',candidate=source,equipmentSlot=slot,worn=copy.deepcopy(worn),reason='worn_destination_binding_changed',eligibility='unknown'))
                    continue
                if evaluate is None:
                    chosen=slot==row.get('gearEquipmentSlot',0) and row.get('gearEquipEligible') is True
                    decision=dict(upgrade=chosen and row.get('conservativeUpgrade') is True,reason=row.get('gearBlockedReason','') if chosen else 'accepted_gear_preview_required')
                else:
                    templates=context.get('templates',{})
                    decision=_preview(evaluate,dict(mode='preview',candidate=copy.deepcopy(templates.get(str(entry))),candidateFacts=copy.deepcopy(row.get('gearFacts',{})),current=copy.deepcopy(templates.get(str(worn.get('entry',0)))),currentFacts=copy.deepcopy(worn),context=dict(classId=context.get('classId'))))
                if not isinstance(decision,dict) or type(decision.get('upgrade')) is not bool or not isinstance(decision.get('reason'),str):
                    return _unknown(result,'accepted_gear_preview_result_invalid')
                reason=decision['reason']
                status='upgrade' if decision['upgrade'] else 'not_upgrade' if reason in ('','comparison_not_an_upgrade') else 'unknown'
                result['comparisons'].append(dict(status=status,candidate=source,equipmentSlot=slot,worn=copy.deepcopy(worn),reason=reason or ('accepted_lua_upgrade' if decision['upgrade'] else 'comparison_not_an_upgrade'),eligibility='eligible',scoreGain=decision.get('scoreGain')))
    else:
        player=(world or {}).get('self',{})
        if str(player.get('guid'))!=guid or player.get('present') is not True:
            return _unknown(result,'observed_recipient_body_required')
        return _prospective(result,context,equipment,player,world,entry,lookup,evaluate)
    return _finish(result)

def _prospective(result,context,equipment,player,world,entry,lookup,evaluate):
    result['comparisonScope']='unowned_base_template_not_actual_instance_or_ownership'
    result['candidateAssumption']='Hypothetical base template with no instance enhancements; not proof about an actual offered/randomized item.'
    if lookup is None or evaluate is None:
        return _unknown(result,'prospective_template_or_accepted_evaluator_unavailable')
    candidate=lookup(entry)
    if not isinstance(candidate,dict) or candidate.get('available') is not True or candidate.get('itemId')!=entry:
        return _unknown(result,'candidate_template_unavailable')
    result['templateProvenance']=copy.deepcopy(candidate.get('provenance'))
    templates=copy.deepcopy(context.get('templates',{}))
    for worn in equipment.values():
        old=worn.get('entry',0)
        if old and str(old) not in templates:
            template=lookup(old)
            if template is not None:
                templates[str(old)]=template
    slots,reason,known=_destinations(candidate,player,world,equipment,templates)
    if not slots:
        result.update(status='not_upgrade' if known else 'unknown',eligibility='ineligible' if known else 'unknown',eligibilityReason=reason,reason=reason,missingFacts=[] if known else [reason])
        return result
    for slot in slots:
        worn=equipment[slot]
        current=templates.get(str(worn.get('entry',0)))
        # This is an explicitly hypothetical unmodified BASE template,
        # never an observation about an unowned physical instance. Worn
        # instance facts and all actor options/weights remain unchanged.
        decision=_preview(evaluate,dict(mode='preview',candidate=copy.deepcopy(candidate),
            candidateFacts=dict(enhancementsKnown=True,hasEnhancements=False),
            current=copy.deepcopy(current),currentFacts=copy.deepcopy(worn),
            context=dict(classId=player['classId'])))
        if not isinstance(decision,dict) or type(decision.get('upgrade')) is not bool or not isinstance(decision.get('reason'),str):
            return _unknown(result,'accepted_gear_preview_result_invalid')
        reason=decision['reason']
        status='upgrade' if decision['upgrade'] else 'not_upgrade' if reason in ('','comparison_not_an_upgrade') else 'unknown'
        result['comparisons'].append(dict(status=status,eligibility='eligible',candidate=dict(entry=entry,owned=False),equipmentSlot=slot,worn=copy.deepcopy(worn),reason=reason or ('accepted_lua_upgrade' if decision['upgrade'] else 'comparison_not_an_upgrade'),scoreGain=decision.get('scoreGain')))
    return _finish(result)

def _finish(result):
    rows=result['comparisons']
    result['eligibility']='eligible' if any(r.get('eligibility')=='eligible' for r in rows) else 'unknown'
    status='upgrade' if any(r['status']=='upgrade' for r in rows) else 'unknown' if not rows or any(r['status']=='unknown' for r in rows) else 'not_upgrade'
    result.update(status=status,reason=next((r['reason'] for r in rows if r['status']==status),'comparison_unavailable'),missingFacts=sorted({r['reason'] for r in rows if r['status']=='unknown'}))
    return result

def compare_set(sheet, *, item_entry, lookup=None, evaluate=None, policy_source=None, bridge=None):
    """Compare a supplied hypothetical sheet, with NO connected-body claim.

    Explicit defaults: emptyUnlistedSlots=true proves omitted slots empty;
    assumeUnmodifiedWornBase=true describes base copies rather than instances;
    skillsComplete=true declares the supplied skill map complete. Otherwise
    missing material facts remain unknown. Default policy is public gear.lua,
    whose conservative stat-component comparison is not a spec/talent model.
    """
    entry=_uint(item_entry,2**32-1)
    if not entry or not isinstance(sheet,dict):
        raise ValueError('item_and_character_sheet_required')
    result=dict(status='unknown',itemEntry=entry,reason='',missingFacts=[],comparisons=[],gameEffectClaimed=False,
        characterBasis='supplied_hypothetical_sheet_not_connected_actor',policyBasis='public_default_conservative_not_spec_or_talent_weights' if policy_source is None else 'explicit_supplied_policy_not_actor_attestation')
    player=copy.deepcopy(sheet.get('character',{}))
    equipment={}
    for row in sheet.get('equipment',[]):
        slot=_uint(row.get('slot'),18)
        if slot in equipment:
            raise ValueError('duplicate_equipment_slot')
        equipment[slot]=copy.deepcopy(row)
    for slot in range(19):
        if slot not in equipment:
            if sheet.get('emptyUnlistedSlots') is not True:
                return _unknown(result,'explicit_equipment_slot_or_empty_default_required')
            equipment[slot]=dict(slot=slot,slotKnown=True,entry=0)
        row=equipment[slot]
        if row.get('slotKnown') is not True:
            return _unknown(result,'supplied_equipment_slot_unknown')
        row['entry']=_uint(row.get('entry',0),2**32-1)
        if sheet.get('assumeUnmodifiedWornBase') is True:
            row.setdefault('enhancementsKnown',True)
            row.setdefault('hasEnhancements',False)
            row.setdefault('durabilityKnown',True)
            row.setdefault('durability',row.get('maxDurability',0))
            row.setdefault('metadataKnown',True)
    world=dict(self=player)
    ranks=sheet.get('skills',{})
    if sheet.get('skillsComplete') is True and isinstance(ranks,dict):
        world.update(suppliedSheetSkillsComplete=True,suppliedSheetSkills=copy.deepcopy(ranks))
    if lookup is None:
        from gear_reference import lookup
    if policy_source is None:
        policy_source='local gear=(function()\n'+(ROOT/'behaviors/gear.lua').read_text()+'\nend)()\nreturn {gear_evaluation=gear.evaluate}'
    result['policySHA256']=hashlib.sha256(policy_source.encode()).hexdigest()
    result['sheetAssumptions']={key:sheet.get(key) is True for key in ('emptyUnlistedSlots','assumeUnmodifiedWornBase','skillsComplete')}
    if evaluate is None:
        runtime=_runtime()
        vm=bridge or runtime.Bridge(ROOT/'data/bin/luapolicy',ROOT/'modules',source_bytes=max(runtime.MAX_SOURCE,len(policy_source.encode())+1024),value_bytes=8*1024*1024,execution_ms=1000)
        wrapped='local p=(function()\n'+policy_source+'\nend)()\nreturn {decide=function(f)return p.gear_evaluation(f)end}'
        evaluate=lambda frame:vm.evaluate(wrapped,frame)
    return _prospective(result,dict(templates={}),equipment,player,world,entry,lookup,evaluate)


def _runtime():
    loader=importlib.machinery.SourceFileLoader('gear_check_runtime',str(ROOT/'dev/task-runtime'))
    spec=importlib.util.spec_from_loader(loader.name,loader)
    module=importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module

def inspect(rpc, *, session_id, character_guid, item_entry, item_guid=None, lookup=None, bridge=None):
    """Read only this bound endpoint; no caller-selected cross-character routing."""
    guid=str(_uint(character_guid))
    identity=dict(sessionId=session_id,characterGuid=guid)
    catalog=rpc.call('session.describe',{})
    if catalog.get('sessionId')!=session_id or str(catalog.get('characterGuid'))!=guid:
        raise ValueError('current_character_binding_required')
    source=rpc.call('session.policy_source',identity)
    code=source.get('source')
    revision=source.get('revision')
    if source.get('sessionId')!=session_id or str(source.get('characterGuid'))!=guid or not isinstance(code,str) or hashlib.sha256(code.encode()).hexdigest()!=revision:
        raise ValueError('current_accepted_policy_source_required')
    inventory=rpc.call('buddy-inventory.state',{})
    world=rpc.call('buddy-world.snapshot',{})
    if lookup is None:
        def lookup(entry):
            from gear_reference import lookup as reference_lookup
            return reference_lookup(entry)
    runtime=_runtime()
    evaluator=bridge or runtime.Bridge(ROOT/'data/bin/luapolicy',ROOT/'modules',source_bytes=max(runtime.MAX_SOURCE,len(code.encode())+1024),value_bytes=8*1024*1024,execution_ms=1000)
    wrapped='local policy=(function()\n'+code+'\nend)()\nreturn {decide=function(frame) if type(policy.gear_evaluation)~="function" then return {available=false} end return {available=true,result=policy.gear_evaluation(frame)} end}'
    def evaluate(frame):
        reply=evaluator.evaluate(wrapped,frame)
        return reply.get('result') if reply.get('available') is True else None
    if (world.get('selfEquipmentKnown') is True and inventory.get('equipmentKnown') is True
            and str(world.get('selfEquipmentRevision'))!=str(inventory.get('equipmentRevision'))):
        return _unknown(dict(status='unknown',sessionId=session_id,characterGuid=guid,itemEntry=item_entry,comparisons=[],gameEffectClaimed=False),'world_inventory_equipment_revision_changed')
    result=check_inventory(inventory,session_id=session_id,character_guid=guid,item_entry=item_entry,item_guid=item_guid,world=world,lookup=lookup,evaluate=evaluate,policy_revision=revision)
    after=rpc.call('session.policy_source',identity)
    after_code=after.get('source')
    if (after.get('sessionId')!=session_id or str(after.get('characterGuid'))!=guid or after.get('revision')!=revision
            or not isinstance(after_code,str) or hashlib.sha256(after_code.encode()).hexdigest()!=revision):
        return _unknown(result,'body_or_policy_changed_during_check')
    return result

def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__)
    mode=parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--rpc-url')
    mode.add_argument('--sheet-json',type=Path)
    parser.add_argument('--session-id')
    parser.add_argument('--character-guid')
    parser.add_argument('--item-entry',type=int,required=True)
    parser.add_argument('--item-guid')
    parser.add_argument('--policy-file',type=Path)
    args=parser.parse_args(argv)
    if args.sheet_json:
        if args.session_id or args.character_guid or args.item_guid:
            parser.error('sheet mode has no connected-body or owned-GUID assertion')
        result=compare_set(json.loads(args.sheet_json.read_text()),item_entry=args.item_entry,policy_source=args.policy_file.read_text() if args.policy_file else None)
    else:
        if not args.session_id or not args.character_guid or args.policy_file:
            parser.error('actor mode requires bound session/character and uses that actor policy')
        runtime=_runtime()
        rpc=runtime.RPC(args.rpc_url,response_bytes=16*1024*1024)
        result=inspect(rpc,session_id=args.session_id,character_guid=args.character_guid,item_entry=args.item_entry,item_guid=args.item_guid)
    print(json.dumps(result,allow_nan=False))
if __name__=='__main__':
    main()
