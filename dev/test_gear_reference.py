import copy
import gzip
import hashlib
import importlib.machinery
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import gear_reference as gear

ROOT=Path(__file__).resolve().parents[1]
SOURCE=Path('/home/jmax/dev/warcraft_buddy/.artifact-jobs/gm-usable-item-data-input/item_template.sql')
loader=importlib.machinery.SourceFileLoader('gear_reference_generator',str(ROOT/'dev/build-item-requirements'))
spec=importlib.util.spec_from_loader(loader.name,loader);generator=importlib.util.module_from_spec(spec);loader.exec_module(generator)


class GearReferenceTests(unittest.TestCase):
    def test_complete_pinned_coverage_identity_and_cache(self):
        meta=gear.metadata();self.assertEqual(meta['itemCount'],46096)
        self.assertEqual(meta['sourceCommit'],'49bdb66ce2fe00771c9e27a9859afd344b7b1344')
        self.assertEqual(meta['itemTemplate']['sha256'],'bd4859be01946ca1521f7896f558f665e1e1d98890757a3df30e717b62768cc6')
        rows,items,_=gear._data();self.assertEqual(set(rows),{int(k) for k in items})
        with patch.object(Path,'read_bytes',side_effect=AssertionError('cached reads only')):
            self.assertEqual(gear.lookup(1196)['name'],'Tabar');gear.lookup(1645);gear.metadata()

    def test_real_plain_weapon_exact_raw_damage_signed_masks_and_zero(self):
        row=gear.lookup(1196)
        self.assertEqual((row['itemClass'],row['subclass'],row['requiredLevel'],row['delayMs']),(2,1,9,3200))
        self.assertEqual(row['allowableClass'],-1);self.assertEqual(row['allowableRace'],-1)
        self.assertEqual(row['damages'],[dict(minimum=21,maximum=32,school=0)])
        self.assertEqual(row['resistances'],[0]*6)
        self.assertEqual(row['armor'],0);self.assertFalse(row['hasSpell'])
        self.assertTrue(row['armorComparisonKnown']);self.assertTrue(row['weaponComparisonKnown'])
        self.assertEqual(len(row['spells']),5) # absent spellID0 still retains real cooldown -1.
        self.assertEqual(row['spells'][0]['cooldownMs'],-1)

    def test_real_effects_sets_sockets_random_and_scaling_remain_visible_not_scored(self):
        rows,items,meta=gear._data()
        for key in ('itemSet','randomProperty','randomSuffix','socketBonus','scalingStatDistribution'):
            source=next(r for r in rows.values() if r[key] not in (None,0))
            row=gear.lookup(source['itemId'])
            self.assertFalse(row['armorComparisonKnown'],(key,row['itemId']))
            self.assertFalse(row['weaponComparisonKnown'])
        thunder=gear.lookup(19019)
        self.assertTrue(thunder['hasSpell']);self.assertTrue(any(s['id']>0 and s['trigger']!=0 for s in thunder['spells']))
        socketed=next(r for r in rows.values() if any(s[1] for s in r['sockets']))
        self.assertTrue(gear.lookup(socketed['itemId'])['sockets'])

    def test_nongear_class_is_truthful_and_no_recipient_or_live_claim(self):
        row=gear.lookup(1645)
        self.assertEqual(row['itemClass'],0);self.assertTrue(row['hasSpell']);self.assertTrue(row['referenceOnly'])
        self.assertFalse(row['weaponComparisonKnown'])
        self.assertNotIn('usable',row);self.assertNotIn('upgrade',row)
        self.assertEqual(row['provenance']['scope'],gear.SCOPE)
        self.assertEqual(gear.lookup(4294967295),None)
        for value in (True,0,-1,'1196',1.5):
            with self.assertRaises(ValueError):gear.lookup(value)

    def test_null_never_becomes_zero_or_comparable_and_return_is_detached(self):
        rows,items,meta=gear._data();row=copy.deepcopy(rows[1196]);row['armor']=None
        normalized=gear._normalize(row,items['1196'],meta)
        self.assertIsNone(normalized['armor']);self.assertFalse(normalized['available']);self.assertFalse(normalized['armorComparisonKnown'])
        row=copy.deepcopy(rows[1196]);row['spells'][0][1]=None
        normalized=gear._normalize(row,items['1196'],meta)
        self.assertIsNone(normalized['hasSpell']);self.assertFalse(normalized['available'])
        got=gear.lookup(1196);got['damages'][0]['minimum']=999;got['provenance']['scope']='bad'
        self.assertEqual(gear.lookup(1196)['damages'][0]['minimum'],21)
        self.assertEqual(gear.lookup(1196)['provenance']['scope'],gear.SCOPE)

    def test_generator_deterministic_full_public_input_and_validation(self):
        if not SOURCE.is_file():self.skipTest('Pinned external public SQL input unavailable; no fetch')
        index=generator.build(SOURCE,ROOT/'pkg/gamecatalog/catalog.json')
        encoded=generator.encoded(index)
        self.assertEqual(encoded,(ROOT/'pkg/gamecatalog/item-requirements.json.gz').read_bytes())
        self.assertEqual(encoded,generator.encoded(index))
        self.assertEqual(len(index['rows']),46096)
        self.assertIn('spells',index['columns']);self.assertIn('sockets',index['columns'])
        with tempfile.TemporaryDirectory() as folder:
            bad=Path(folder)/'bad.sql';bad.write_text('not the pinned data')
            with self.assertRaisesRegex(ValueError,'checksum'):generator.build(bad,ROOT/'pkg/gamecatalog/catalog.json')
        for value in (True,float('nan'),float('inf'),'0'):
            with self.assertRaises(ValueError):generator.numeric(value)
        self.assertIsNone(generator.numeric(None));self.assertEqual(generator.numeric(-1,integer=True),-1)

    def test_corrupt_join_fails_closed(self):
        with tempfile.TemporaryDirectory() as folder:
            p=Path(folder)
            for name in ('catalog.json','item-requirements.json.gz'):(p/name).write_bytes((gear.ROOT/name).read_bytes())
            catalog=json.loads((p/'catalog.json').read_bytes());catalog['build']=1;(p/'catalog.json').write_text(json.dumps(catalog))
            gear._data.cache_clear()
            try:
                with patch.object(gear,'ROOT',p),self.assertRaisesRegex(ValueError,'provenance'):gear.lookup(1196)
            finally:gear._data.cache_clear()

if __name__=='__main__':unittest.main()
