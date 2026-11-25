"""Cached, pinned PUBLIC template facts. Never current realm/owned-item evidence."""
import copy
from functools import lru_cache
import gzip
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1] / 'pkg/gamecatalog'
SCOPE = 'PINNED_PUBLIC_STATIC_TEMPLATE_NOT_CURRENT_REALM_OR_RECIPIENT_STATE'


@lru_cache(maxsize=1)
def _data():
    raw=(ROOT/'item-requirements.json.gz').read_bytes()
    index=json.loads(gzip.decompress(raw))
    catalog_raw=(ROOT/'catalog.json').read_bytes()
    catalog=json.loads(catalog_raw)
    if (index.get('schemaVersion')!=1 or index['provenance']['catalogSHA256']!=hashlib.sha256(catalog_raw).hexdigest()
            or index['sourceCommit']!=catalog['sourceCommit'] or index['build']!=catalog['build']
            or index['sourceSHA256']!=catalog['sources']['item_template']['sha256']):
        raise ValueError('static_gear_reference_provenance_mismatch')
    columns=index['columns']
    needed={'itemId','stats','damage','resistances','spells','sockets','armorDamageModifier','scriptName'}
    if len(set(columns))!=len(columns) or not needed.issubset(columns):
        raise ValueError('static_gear_reference_extended_schema_required')
    rows={}
    for values in index['rows']:
        if len(values)!=len(columns):raise ValueError('static_gear_reference_row_width')
        row=dict(zip(columns,values));entry=row['itemId']
        if type(entry) is not int or entry<=0 or entry in rows:raise ValueError('static_gear_reference_item_id')
        rows[entry]=row
    if set(rows)!={int(k) for k in catalog['items']}:raise ValueError('static_gear_reference_coverage_mismatch')
    meta=dict(scope=SCOPE,referenceOnly=True,build=index['build'],sourceCommit=index['sourceCommit'],
              itemTemplate=copy.deepcopy(index['provenance']['itemTemplate']),
              indexSHA256=hashlib.sha256(raw).hexdigest(),catalogSHA256=hashlib.sha256(catalog_raw).hexdigest(),
              itemCount=len(rows),generator=index['provenance']['generator'],
              generatorSHA256=index['provenance']['generatorSHA256'],
              sqlParserSHA256=index['provenance']['sqlParserSHA256'],
              knownness='Zero is observed public template data; null is unknown. No ownership, usable/equipped, realm override or actual upgrade claim.')
    return rows,catalog['items'],meta


def metadata():
    """Exact source identity, separate from any recipient or current server."""
    return copy.deepcopy(_data()[2])


def _known(value):
    if isinstance(value,list):return all(_known(v) for v in value)
    if isinstance(value,dict):return all(_known(v) for v in value.values())
    return value is not None


def _normalize(row,item,meta):
    out={key:copy.deepcopy(row[key]) for key in (
        'itemId','quality','armor','delayMs','itemLevel','requiredLevel','flags',
        'allowableClass','allowableRace','requiredSkill','requiredSkillRank','requiredSpell',
        'requiredHonorRank','requiredCityRank','requiredReputationFaction','requiredReputationRank',
        'area','map','holiday','bonding','block','randomProperty','randomSuffix','itemSet',
        'socketBonus','gemProperties','totemCategory','duration','itemLimitCategory','armorDamageModifier','scriptName')}
    out.update({key:copy.deepcopy(item[key]) for key in ('name','itemClass','subclass','inventoryType')})
    out.update(flags2=row['flagsExtra'],scalingDistribution=row['scalingStatDistribution'],
               scalingValue=row['scalingStatValue'],resistances=copy.deepcopy(row['resistances']),
               stats=[dict(type=kind,value=value) for _,kind,value in row['stats']],
               damages=[dict(minimum=low,maximum=high,school=school) for _,low,high,school in row['damage']],
               spells=[dict(slot=slot,id=ident,trigger=trigger,charges=charges,ppmRate=ppm,cooldownMs=cooldown,
                            category=category,categoryCooldownMs=category_cd)
                       for slot,ident,trigger,charges,ppm,cooldown,category,category_cd in row['spells']],
               sockets=[dict(slot=slot,color=color,content=content) for slot,color,content in row['sockets']])
    out['hasSpell']=any(s['id'] not in (None,0) for s in out['spells']) if _known(out['spells']) else None
    out['available']=_known(out)
    # Refuse scalar-only comparison when a template can carry value outside the
    # shared scorer. Exact effects remain exposed; no spell/set/socket valuation.
    special_keys=('flags2','scalingDistribution','scalingValue','randomProperty','randomSuffix','itemSet',
                  'socketBonus','gemProperties','totemCategory','duration','itemLimitCategory','armorDamageModifier')
    special=(any(out[k] not in (0,None) for k in special_keys) or out['hasSpell'] is True
             or bool(out['sockets']) or out['scriptName'] not in ('',None))
    complete=out['available'] and len(out['resistances'])==6
    out['armorComparisonKnown']=bool(complete and not special)
    out['weaponComparisonKnown']=bool(complete and not special and out['itemClass']==2
        and out['delayMs']>0 and out['damages']
        and all(d['school']==0 and 0<=d['minimum']<=d['maximum'] for d in out['damages']))
    out['referenceOnly']=True
    out['provenance']=dict(scope=SCOPE,build=meta['build'],sourceCommit=meta['sourceCommit'],
                          sourceSHA256=meta['itemTemplate']['sha256'],indexSHA256=meta['indexSHA256'])
    return out


def lookup(entry):
    """Return detached normalized facts, or None for an absent public entry."""
    if type(entry) is not int or not 0<entry<2**32:raise ValueError('positive_item_entry_required')
    rows,items,meta=_data();row=rows.get(entry)
    return None if row is None else _normalize(row,items[str(entry)],meta)
