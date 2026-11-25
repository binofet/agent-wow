"""Pinned public enchantment facts and conservative flat modifier resolution.

No server override, active aura, recipient eligibility or upgrade is inferred.
A partial result MUST NOT be used as the item's total realized modifiers.
"""
import copy,gzip,hashlib,json
from collections import Counter
from functools import lru_cache
from pathlib import Path
DATA=Path(__file__).resolve().parents[1]/'pkg/gamecatalog/enchantment-reference.json.gz'
INDEX_SHA256='f5dc103421bfc42c1f4f5d3263de02dcbfde7351ec8badfd3196825108fd388f'

@lru_cache(maxsize=1)
def _data():
 raw=DATA.read_bytes()
 if hashlib.sha256(raw).hexdigest()!=INDEX_SHA256:raise ValueError('enchantment_reference_checksum_mismatch')
 data=json.loads(gzip.decompress(raw))
 if data['schemaVersion']!=1 or data['build']!=12340:raise ValueError('enchantment_reference_version_mismatch')
 return data

def metadata():
 d=_data();return dict(scope=d['scope'],build=d['build'],indexSHA256=INDEX_SHA256,provenance=copy.deepcopy(d['provenance']))

def lookup(enchantment_id):return _lookup('SpellItemEnchantment',enchantment_id)
def suffix(suffix_id):return _lookup('ItemRandomSuffix',suffix_id)
def random_property(property_id):return _lookup('ItemRandomProperties',property_id)
def gem(gem_properties_id):return _lookup('GemProperties',gem_properties_id)
def _lookup(table,ident):
 if type(ident) is not int or not 0<ident<2**32:raise ValueError('positive_reference_id_required')
 return copy.deepcopy(_data()['tables'][table].get(str(ident)))

def _uint(value):return type(value) is int and 0<=value<2**32

def resolve(details, *, recipient=None, template=None):
 """Resolve complete native sparse enhancementDetails to flat reference deltas.

 Slots/known masks come from the current owned World observation. Negative
 randomPropertyId denotes a suffix; propertySeed is its OBSERVED factor, never
 derived from item level. Socket bonus/meta/prismatic/proc/spell effects remain
 unknown. Plain native sockets require explicit nonzero template socket color.
 """
 reasons=[];stats=Counter();armor=0;res=[0]*6;damage=0
 def unknown(reason):
  if reason not in reasons:reasons.append(reason)
 if not isinstance(details,dict):details={};unknown('enhancement_details_unobserved')
 masks=[details.get(k) for k in ('idKnownMask','durationKnownMask','chargesKnownMask')]
 if any(type(mask) is not int or mask!=4095 for mask in masks):unknown('complete_native_slot_fields_required')
 raw=details.get('enchantments',[]);slots={}
 if not isinstance(raw,list):raw=[];unknown('malformed_sparse_enchantments')
 for row in raw:
  if (not isinstance(row,dict) or type(row.get('slot',0)) is not int or not 0<=row.get('slot',0)<12
      or row.get('slot',0) in slots or any(not _uint(row.get(k,0)) for k in ('id','durationRaw','charges'))):
   unknown('malformed_sparse_enchantments');continue
  slot=row.get('slot',0);slots[slot]=row
  for mask,key in zip(masks,('id','durationRaw','charges')):
   if row.get(key,0) and (type(mask) is not int or not mask&(1<<slot)):unknown('value_without_known_slot_field')
 if details.get('propertySeedKnown') is not True or details.get('randomPropertyIdKnown') is not True:unknown('random_property_context_unobserved')
 prop=details.get('randomPropertyId',0) if details.get('randomPropertyIdKnown') is True else None
 seed=details.get('propertySeed',0) if details.get('propertySeedKnown') is True else None
 if prop is not None and (type(prop) is not int or not -(2**31)<=prop<2**31):unknown('invalid_signed_random_property');prop=None
 if seed is not None and not _uint(seed):unknown('invalid_observed_suffix_factor');seed=None
 # Never infer the chosen random property from a template's loot pool.
 random_row=suffix(-prop) if prop is not None and prop<0 else random_property(prop) if prop else None
 if prop and random_row is None:unknown('random_property_reference_missing')
 if prop:
  for i,ident in enumerate((random_row or {}).get('enchantmentIds',[])):
   if slots.get(7+i,{}).get('id',0)!=ident:unknown('observed_random_slot_does_not_match_reference')
 for slot,row in sorted(slots.items()):
  ident=row.get('id',0)
  if not ident:continue
  enchant=lookup(ident)
  if enchant is None:unknown('enchantment_reference_missing:'+str(ident));continue
  if slot in (5,6):unknown('socket_bonus_or_prismatic_condition_unresolved');continue
  if enchant['conditionId']:unknown('enchantment_condition_unresolved');continue
  if slot in (2,3,4):
   sockets=(template or {}).get('sockets')
   colors=[s.get('color') for s in sockets or [] if isinstance(s,dict) and s.get('slot')==slot-1]
   # Static provider socket slots are one-based: native slot2 is template slot1.
   if len(colors)!=1 or type(colors[0]) is not int or colors[0]<=0:unknown('native_or_prismatic_socket_context_unresolved');continue
  req_level,req_skill,req_rank=enchant['requiredLevel'],enchant['requiredSkill'],enchant['requiredSkillRank']
  if req_level:
   level=(recipient or {}).get('level')
   if type(level) is not int or level<req_level:unknown('enchantment_level_eligibility_unverified');continue
  if req_skill:
   skills=(recipient or {}).get('skills')
   if not isinstance(skills,dict) or type(skills.get(req_skill)) is not int or skills[req_skill]<req_rank:unknown('enchantment_skill_eligibility_unverified');continue
  for effect in enchant['effects']:
   kind,amount,arg=effect['type'],effect['amount'],effect['argument']
   if kind==0:continue
   if kind not in (2,4,5):unknown('unsupported_enchantment_effect_type:'+str(kind));continue
   if kind in (4,5) and amount==0:
    if prop is None or prop>=0 or seed is None or random_row is None:unknown('observed_negative_suffix_and_factor_required');continue
    entries=random_row['enchantmentIds'];matches=[i for i,e in enumerate(entries) if e==ident]
    if len(matches)!=1:unknown('suffix_enchantment_allocation_ambiguous');continue
    product=random_row['allocationPct'][matches[0]]*seed
    if product>=2**32:unknown('suffix_allocation_overflow_unsupported');continue
    amount=product//10000
   if not 0<=amount<2**31:unknown('enchantment_amount_outside_supported_signed_range');continue
   if kind==2:damage+=amount
   elif kind==4:
    if arg==0:armor+=amount
    elif 1<=arg<=6:res[arg-1]+=amount
    else:unknown('unsupported_resistance_school')
   elif arg in (0,1,3,4,5,6,7,38,39,45):stats[arg]+=amount
   else:unknown('unsupported_item_stat_type:'+str(arg))
 subtotal=dict(stats=[dict(type=k,value=v) for k,v in sorted(stats.items())],armor=armor,resistances=res,weaponDamageFlat=damage)
 return dict(complete=not reasons,modifiers=subtotal if not reasons else None,
             supportedSubtotal=subtotal,unknownReasons=reasons,referenceOnly=True,
             scope=_data()['scope'],indexSHA256=INDEX_SHA256)
