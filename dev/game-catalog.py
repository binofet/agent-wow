"""Shared, bounded ID lookups for the conversation controller and CLI."""
import functools
import json
from pathlib import Path


@functools.lru_cache(maxsize=1)
def catalog():
    return json.loads((Path(__file__).resolve().parent.parent/'pkg/gamecatalog/catalog.json').read_text())


def lookup(kind, identifier):
    if kind not in ('items','quests','gameobjects'):raise ValueError('Unknown catalog namespace')
    if not str(identifier).isdigit() or not 0<int(identifier)<=0xffffffff:return None
    return catalog()[kind].get(str(int(identifier)))


def context(observations, inventory, quests):
    """Attach records for observed IDs only; never dump the database to an LLM."""
    ids={'items':set(),'quests':set(),'gameobjects':set()}
    for row in observations.get('questObjects',[])[:32]:
        if isinstance(row,dict):ids['gameobjects'].add(row.get('entry',0))
    for row in quests.get('questLog',[])[:25]:
        if isinstance(row,dict):ids['quests'].add(row.get('questId',0))
    for bag in inventory.get('inventoryBags',[])[:5]:
        for row in bag.get('slots',[])[:36]:
            if isinstance(row,dict):ids['items'].add(row.get('entry',0))
    # Conversation inventory is already reduced to an ownership-checked list.
    for row in inventory.get('items',[])[:180]:
        if isinstance(row,dict):ids['items'].add(row.get('entry',0))
    out={kind:{str(i):record for i in sorted((int(v) for v in values if str(v).isdigit() and int(v)>0))[:32]
               if (record:=lookup(kind,i)) is not None} for kind,values in ids.items()}
    return dict(out,sourceCommit=catalog()['sourceCommit'],build=12340,
                meaning='Static content knowledge; live observations and installed controls determine what can actually be done.')


if __name__=='__main__':
    import argparse
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('kind',choices=['items','quests','gameobjects']);p.add_argument('id',type=int)
    a=p.parse_args();print(json.dumps(lookup(a.kind,a.id),ensure_ascii=False))
