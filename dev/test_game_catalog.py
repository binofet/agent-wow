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
