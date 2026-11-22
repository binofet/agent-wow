"""Practice target choices through the maintained full Lua bundle, no game transport."""
import copy
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import subprocess
import unittest
ROOT=Path(__file__).resolve().parent.parent
loader=importlib.machinery.SourceFileLoader('practice_target_runtime',str(ROOT/'dev/task-runtime'))
spec=importlib.util.spec_from_loader(loader.name,loader);runtime=importlib.util.module_from_spec(spec);loader.exec_module(runtime)

class PracticeTargetPolicyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.bridge=runtime.Bridge(Path(os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY',ROOT/'data/bin/luapolicy')),
            ROOT/'modules',source_bytes=8*1024*1024,value_bytes=8*1024*1024,execution_ms=1000)
        cls.source=subprocess.check_output(['python3','-B',str(ROOT/'dev/encounter-bundle')],text=True)
    def candidate(self,n,distance,**fields):
        return dict(guid=str((0xf130<<48)+n),creatureObject=True,entry=299,present=True,mapKnown=True,mapId=0,
            positionKnown=True,x=-8933+distance,y=-136,z=81,aliveKnown=True,alive=True,
            healthKnown=True,unitFlagsKnown=True,health=50,maxHealth=50,levelKnown=True,level=1,**fields)
    def frame(self):
        return dict(sessionId='same-practice-body',selfGuid='4',autoAttackKnown=False,selection='practice_target',
            currentTargetGuid='0',failedTargetGuid='0',candidates=[self.candidate(1,5),self.candidate(2,15)],
            observation=dict(sessionId='same-practice-body',selfGuid='4',mapId=0,
                players={'4':dict(guid='4',present=True,mapKnown=True,mapId=0,positionKnown=True,x=-8933,y=-136,z=81)}))
    def choose(self,value,source=None):
        return self.bridge.evaluate('local p=(function()\n'+(source or self.source)+'\nend)();return {decide=p.starter_spell}',value)
    def test_nearest_default_and_lua_only_rank_radius_eligibility_retuning(self):
        frame=self.frame();near,far=frame['candidates']
        self.assertEqual(self.choose(frame),dict(kind='target',targetGuid=near['guid']))
        edited=self.source.replace('search_yards=120,order="nearest"','search_yards=120,order="farthest"')
        self.assertEqual(self.choose(frame,edited),dict(kind='target',targetGuid=far['guid']))
        frame['candidates']=[self.candidate(3,150)]
        self.assertEqual(self.choose(frame),dict(kind='wait',targetGuid='0'))
        expanded=self.source.replace('search_yards=120,order="nearest"','search_yards=180,order="nearest"')
        self.assertEqual(self.choose(frame,expanded)['targetGuid'],frame['candidates'][0]['guid'])
        frame['candidates'][0]['entry']=69
        alternate=expanded.replace('entries={[299]=true}','entries={[69]=true}')
        self.assertEqual(self.choose(frame,alternate)['targetGuid'],frame['candidates'][0]['guid'])
    def test_claimed_controlled_engaged_or_unknown_rows_never_become_future_pull(self):
        for fields in [dict(dynamicFlags=4),dict(protected=True),dict(unitFlags=524288),
                dict(attackVictimKnown=True,attackVictimGuid='3'),dict(targetKnown=True,targetGuid='3'),
                dict(levelKnown=False),dict(unitFlagsKnown=False),dict(positionKnown=False),dict(healthKnown=False),dict(mapId=1),dict(npcFlags=1)]:
            with self.subTest(fields=fields):
                frame=self.frame();frame['candidates'][0].update(fields)
                self.assertEqual(self.choose(frame)['targetGuid'],frame['candidates'][1]['guid'])
    def test_current_target_is_retained_without_new_pull_radius_or_nearest_switch(self):
        frame=self.frame();current=frame['candidates'][1]
        frame['currentTargetGuid']=current['guid'];current.update(x=-8700,unitFlags=524288,
            attackVictimKnown=True,attackVictimGuid='4',attackAgeMs=0)
        self.assertEqual(self.choose(frame)['targetGuid'],current['guid'])
        current['attackVictimGuid']='3'
        self.assertEqual(self.choose(frame),dict(kind='stop',targetGuid='0'))
    def test_unknown_attacker_check_is_independent_of_candidate_order(self):
        frame=self.frame();frame['currentTargetGuid']=frame['candidates'][0]['guid']
        attacker=self.candidate(3,10);attacker.update(entry=69,attackVictimKnown=True,attackVictimGuid='4',attackAgeMs=0)
        for rows in [frame['candidates']+[attacker],[attacker]+frame['candidates']]:
            frame['candidates']=rows
            self.assertEqual(self.choose(frame),dict(kind='stop',targetGuid='0'))
    def test_lua_distinguishes_missing_current_target_from_uncertain_position(self):
        frame=self.frame();frame['currentTargetGuid']=frame['candidates'][0]['guid']
        frame['candidates'][0]['positionKnown']=False
        self.assertEqual(self.choose(frame),dict(kind='wait',targetGuid='0'))
        frame['candidates'][0]['present']=False
        self.assertEqual(self.choose(frame),dict(kind='stop',targetGuid='0'))
        frame['candidates']=frame['candidates'][1:]
        self.assertEqual(self.choose(frame),dict(kind='stop',targetGuid='0'))
        frame['currentTargetGuid']='0'
        self.assertEqual(self.choose(frame)['targetGuid'],frame['candidates'][0]['guid'])
    def test_equal_distance_uses_exact_guid_tie_break_not_input_order(self):
        frame=self.frame();frame['candidates'][1]['x']=frame['candidates'][0]['x']
        expected=frame['candidates'][0]['guid']
        self.assertEqual(self.choose(frame)['targetGuid'],expected)
        frame['candidates'].reverse()
        self.assertEqual(self.choose(frame)['targetGuid'],expected)

    def pull_frame(self, class_id=8):
        frame=self.frame()
        frame['selection']='practice_pull_readiness'
        for key in ('candidates','currentTargetGuid','failedTargetGuid'):
            del frame[key]
        frame['observation']['players']['4'].update(classKnown=True,classId=class_id,
            healthKnown=True,health=100,maxHealth=100,powerKnown=True,
            powerType=3 if class_id==4 else 0,power=100,maxPower=100,
            baseManaKnown=True,baseMana=100)
        return frame

    def test_pull_resource_defaults_and_known_base_mana_fallback(self):
        for class_id,threshold in ((4,90),(5,45),(8,40)):
            frame=self.pull_frame(class_id)
            self.assertEqual(self.choose(frame),dict(kind='ready',spellId=0))
            player=frame['observation']['players']['4']
            player['power']=threshold-1
            self.assertEqual(self.choose(frame),dict(kind='wait',spellId=0))
            player['power']=threshold
            self.assertEqual(self.choose(frame),dict(kind='ready',spellId=0))
            player['health']=94
            self.assertEqual(self.choose(frame),dict(kind='wait',spellId=0))
        frame=self.pull_frame(5)
        player=frame['observation']['players']['4']
        player.update(baseMana=101,power=45)
        self.assertEqual(self.choose(frame)['kind'],'wait')
        player['power']=46
        self.assertEqual(self.choose(frame)['kind'],'ready')
        for known,base in ((False,100),(True,0)):
            player.update(baseManaKnown=known,baseMana=base,maxPower=200,power=89)
            self.assertEqual(self.choose(frame)['kind'],'wait')
            player['power']=90
            self.assertEqual(self.choose(frame)['kind'],'ready')

    def test_pull_thresholds_are_shared_source_choices_and_need_no_spell_or_target(self):
        old='health_percent=95,mana_percent={[5]=45,[8]=40},energy=90'
        self.assertEqual(self.source.count(old),1)
        edited=self.source.replace(old,'health_percent=60,mana_percent={[5]=30,[8]=30},energy=50')
        for class_id in (4,5,8):
            frame=self.pull_frame(class_id)
            frame['observation']['players']['4'].update(health=70,power=50 if class_id==4 else 35)
            before=copy.deepcopy(frame)
            self.assertEqual(self.choose(frame)['kind'],'wait')
            self.assertEqual(self.choose(frame,edited),dict(kind='ready',spellId=0))
            self.assertEqual(frame,before)

    def test_pull_unknown_or_inconsistent_resources_never_authorize(self):
        for changed in (dict(healthKnown=False),dict(health=0),dict(maxHealth=0),dict(health=101),
                        dict(powerKnown=False),dict(power=-1),dict(powerType=3),
                        dict(classKnown=False),dict(classId=9),dict(baseManaKnown=False,maxPower=-1)):
            with self.subTest(changed=changed):
                frame=self.pull_frame()
                frame['observation']['players']['4'].update(changed)
                self.assertEqual(self.choose(frame),dict(kind='wait',spellId=0))

if __name__=='__main__':unittest.main()
