import importlib.util
import json
from pathlib import Path
import unittest

spec=importlib.util.spec_from_file_location('combat_context',Path(__file__).with_name('combat_context.py'))
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)

class Clock:
    def __init__(self):self.t=100
    def __call__(self):return self.t

def page(events,sid='aw-test',guid='1',**kw):
    return dict(sessionId=sid,selfGuid=guid,latestSequence=str(len(events)),nextSequence=str(len(events)),events=events,**kw)

def event(seq,kind='spell_damage',**kw):
    return dict(sequence=str(seq),receivedAtUnixMs=str(seq*100),kind=kind,opcode=592,
                sourceGuid='1',targetGuid='9',**dict({'spellId':1004},**kw))

def scene(combat=False,alive=True):
    return {'self':dict(guid='1',present=True,healthKnown=True,health=100,maxHealth=100,
            aliveKnown=True,alive=alive,unitFlagsKnown=True,unitFlags=0x80000 if combat else 8,
            positionKnown=True,x=1,y=1,z=1), 'nearbyPlayers':[], 'nearbyCreatures':[]}

class CombatContextTests(unittest.TestCase):
    def feed(self,**kw):
        c=m.CombatContext('1',**kw);c.bind('aw-test');return c

    def test_only_known_effect_amounts_are_credited(self):
        c=self.feed();c.ingest(page([event(1,amountKnown=True,amount=123),
            event(2,'heal',amountKnown=True,amount=50), event(3,'cast_go')]))
        rows=c.context()['events']
        self.assertEqual(sum(x['amount'] for x in rows if x['kind']=='spell_damage'),123)
        self.assertEqual(sum(x['amount'] for x in rows if x['kind']=='heal'),50)
        self.assertFalse(next(x for x in rows if x['kind']=='cast_go')['amountKnown'])

    def test_proto_omitted_zero_is_a_known_zero_not_invalid(self):
        c=self.feed();c.ingest(page([event(1,amountKnown=True),event(2,'cast_failure',resultKnown=True)]))
        self.assertEqual(c.context()['events'][0]['result'],0)
        self.assertEqual(sum(x['amount'] for x in c.context()['events']),0)

    def test_identity_rejection_does_not_advance_cursor(self):
        c=self.feed()
        with self.assertRaises(ValueError):c.ingest(page([event(1)],guid='4'))
        self.assertEqual(c.cursor,0)
        c.ingest(page([event(1)]));c.bind('aw-next')
        self.assertEqual(c.cursor,0)
        with self.assertRaises(ValueError):c.ingest(page([event(1)]))

    def test_unreported_cursor_hole_is_disclosed(self):
        c=self.feed();c.ingest(dict(sessionId='aw-test',selfGuid='1',latestSequence='4',
            nextSequence='4',events=[event(1),event(4)]))
        self.assertEqual(c.context()['coverage']['gaps'][0]['fromSequence'],'2')
        self.assertEqual(c.cursor,4)

    def test_prompt_budget_does_not_cap_retained_native_log(self):
        c=self.feed(prompt_bytes=2400)
        events=[event(i,amountKnown=True,amount=1,spellId=i) for i in range(1,401)]
        # Override helper's spellId without duplicate kwargs.
        c.ingest(page(events))
        summary=c.context()
        self.assertGreater(summary['omittedGroups'],0)
        self.assertTrue(summary['rawEvidence']['retained'])
        self.assertEqual(summary['eventCount'],400)
        self.assertLessEqual(len(json.dumps(summary,separators=(',',':')).encode()),2500)

    def test_projected_own_pose_and_explicit_omitted_dead_flag(self):
        c=self.feed();s=scene(alive=False);s['self'].pop('alive');s['self'].pop('health')
        c.observe(s,{'position':{'known':True,'x':50,'y':60,'z':3}},None)
        v=c.context();self.assertEqual(v['scene']['position']['x'],50)
        self.assertFalse(v['scene']['players']['1']['alive'])
        self.assertEqual(v['scene']['players']['1']['health'],0)

    def test_review_novelty_rate_and_failure_backoff(self):
        clock=Clock();c=self.feed(clock=clock);c.ingest(page([]))
        c.observe(scene(True),{},None);clock.t+=10;c.observe(scene(False),{},None)
        candidate=c.review_candidate();self.assertIsNotNone(candidate)
        self.assertTrue(c.reserve_review(candidate['key']))
        self.assertIsNone(c.review_candidate())
        clock.t+=60;self.assertIsNotNone(c.review_candidate())
        c.mark_reviewed(candidate['key']);self.assertIsNone(c.review_candidate())
        c.observe(scene(True),{},None);clock.t+=65;c.observe(scene(False),{},None)
        self.assertIsNone(c.review_candidate())  # Familiar clean fight, no extra model turn.

    def test_new_listener_starts_at_explicit_tail_then_ingests_new_data(self):
        c=self.feed();clock=Clock();c.clock=clock
        class RPC:
            def call(self, method, params):
                if method=='buddy-world.snapshot':return scene(True)
                if params['afterSequence']=='0':
                    return dict(sessionId='aw-test',selfGuid='1',latestSequence='900',nextSequence='1',events=[event(1,amountKnown=True,amount=999)])
                return dict(sessionId='aw-test',selfGuid='1',latestSequence='901',nextSequence='901',events=[event(901,amountKnown=True,amount=7)])
        c.poll(RPC(),{'sessionId':'aw-test','selfGuid':'1'})
        self.assertEqual(c.cursor,900);self.assertEqual(c.event_count,0)
        self.assertEqual(c.context()['coverage']['afterSequence'],'900')
        clock.t+=1;c.poll(RPC(),{'sessionId':'aw-test','selfGuid':'1'})
        self.assertEqual(c.cursor,901)
        self.assertEqual(sum(x['amount'] for x in c.context()['events']),7)

    def test_unavailable_feed_stays_explicit_and_never_triggers_review(self):
        c=self.feed()
        class RPC:
            def call(self,method,params):
                if method=='buddy-world.snapshot':return scene(True)
                raise RuntimeError('method not found')
        c.poll(RPC(),{'sessionId':'aw-test','selfGuid':'1'})
        self.assertFalse(c.context()['available'])
        self.assertIn('method not found',c.context()['reason'])
        self.assertIsNone(c.review_candidate())

    def test_current_fight_does_not_claim_lifetime_damage(self):
        c=self.feed();c.ingest(page([event(1,amountKnown=True,amount=999)]))
        c.observe(scene(True),{},None)
        c.ingest(dict(sessionId='aw-test',selfGuid='1',latestSequence='2',nextSequence='2',
                      events=[event(2,amountKnown=True,amount=5)]))
        self.assertEqual(sum(x['amount'] for x in c.context()['events']),5)
        self.assertEqual(c.context()['coverage']['afterSequence'],'1')

if __name__=='__main__':unittest.main()
