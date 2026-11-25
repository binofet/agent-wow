"""Pinned public-data eligibility and simulated advertised-tool boundary tests."""
import copy
import json
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import Mock

import item_search as search
from direct_tools import DirectTools
from test_direct_tools import RPC

ROOT = Path(__file__).resolve().parent.parent


class ItemSearchTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.catalog = json.loads((ROOT/'pkg/gamecatalog/catalog.json').read_text())
        cls.requirements = search.requirements()

    def recipient(self, **changes):
        who = dict(guid='3', classId=5, level=40, raceId=1, powerType=0,
                   skills={415:200, 136:200, 54:200, 173:200})
        who.update(changes)
        return who

    def find(self, args, **changes):
        return search.find(self.catalog, self.recipient(**changes), args)

    def test_four_actual_unsuitable_drops_rejected_for_level40_caster_and_warrior(self):
        for cls, power in ((5,0),(1,1)):
            result=self.find(dict(item_ids=[13444,31839,13447,34751]),classId=cls,powerType=power)
            self.assertEqual([r['requiredLevel'] for r in result['checked']],[49,61,44,70])
            self.assertTrue(all(not r['matches'] and r['reason']=='required_level_too_high' for r in result['checked']))
            self.assertFalse(result['gameEffectClaimed'])

    def test_real_water_and_healing_potions_match_level40(self):
        rows=self.find(dict(item_ids=[1645,1710,3928]))['checked']
        self.assertTrue(all(r['matches'] and r['reason'] is None for r in rows))
        water=self.find(dict(kind='mana_drink', name='Moonberry Juice'))['matches']
        self.assertEqual([r['itemId'] for r in water],[1645])
        self.assertGreater(water[0]['manaPerTick'],0)
        self.assertTrue(water[0]['useSpells'])

    def test_mana_resource_unknown_and_nonmana_are_not_eligible(self):
        for power,reason in ((1,'mana_not_current_resource'),(None,'current_resource_unknown')):
            result=self.find(dict(kind='mana_drink'),powerType=power)
            self.assertEqual(result['matches'],[])
            self.assertGreater(result['excludedByFirstReason'].get(reason,0),0)
            self.assertEqual(self.find(dict(item_ids=[1645]),powerType=power)['checked'][0]['reason'],reason)
        self.assertTrue(self.find(dict(item_ids=[3928]),powerType=1)['checked'][0]['matches'])

    def test_known_proficiency_and_missing_complete_baseline(self):
        row=self.find(dict(kind='armor', name='Regal Robe'))['matches'][0]
        item_id=row['itemId']
        self.assertTrue(self.find(dict(item_ids=[item_id]))['checked'][0]['matches'])
        for skills,reason in ((None,'recipient_proficiency_unobserved'),({},'required_skill_or_proficiency_not_met')):
            checked=self.find(dict(item_ids=[item_id]),skills=skills)['checked'][0]
            self.assertFalse(checked['matches']);self.assertEqual(checked['reason'],reason)

    def test_actual_restricted_class_and_race_templates(self):
        # Select actual source rows, avoiding invented item requirement values.
        class_row=next(r for r in self.requirements.values() if r.requiredLevel<=40 and r.allowableClass is not None
                       and r.allowableClass>0 and not r.allowableClass & (1<<4))
        item=self.catalog['items'][str(class_row.itemId)]
        self.assertEqual(search.unmet_requirement(item,class_row,self.recipient(),[],None),'class_restriction_or_unknown')
        race_row=next(r for r in self.requirements.values() if r.requiredLevel<=40 and r.allowableClass is not None
                      and r.allowableClass & (1<<4) and r.allowableRace is not None and r.allowableRace>0
                      and not r.allowableRace & 1)
        item=self.catalog['items'][str(race_row.itemId)]
        self.assertEqual(search.unmet_requirement(item,race_row,self.recipient(),[],None),'race_restriction')
        who=self.recipient();who.pop('raceId')
        self.assertEqual(search.unmet_requirement(item,race_row,who,[],None),'recipient_race_unobserved')

    def test_requirement_unknowns_fail_closed(self):
        r=self.requirements[1645];item=self.catalog['items']['1645']
        for field,reason in (('requiredLevel','required_level_unknown'),('allowableClass','class_restriction_or_unknown'),
                             ('allowableRace','race_restriction_unknown'),('flags','item_flags_unknown'),
                             ('requiredSkill','required_skill_unknown')):
            with self.subTest(field=field):
                self.assertEqual(search.unmet_requirement(item,r._replace(**{field:None}),self.recipient(),
                    [search.use_spells()[i] for i in item['useSpells']],None),reason)

    def test_category_name_words_slot_and_pagination(self):
        page=self.find(dict(kind='armor',name='robe',slot='chest',limit=2))
        self.assertEqual(len(page['matches']),2);self.assertEqual(page['nextOffset'],2)
        next_page=self.find(dict(kind='armor',name='ROBE',slot='chest',offset=2,limit=2))
        self.assertFalse({r['itemId'] for r in page['matches']} & {r['itemId'] for r in next_page['matches']})
        self.assertTrue(all(r['inventoryType'] in (5,20) and 'robe' in r['name'].lower() for r in page['matches']))
        self.assertEqual(self.find(dict(kind='mana_drink',name='juice moonberry'))['totalMatches'],1)
        for kind in ('healing_potion','mana_potion','food_drink','weapon'):
            result=self.find(dict(kind=kind,limit=2))
            self.assertTrue(all(r['requiredLevel']<=40 for r in result['matches']))

    def test_ids_unknown_deduplicate_and_argument_errors(self):
        rows=self.find(dict(item_ids=[1645,1645,4294967295]))['checked']
        self.assertEqual(len(rows),2);self.assertEqual(rows[1]['reason'],'unknown_item_id')
        for args in ({},{'kind':'invented'},{'item_ids':[True]},{'item_ids':list(range(1,14))},
                     {'kind':'gear','limit':0},{'kind':'gear','offset':True},
                     {'kind':'mana_drink','slot':'head'},{'kind':'gear','name':42}):
            with self.subTest(args=args),self.assertRaises(ValueError):search.search_arguments(args)

    def test_actual_directtools_owner_dispatch_and_remote_boundary(self):
        rpc=RPC('4')
        world=dict(self=dict(guid='4',present=True,classKnown=True,classId=5,levelKnown=True,level=40,
                             raceKnown=True,raceId=1,powerKnown=True))
        original=rpc.call
        def call(method,args,**kw):
            if method=='buddy-world.snapshot':
                rpc.calls.append((method,copy.deepcopy(args)));return copy.deepcopy(world)
            return original(method,args,**kw)
        rpc.call=call
        tools=DirectTools(rpc,rpc.sid,'4',None,ROOT,owner_allowed=lambda:True,
             helpers={'game_catalog':SimpleNamespace(catalog=lambda:self.catalog)})
        result=tools.dispatch('find_usable_items',dict(item_ids=[1645]))
        self.assertTrue(result['ok'],result)
        self.assertTrue(result['result']['checked'][0]['matches']);self.assertEqual(rpc.writes,[])
        remote=tools.dispatch('find_usable_items',dict(recipient_guid='3',item_ids=[1645]))
        self.assertFalse(remote['ok']);self.assertEqual(rpc.writes,[])
        denied=DirectTools(rpc,rpc.sid,'4',None,ROOT)
        self.assertNotIn('find_usable_items',[s['function']['name'] for s in denied.tool_schemas()])
        self.assertFalse(denied.dispatch('find_usable_items',dict(item_ids=[1645]))['ok'])

    def test_actual_directtools_authorized_gm_registered_recipient(self):
        from test_item_recipient import GM, RPC as RecipientRPC, person
        rpc=RPC('87')
        target=RecipientRPC('4','aw-mira',dict(self=person('4',5,40,powerKnown=True)), 'http://synthetic-mira/rpc')
        gm=GM([dict(guid='4',name='Mira',rpc_url=target.url)],{target.url:target})
        tools=DirectTools(rpc,rpc.sid,'87',ROOT,ROOT,allowed=lambda:True,gm_allowed=lambda:True,
            helpers={'gm_tasks':gm,'game_catalog':SimpleNamespace(catalog=lambda:self.catalog)})
        result=tools.dispatch('find_usable_items',dict(recipient_guid='4',item_ids=[1645,13444]))
        self.assertTrue(result['ok'],result)
        checked=result['result']['checked']
        self.assertTrue(checked[0]['matches']);self.assertFalse(checked[1]['matches'])
        self.assertEqual(result['result']['recipient']['guid'],'4')
        self.assertEqual(gm.created,[target.url]);self.assertEqual(rpc.writes,[])
        self.assertTrue(all(method in ('buddy-actions.status','buddy-world.snapshot') for method,_ in target.calls))
        blocked=DirectTools(rpc,rpc.sid,'87',ROOT,ROOT,allowed=lambda:True,gm_allowed=lambda:False,
            helpers={'gm_tasks':gm,'game_catalog':SimpleNamespace(catalog=lambda:self.catalog)})
        self.assertFalse(blocked.dispatch('find_usable_items',dict(recipient_guid='4',item_ids=[1645]))['ok'])


