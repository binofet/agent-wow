"""Public chat audience routing, separate from Lua tactical decisions."""
import unittest
from test_converse import converse, FakeRPC, FakeBrain, ProfilePartyTests, poll, message

ORDER=('Those Creedping Sludge enemies have an AoE effect that is deadly. '
       'We will use ranged damage only to bring them down. '
       'Casters use wands, not melee if you run out of mana. Rook, no melee!')
ROSTER={1:'Arlen',4:'Mira',5:'Rook',8:'Nixwick'}

class ClauseScopeTests(unittest.TestCase):
    def test_mixed_order_keeps_party_role_and_named_clauses_separate(self):
        scope=converse.owner_message_scope(ORDER,ROSTER.values())
        self.assertTrue(scope['broadcast'])
        self.assertEqual([r['audience'] for r in scope['clauses']],['party','party','role','named'])
        self.assertEqual(scope['clauses'][2]['role'],'casters')
        self.assertEqual(scope['clauses'][3]['names'],['rook'])
        self.assertEqual(ORDER[scope['clauses'][3]['start']:scope['clauses'][3]['end']],'Rook, no melee')
        self.assertEqual(converse.addressed_names(ORDER,ROSTER.values()),set())

    def test_other_mixed_and_role_only_orders_are_not_stolen_by_names(self):
        for text in ('Hold at this doorway. Mira, watch our rear.',
                     'Rook, wait; everyone stay behind the pillar.',
                     'Healers, conserve mana. Rook, interrupt.',
                     'Use ranged attacks against Rook\'s target.'):
            with self.subTest(text=text):self.assertEqual(converse.addressed_names(text,ROSTER.values()),set())
        scope=converse.owner_message_scope('Healers, conserve mana.',ROSTER.values())
        self.assertEqual(scope['clauses'][0]['role'],'healers')

    def test_strict_named_orders_remain_exclusive_and_mentions_are_not_addresses(self):
        for text,names in [('Rook, no melee!',{'rook'}),('Arlen and Mira, follow',{'arlen','mira'}),
                           ('Arlen, heal Joel. Mira, attack.',{'arlen','mira'}),
                           ('Mira, follow Rook',{'mira'})]:
            self.assertEqual(converse.addressed_names(text,ROSTER.values()),names)
        self.assertEqual(converse.addressed_names('Stay near Mira.',ROSTER.values()),set())
        self.assertEqual(converse.addressed_names("Mira's mana is low.",ROSTER.values()),set())

class OwnerRoutingTests(unittest.TestCase):
    def loop(self,name,text,channel='PARTY',receiver='0'):
        loop,rpc,brain,controls,_=ProfilePartyTests().setup_party(
            [poll(),poll([message(1,text,channel=channel,receiverGuid=receiver)])],
            reply=dict(text='Received.',action='NONE',emote='NONE'))
        loop.character_name=name;loop.character_guid=next(g for g,n in ROSTER.items() if n==name)
        loop.party_agents={g:n for g,n in ROSTER.items() if n!=name}
        original=brain.generate
        def generate(history,state):
            brain.last_state=state
            return original(history,state)
        brain.generate=generate
        return loop,rpc,brain,controls

    def test_exact_mixed_party_order_reaches_all_four_without_automatic_gameplay(self):
        for name in ROSTER.values():
            with self.subTest(name=name):
                loop,rpc,brain,controls=self.loop(name,ORDER)
                loop.tick();loop.tick()
                self.assertEqual(brain.histories[-1][-1]['text'],ORDER)
                self.assertEqual(controls.executed,['NONE'])
                self.assertFalse(any(m=='session.send_event' or m.startswith('buddy-actions.') for m,_ in rpc.calls))
                self.assertEqual(brain.last_state['current_request']['messageScopes'][0]['scope'],
                    converse.owner_message_scope(ORDER,ROSTER.values()))
                self.assertEqual(loop.cursor,1)
                self.assertTrue(loop.owner_message_waiting(poll([message(2,ORDER,channel='PARTY')])))

    def test_standalone_named_order_skips_others_but_not_body_addressed_whisper(self):
        loop,_,brain,_=self.loop('Mira','Rook, no melee!')
        loop.tick();loop.tick();self.assertEqual(brain.histories,[])
        for receiver,heard in [('4',True),('3',True),('99',False)]:
            loop,_,brain,_=self.loop('Mira','Rook is too close. Help him.',channel='WHISPER',receiver=receiver)
            loop.tick();loop.tick();self.assertEqual(bool(brain.histories),heard)

    def test_mixed_order_preempts_but_other_named_order_does_not(self):
        loop,_,_,_=self.loop('Mira',ORDER)
        loop.cursor=0
        self.assertTrue(loop.owner_message_waiting(poll([message(1,ORDER,channel='PARTY')])))
        self.assertFalse(loop.owner_message_waiting(poll([message(1,'Rook, no melee!',channel='PARTY')])))
