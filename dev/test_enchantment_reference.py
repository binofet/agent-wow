import copy,hashlib,importlib.machinery,importlib.util
from pathlib import Path
import tempfile,unittest
from unittest.mock import patch
import enchantment_reference as ref
ROOT=Path(__file__).resolve().parents[1]
INPUT=Path('/home/jmax/dev/warcraft_buddy/.artifact-jobs/static-enchantment-reference-input')
loader=importlib.machinery.SourceFileLoader('enchantment_generator',str(ROOT/'dev/build-enchantment-reference'));spec=importlib.util.spec_from_loader(loader.name,loader);gen=importlib.util.module_from_spec(spec);loader.exec_module(gen)

def details(ids=(),prop=0,seed=0):
 return dict(idKnownMask=4095,durationKnownMask=4095,chargesKnownMask=4095,
             propertySeedKnown=True,propertySeed=seed,randomPropertyIdKnown=True,randomPropertyId=prop,
             enchantments=[dict(slot=slot,id=ident) for slot,ident in ids])

class EnchantmentReferenceTests(unittest.TestCase):
 def test_real_data_complete_rows_provenance_and_cached_detachment(self):
  d=ref._data();self.assertEqual({k:len(v) for k,v in d['tables'].items()},dict(SpellItemEnchantment=2656,ItemRandomSuffix=95,ItemRandomProperties=2012,GemProperties=626,SpellItemEnchantmentCondition=49))
  self.assertFalse(ref.metadata()['provenance']['wholeArchiveHashVerified'])
  with patch.object(Path,'read_bytes',side_effect=AssertionError('no repeated parsing')):
   self.assertEqual(ref.lookup(15)['effects'][0]['amount'],8)
  row=ref.lookup(15);row['effects'][0]['amount']=999;self.assertEqual(ref.lookup(15)['effects'][0]['amount'],8)
  self.assertIsNone(ref.lookup(4294967295))
 def test_real_armor_stat_and_weapon_damage(self):
  rows=ref._data()['tables']['SpellItemEnchantment']
  weapon=next(r for r in rows.values() if not r['conditionId'] and not r['requiredLevel'] and not r['requiredSkill'] and any(e['type']==2 and e['amount']>0 for e in r['effects']) and all(e['type'] in (0,2) for e in r['effects']))
  r=ref.resolve(details([(0,15),(1,24),(7,weapon['id'])]))
  self.assertTrue(r['complete'],r);self.assertEqual(r['modifiers']['armor'],8)
  self.assertEqual(r['modifiers']['stats'],[dict(type=0,value=5)])
  self.assertEqual(r['modifiers']['weaponDamageFlat'],sum(e['amount'] for e in weapon['effects'] if e['type']==2))
 def test_real_negative_suffix_observed_factor_matches_five_native_slots(self):
  suffix=ref.suffix(5);state=details([(7+i,e) for i,e in enumerate(suffix['enchantmentIds']) if e],prop=-5,seed=12345)
  r=ref.resolve(state);self.assertTrue(r['complete'],r)
  expected={}
  for i,ident in enumerate(suffix['enchantmentIds']):
   if not ident:continue
   for effect in ref.lookup(ident)['effects']:
    if effect['type']==5:expected[effect['argument']]=suffix['allocationPct'][i]*12345//10000
  self.assertEqual(r['modifiers']['stats'],[dict(type=k,value=v) for k,v in sorted(expected.items())])
  changed=copy.deepcopy(state);changed['enchantments'][0]['id']=15
  self.assertFalse(ref.resolve(changed)['complete'])
  state['propertySeedKnown']=False;self.assertFalse(ref.resolve(state)['complete'])
 def test_partial_unknown_zero_and_native_proto_slot_zero(self):
  self.assertTrue(ref.resolve(details())['complete'])
  state=details([(0,15)]);state['enchantments'][0].pop('slot')
  self.assertEqual(ref.resolve(state)['modifiers']['armor'],8)
  for key in ('idKnownMask','durationKnownMask','chargesKnownMask'):
   state=details();state[key]=4094;self.assertFalse(ref.resolve(state)['complete'])
  state=details([(0,15)]);state['idKnownMask']=0
  self.assertIn('value_without_known_slot_field',ref.resolve(state)['unknownReasons'])
 def test_complete_masks_require_exact_uint_integer_even_without_sparse_rows(self):
  for key in ('idKnownMask','durationKnownMask','chargesKnownMask'):
   for invalid in (4095.0,True,False,'4095',None,-1,0,4096,2**32):
    with self.subTest(key=key,value=invalid):
     state=details();state[key]=invalid
     result=ref.resolve(state)
     self.assertFalse(result['complete']);self.assertIsNone(result['modifiers'])
     self.assertIn('complete_native_slot_fields_required',result['unknownReasons'])
 def test_proc_equip_spell_conditions_and_socket_bonus_never_partial_total(self):
  for ident in (1891,):
   r=ref.resolve(details([(0,ident)]));self.assertFalse(r['complete']);self.assertIsNone(r['modifiers'])
  conditional=next(r for r in ref._data()['tables']['SpellItemEnchantment'].values() if r['conditionId'])
  self.assertFalse(ref.resolve(details([(0,conditional['id'])]))['complete'])
  self.assertFalse(ref.resolve(details([(5,15)]))['complete'])
  self.assertFalse(ref.resolve(details([(2,15)]))['complete'])
  self.assertTrue(ref.resolve(details([(2,15)]),template=dict(sockets=[dict(slot=1,color=2)]))['complete'])
 def test_unavailable_requirements_and_unsupported_values_stay_unknown(self):
  r=ref.resolve(details([(0,1483)]));self.assertFalse(r['complete']) # actual level requirement
  self.assertTrue(ref.resolve(details([(0,1483)]),recipient=dict(level=80))['complete'])
  for state in ({},details([(12,15)]),details([(0,15),(0,16)]),details([(0,True)])):
   self.assertFalse(ref.resolve(state)['complete'])
 def test_deterministic_generator_pins_and_validation(self):
  if not INPUT.is_dir():self.skipTest('Verified public DBC input unavailable; no fetch')
  data=gen.build(INPUT);self.assertEqual(gen.encoded(data),(ROOT/'pkg/gamecatalog/enchantment-reference.json.gz').read_bytes())
  self.assertEqual(gen.encoded(data),gen.encoded(data))
  with tempfile.TemporaryDirectory() as folder:
   p=Path(folder)/'bad.dbc';p.write_bytes(b'bad')
   with self.assertRaisesRegex(ValueError,'checksum'):gen.table(p,*gen.PINS['SpellItemEnchantment.dbc'])

if __name__=='__main__':unittest.main()
