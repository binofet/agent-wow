import importlib.machinery
import importlib.util
from pathlib import Path
import tempfile
import unittest

def load(name,filename):
    loader=importlib.machinery.SourceFileLoader(name,str(Path(__file__).with_name(filename)))
    spec=importlib.util.spec_from_loader(name,loader);module=importlib.util.module_from_spec(spec);loader.exec_module(module);return module
g=load('catalog_generator','build-game-catalog')
c=load('catalog_lookup','game-catalog.py')

class CatalogTests(unittest.TestCase):
    def test_sql_quotes_commas_newlines_and_null_are_data(self):
        with tempfile.TemporaryDirectory() as folder:
            p=Path(folder)/'fixture.sql'
            p.write_text("CREATE TABLE `t` (`id` int, `name` text, `amount` int) ENGINE=InnoDB;\n"
                         "INSERT INTO `t` VALUES (7,'Crate, O\\'Brien; \\nnext',NULL),(8,'It''s safe',3);")
            self.assertEqual(list(g.sql_rows(p)),[dict(id=7,name="Crate, O'Brien; \nnext",amount=None),dict(id=8,name="It's safe",amount=3)])

    def test_fear_break_item_cooldowns_preserve_exact_overrides(self):
        row=dict(entry=18854, **{'class':4,'inventorytype':12})
        for i in range(1,6):
            row.update({f'spellid_{i}':0,f'spelltrigger_{i}':0,f'spellcooldown_{i}':-1,
                        f'spellcategory_{i}':0,f'spellcategorycooldown_{i}':-1})
        row.update(spellid_1=42292,spellcooldown_1=300000,spellcategory_1=1182,spellcategorycooldown_1=300000)
        from unittest.mock import patch
        with patch.object(g,'sql_rows',return_value=iter([row,dict(row,entry=7,inventorytype=16)])):
            self.assertEqual(g.fear_break_items(None),{18854:dict(cooldownMs=300000,category=1182,categoryCooldownMs=300000)})

    def test_lua_preserves_utf8_and_control_bytes(self):
        encoded=g.lua('Élune\x005\u0001')
        self.assertIn('Élune',encoded)
        self.assertIn('\\0005\\001',encoded)
        self.assertNotIn('\\u',encoded)

    def test_id_namespaces_and_bounded_observed_context(self):
        self.assertEqual(c.lookup('items',6076)['name'],'Tapered Pants')
        self.assertIsNone(c.lookup('items',0))
        with self.assertRaises(ValueError):c.lookup('anything',6076)
        value=c.context(dict(questObjects=[dict(entry=161557)]),
                        dict(inventoryBags=[dict(slots=[dict(entry=6076)])]),
                        dict(questLog=[dict(questId=3904)]))
        self.assertEqual(set(value['items']),{'6076'})
        self.assertEqual(value['quests']['3904']['requiredItems'],[dict(itemId=11119,count=8)])
        self.assertEqual(value['gameobjects']['161557']['action'],'open_and_loot')
        self.assertEqual(c.context({}, {}, {})['items'],{})

if __name__=='__main__':unittest.main()