class GearToolTests(unittest.TestCase):
    def test_ordinary_actor_compares_an_exact_item_copy_on_its_own_session(self):
        rpc = RPC('4')
        result = dict(status='upgrade', characterGuid='4', comparisons=[dict(equipmentSlot=10)])
        inspect = Mock(return_value=result)
        tools = DirectTools(rpc, rpc.sid, '4', None, ROOT, owner_allowed=lambda: True,
                            helpers={'gear_check': SimpleNamespace(inspect=inspect)})
        reply = tools.dispatch('check_gear_upgrade', dict(item_entry=501, item_guid='888'))
        self.assertEqual(reply, dict(ok=True, result=result))
        inspect.assert_called_once_with(rpc, session_id=rpc.sid, character_guid='4',
                                        item_entry=501, item_guid='888')
        for invalid in (dict(item_entry=True), dict(item_entry=0), dict(item_entry=2**32),
                        dict(item_entry=501, item_guid=888), dict(item_entry=501, recipient_guid='3')):
            self.assertFalse(tools.dispatch('check_gear_upgrade', invalid)['ok'])
        self.assertEqual(inspect.call_count, 1)
        self.assertEqual(rpc.writes, [])

    def test_gm_compares_registered_recipient_and_detects_binding_change(self):
        from test_item_recipient import GM, RPC as RecipientRPC
        rpc = RPC('87')
        target = RecipientRPC('4', 'aw-mira', url='http://mira/rpc')
        gm = GM([dict(guid='4', name='Mira', rpc_url=target.url)], {target.url: target})
        inspect = Mock(return_value=dict(status='not_upgrade', characterGuid='4'))
        tools = DirectTools(rpc, rpc.sid, '87', ROOT, ROOT, allowed=lambda: True,
                            gm_allowed=lambda: True,
                            helpers={'gm_tasks': gm, 'gear_check': SimpleNamespace(inspect=inspect)})
        args = dict(item_entry=501, recipient_guid='4')
        self.assertEqual(tools.dispatch('check_gear_upgrade', args)['result']['characterGuid'], '4')
        inspect.assert_called_once_with(target, session_id='aw-mira', character_guid='4',
                                        item_entry=501, item_guid=None)
        absent = tools.dispatch('check_gear_upgrade', dict(item_entry=501, recipient_guid='3'))
        self.assertEqual(absent['result']['status'], 'unknown')
        self.assertEqual(inspect.call_count, 1)
        inspect.side_effect = lambda *a, **kw: gm.rows.clear() or dict(status='upgrade')
        self.assertFalse(tools.dispatch('check_gear_upgrade', args)['ok'])
        self.assertEqual(rpc.writes, [])


if __name__=='__main__':unittest.main()
