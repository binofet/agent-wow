import unittest
from unittest.mock import patch


import party_bag_pressure as bags


GUID_BASE = 0x4000000000000000
JUNK_ID, GEAR_ID, QUEST_ID = 1, 2, 3


def item(bag, slot, entry, count=1):
    return dict(bag=bag, slot=slot, itemGuid=str(GUID_BASE + 1 + bag * 100 + slot), entry=entry,
                stackCount=count, metadataKnown=True)


def snapshot(guid, loot=True):
    session = 'body'
    backpack = [item(0, slot, GEAR_ID) for slot in range(16)]
    equipped = [item(1, 0, JUNK_ID, 2), item(1, 1, QUEST_ID)]
    slots = [backpack, equipped, [], [], []]
    first = dict(guid=str(guid), present=True, aliveKnown=True, alive=True, unitFlagsKnown=True)
    position = dict(known=True, mapKnown=True, mapId=34, x=5, y=7, z=9)
    return dict(world=dict(self=first, lootableCorpses=[dict(position, present=True, positionKnown=True, x=6)]),
        actions=dict(sessionId=session, selfGuid=str(guid), position=position),
        inventory=dict(sessionId=session, selfGuid=str(guid), inventoryKnown=True, inventoryRevision='19',
            inventoryBags=[dict(bag=bag, slotKnown=True, contentsKnown=True, slotCount=len(rows), slots=rows)
                           for bag, rows in enumerate(slots)],
            items=[dict(item=row, metadataKnown=True, safeJunkSaleEligible=True, unitSellPriceCopper=5)
                   for row in [*backpack, *equipped]]),
        trade=dict(sessionId=session, phase='IDLE'),
        loot=dict(sessionId=session, enabled=loot, inventoryPaused=loot))


class BagPressureTests(unittest.TestCase):
    def setUp(self):
        self.entries = bags.safe_junk_entries({'items': {
            str(JUNK_ID): dict(itemClass=15, subclass=0),
            str(GEAR_ID): dict(itemClass=4, subclass=1),
            str(QUEST_ID): dict(itemClass=15, subclass=0, questIds=[386])}})
        self.assertEqual(self.entries, {JUNK_ID})
        self.views = {8088: snapshot(5)}
        self.attempts = {}
        self.calls = []
        self.lookup_available = True
        self.dispatch_error = None

    def rpc(self, port, method, args):
        self.calls.append((port, method, args))
        if method == 'buddy-trade.lookup_items':
            return dict(sessionId='body', items=[dict(itemId=JUNK_ID, available=self.lookup_available)])
        if self.dispatch_error:
            raise RuntimeError(self.dispatch_error)
        return {}

    def tick(self, when=100, port=8088):
        with patch.object(bags.time, 'monotonic', return_value=when):
            return bags.maintain_bag_pressure(self.rpc, self.views, self.attempts, self.entries)[port]

    def test_rook_frees_only_gray_miscellaneous_with_exact_source(self):
        self.assertEqual(self.tick(), 'waiting_for_disposal')
        self.assertEqual(self.calls, [(8088, 'buddy-trade.lookup_items', {'itemIds': [JUNK_ID]}),
            (8088, 'buddy-trade.destroy_junk', dict(sourceBag=1, sourceSlot=0, inventoryRevision='19',
                sessionId='body', revision='0', expectedItemGuid=str(GUID_BASE + 101)))])

    def test_requires_matching_receipt_and_fresh_inventory_and_cools_down(self):
        self.tick()
        rook = self.views[8088]
        rook['trade']['lastJunkDisposal'] = dict(itemGuid=str(GUID_BASE + 101), status='SERVER_SLOT_REMOVAL_CONFIRMED')
        self.assertEqual(self.tick(101), 'waiting_for_disposal')
        rook['inventory']['inventoryRevision'] = '20'
        rook['inventory']['inventoryBags'][1]['slots'][0] = dict(bag=1, slot=0)
        self.assertEqual(self.tick(102), 'confirmed_removed')
        rook['inventory']['inventoryBags'][1]['slots'][0] = item(1, 0, GEAR_ID)
        self.assertEqual(self.tick(105), 'cooldown')

    def test_ambiguous_transport_is_never_replayed(self):
        self.dispatch_error = 'connection closed without response'
        self.assertEqual(self.tick(), 'disposal_unconfirmed_no_retry')
        self.assertEqual(self.tick(180), 'disposal_unconfirmed_no_retry')
        self.assertEqual(sum(method.endswith('destroy_junk') for _, method, _ in self.calls), 1)

    def test_no_space_pressure_from_stale_corpse_or_unverified_water_count(self):
        self.views[8088]['world']['lootableCorpses'][0]['x'] = 50
        self.assertEqual(self.tick(), 'no_current_bag_pressure')
        self.views[8086] = snapshot(1, loot=False)
        self.assertEqual(self.tick(port=8086), 'no_current_bag_pressure')
        self.views[8086]['inventory']['manaDrinkCount'] = 0
        self.views[8086]['world']['self']['unitFlags'] = bags.IN_COMBAT
        self.assertEqual(self.tick(port=8086), 'waiting_for_safe_pause')
        self.assertEqual(self.calls, [])
        self.views[8086]['world']['self']['unitFlags'] = 0
        self.assertEqual(self.tick(port=8086), 'waiting_for_disposal')

    def test_equipped_item_requires_confirmed_trade_template(self):
        self.lookup_available = False
        self.assertEqual(self.tick(), 'waiting_for_item_template')
        self.assertEqual(self.tick(102), 'cooldown')
        self.lookup_available = True
        self.assertEqual(self.tick(106), 'waiting_for_disposal')

    def test_existing_room_or_trade_prevents_disposal(self):
        rook = self.views[8088]
        rook['trade']['waterGiftArmed'] = True
        self.assertEqual(self.tick(), 'waiting_for_safe_pause')
        rook['inventory']['inventoryBags'][1]['slots'][0] = dict(bag=1, slot=0)
        self.assertEqual(self.tick(), 'room_available')
        self.assertEqual(self.calls, [])


if __name__ == '__main__':
    unittest.main()
