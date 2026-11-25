import copy,hashlib,os,unittest
from pathlib import Path
import gear_check as g
R=Path(__file__).resolve().parents[1];SID='aw-4';GUID='4';ITEM='4611686018427389000';OLD='4611686018427389001'
def template(entry,armor=0):
 return dict(itemId=entry,available=True,itemClass=4,subclass=2,inventoryType=7,quality=2,armor=armor,stats=[],damages=[],delayMs=0,resistances=[0]*6,armorComparisonKnown=True,flags=0,flags2=0,hasSpell=False,scalingDistribution=0,scalingValue=0,allowableClass=-1,allowableRace=-1,requiredLevel=1,requiredSkill=0,requiredSkillRank=0,requiredSpell=0,requiredHonorRank=0,requiredCityRank=0,requiredReputationFaction=0,requiredReputationRank=0,area=0,map=0,holiday=0)
class Check(unittest.TestCase):
 @classmethod
 def setUpClass(cls):
  cls.bridge=g._runtime().Bridge(Path(os.environ['BUDDY_LUAPOLICY_TEST_BINARY']),R/'modules',source_bytes=8*1024*1024,value_bytes=8*1024*1024,execution_ms=1000);cls.gear=(R/'behaviors/gear.lua').read_text()
 def setUp(self):
  equipment=[dict(slot=i,slotKnown=True) for i in range(19)];equipment[6].update(itemGuid=OLD,entry=101,metadataKnown=True,enhancementsKnown=True,hasEnhancements=False,durabilityKnown=True,durability=10,maxDurability=10)
  source=dict(itemGuid=ITEM,entry=100,stackCount=1,bag=0,slot=2,metadataKnown=True)
  self.templates={100:template(100,10),101:template(101,5)}
  self.inv=dict(sessionId=SID,selfGuid=GUID,inventoryKnown=True,inventoryRevision='5',equipmentKnown=True,equipmentRevision='8',equipment=equipment,inventoryBags=[dict(bag=0,slotKnown=True,contentsKnown=True,slots=[copy.deepcopy(source)])],gearContext=dict(templates={str(k):v for k,v in self.templates.items()}),items=[dict(item=source,metadataKnown=True,gearFacts=dict(enhancementsKnown=True,hasEnhancements=False,destinations=[dict(eligible=True,equipmentSlot=6,expectedEquippedItemGuid=OLD)]))])
  skills=[dict(slot=i) for i in range(128)]
  for i,k in enumerate((414,173,118,54,136)):skills[i].update(skillId=k,value=100,maxValue=100)
  self.world=dict(self=dict(guid=GUID,present=True,classKnown=True,classId=4,raceKnown=True,raceId=1,levelKnown=True,level=20),selfSkillsKnown=True,selfSkillsRevision='3',selfSkills=skills,selfEquipmentKnown=True,selfEquipmentRevision='8')
  self.code('')
 def code(self,tuning):
  self.source='local gear=(function()\n'+self.gear+'\nend)()\n'+tuning+'\nreturn {gear_evaluation=gear.evaluate}';self.rev=hashlib.sha256(self.source.encode()).hexdigest();self.inv['gearContext']['policyRevision']=self.rev
 def evaluate(self,frame):return self.bridge.evaluate('local p=(function()\n'+self.source+'\nend)()\nreturn {decide=p.gear_evaluation}',frame)
 def check(self,entry=100,exact=None):return g.check_inventory(self.inv,session_id=SID,character_guid=GUID,item_entry=entry,item_guid=exact,world=self.world,lookup=self.templates.get,evaluate=self.evaluate,policy_revision=self.rev)
 def test_owned_actual_lua_exact_guid_source_and_worn(self):
  self.inv['items']=self.evaluate(dict(inventory=self.inv))['items'];before=copy.deepcopy(self.inv);r=self.check(exact=ITEM);self.assertEqual(r['status'],'upgrade');self.assertEqual(r['comparisons'][0]['worn']['entry'],101);self.assertEqual(self.inv,before)
  self.inv['inventoryBags'][0]['slots'][0]['itemGuid']='99';self.assertEqual(self.check(exact=ITEM)['status'],'unknown')
 def test_unowned_base_is_comparable_without_invented_ownership(self):
  self.inv['items']=[];r=self.check();self.assertEqual(r['status'],'upgrade');self.assertFalse(r['comparisons'][0]['candidate']['owned']);self.assertIn('Hypothetical',r['candidateAssumption']);self.assertFalse(r['gameEffectClaimed']);self.assertEqual(self.check(exact=ITEM)['reason'],'exact_owned_item_not_observed')
 def test_tradeoff_and_role_weights_use_actual_actor_lua(self):
  self.inv['items']=[];self.templates[100]['stats']=[dict(type=4,value=5)];self.templates[101]['stats']=[dict(type=7,value=5)];self.assertEqual(self.check()['status'],'not_upgrade')
  self.code('gear.class_profiles[4]={statWeights={[4]=10,[7]=0},armorWeight=0}');self.assertEqual(self.check()['status'],'upgrade')
 def test_worn_enhancements_remain_actual_and_policy_tuning_preserved(self):
  self.inv['items']=[];worn=self.inv['equipment'][6];worn.pop('enhancementsKnown');self.assertEqual(self.check()['reason'],'enhancement_context_pending')
  worn.update(enhancementsKnown=True,hasEnhancements=True);self.assertEqual(self.check()['status'],'unknown');self.code('gear.tuning.allow_enhanced_comparison=true');self.assertEqual(self.check()['status'],'upgrade')
 def test_ring_slots_and_unique_equipped(self):
  self.inv['items']=[];c=template(300);c.update(subclass=0,inventoryType=11,stats=[dict(type=7,value=4)]);self.templates[300]=c
  for slot,entry,value in ((10,301,3),(11,302,6)):
   self.inv['equipment'][slot].update(itemGuid=str(1000+slot),entry=entry,metadataKnown=True,enhancementsKnown=True,hasEnhancements=False);old=copy.deepcopy(c);old.update(itemId=entry,stats=[dict(type=7,value=value)]);self.templates[entry]=old
  r=self.check(300);self.assertEqual([x['equipmentSlot'] for x in r['comparisons']],[10,11]);self.assertEqual([x['status'] for x in r['comparisons']],['upgrade','not_upgrade'])
  c['flags']=0x80000;self.inv['equipment'][11]['entry']=300;self.assertEqual(self.check(300)['status'],'not_upgrade')
 def test_dagger_slots_and_staff_offhand(self):
  self.inv['items']=[];c=template(400);c.update(itemClass=2,subclass=15,inventoryType=13,delayMs=1000,damages=[dict(minimum=10,maximum=10,school=0)]);self.templates[400]=c
  for slot,entry,damage in ((15,401,8),(16,402,12)):
   self.inv['equipment'][slot].update(itemGuid=str(1000+slot),entry=entry,metadataKnown=True,enhancementsKnown=True,hasEnhancements=False);old=copy.deepcopy(c);old.update(itemId=entry,damages=[dict(minimum=damage,maximum=damage,school=0)]);self.templates[entry]=old
  r=self.check(400);self.assertEqual([x['equipmentSlot'] for x in r['comparisons']],[15,16]);self.assertEqual([x['status'] for x in r['comparisons']],['upgrade','not_upgrade']);c.update(subclass=10,inventoryType=17);self.assertEqual(self.check(400)['reason'],'two_hand_requires_offhand_displacement_comparison')
 def test_specific_missing_requirements_not_all_unowned_unknown(self):
  self.inv['items']=[];self.world['selfSkillsKnown']=False;self.assertEqual(self.check()['reason'],'complete_own_skill_baseline_required');self.world['selfSkillsKnown']=True;self.templates[100]['requiredSpell']=42;self.assertEqual(self.check()['reason'],'special_item_requirement_unverified');self.templates[100]['requiredSpell']=0;self.templates[100]['requiredLevel']=30;self.assertEqual(self.check()['status'],'not_upgrade');self.inv['equipmentKnown']=False;self.assertEqual(self.check()['status'],'unknown')
 def test_real_public_unowned_template_positive_and_effect_uncertainty(self):
  import gear_reference
  self.inv['items']=[];self.inv['equipment'][6]['entry']=1366;self.inv['gearContext']['templates']={}
  r=g.check_inventory(self.inv,session_id=SID,character_guid=GUID,item_entry=2962,world=self.world,lookup=gear_reference.lookup,evaluate=self.evaluate,policy_revision=self.rev)
  self.assertEqual(r['status'],'upgrade');self.assertEqual(r['comparisons'][0]['worn']['entry'],1366)
  c=template(500);c.update(inventoryType=12,subclass=0,hasSpell=True,armorComparisonKnown=False);self.templates[500]=c;r=self.check(500);self.assertEqual(r['status'],'unknown');self.assertEqual(r['reason'],'special_template_requires_policy_choice')
 def test_equipment_duplicate_guid_and_unobserved_empty_never_invent_slot(self):
  self.inv['items']=[];self.inv['equipment'][7].update(itemGuid=OLD,entry=101);self.assertEqual(self.check()['reason'],'complete_own_equipment_baseline_required')
  self.inv['equipment'][7]=dict(slot=7,slotKnown=False);self.assertEqual(self.check()['status'],'unknown')
 def test_inspect_source_body_before_after_and_no_mutations(self):
  self.inv['items']=[];calls=[];test=self
  class RPC:
   rebind=False
   def call(self,name,params):
    calls.append(name)
    if name=='session.describe':return dict(sessionId=SID,characterGuid=GUID)
    if name=='session.policy_source':return dict(sessionId='other' if self.rebind and calls.count(name)%2==0 else SID,characterGuid=GUID,source=test.source,revision=test.rev)
    if name=='buddy-inventory.state':return copy.deepcopy(test.inv)
    if name=='buddy-world.snapshot':return copy.deepcopy(test.world)
    raise AssertionError(name)
  rpc=RPC();r=g.inspect(rpc,session_id=SID,character_guid=GUID,item_entry=100,lookup=self.templates.get,bridge=self.bridge);self.assertEqual(r['status'],'upgrade');self.assertEqual(calls.count('session.policy_source'),2);rpc.rebind=True;r=g.inspect(rpc,session_id=SID,character_guid=GUID,item_entry=100,lookup=self.templates.get,bridge=self.bridge);self.assertEqual(r['reason'],'body_or_policy_changed_during_check')
if __name__=='__main__':unittest.main()
