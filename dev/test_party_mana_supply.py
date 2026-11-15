import unittest

import party_mana_supply as supply


def observation(guid, count, x, level=20):
    sid = f"aw-{guid}"
    flags = dict(guid=str(guid), present=True, aliveKnown=True, alive=True,
                 unitFlagsKnown=True, mapKnown=True, mapId=34, levelKnown=True,
                 level=level, positionKnown=True, x=x, power=100, maxPower=100, powerKnown=True)
    return {
        "world": {"self": flags, "owner": dict(flags, guid="3")},
        "party": dict(sessionId=sid, group=dict(
            known=True, inGroup=True, groupGuid="77", members=[
                dict(guid=str(guid), self=True, online=True), dict(guid="3", online=True),
            ],
        )),
        "actions": dict(sessionId=sid, selfGuid=str(guid)),
        "inventory": dict(sessionId=sid, selfGuid=str(guid), inventoryKnown=True,
                          manaDrinkCount=count, inventoryBags=[dict(contentsKnown=True, slotCount=1, slots=[{}])]),
        "trade": dict(sessionId=sid, phase="IDLE", revision="2"),
    }


def carried(mira, entry, count):
    mira["world"].update(backpackKnown=True, backpackRevision="5", backpack=[
        dict(entry=entry, stackCount=count, metadataKnown=True, slot=8,
             itemGuid=str(0x4000000000000001)),
    ])


class SupplyTest(unittest.TestCase):
    def setUp(self):
        self.mira = observation(4, 18, 0)
        self.arlen = observation(1, 0, 3)
        self.nix = observation(8, 16, 4)
        self.observations = {8086: self.arlen, 8087: self.mira, 8402: self.nix}
        self.calls = []
        self.refresh_world()

    def refresh_world(self):
        for observation in self.observations.values():
            observation["world"]["nearbyPlayers"] = [
                other["world"]["self"] for other in self.observations.values() if other is not observation
            ]

    def rpc(self, port, method, params):
        if method == "buddy-actions.spell_toolkit":
            rows = [dict(familyId=5504, capability="create_item", createdItemEntry=item,
                         rank=rank, spellId=spell) for rank, spell, item in ((2, 5505, 2288), (3, 5506, 2136))]
            return dict(sessionId="aw-4", selfGuid="4", spellbookKnown=True, spells=rows)
        if method == "buddy-trade.lookup_items":
            return dict(sessionId="aw-4", items=[dict(itemId=2288, available=True, requiredLevel=5),
                                                 dict(itemId=2136, available=True, requiredLevel=15)])
        self.calls.append((port, method, params))
        if method == "buddy-trade.water_gift":
            return dict(sessionId="aw-4" if port == 8087 else "aw-1", waterGiftArmed=True)
        self.assertEqual(method, "buddy-actions.create_item")
        return dict(sessionId="aw-4")

    def step(self):
        return supply.maintain_supply(self.rpc, self.observations, {})

    def test_water_at_full_mana_flows_to_out_of_stock_caster(self):
        carried(self.mira, 2136, 8)
        self.assertEqual(self.step(), "water_trade_started")
        self.assertEqual([call[:2] for call in self.calls],
                         [(8086, "buddy-trade.water_gift"), (8087, "buddy-trade.water_gift")])
        self.assertEqual(self.calls[1][2]["partnerGuid"], "1")
        self.assertEqual(self.calls[1][2]["backpackSlot"], 8)

    def test_preserves_own_supply_when_recipient_still_has_water(self):
        carried(self.mira, 2288, 14)
        self.arlen["world"]["self"]["level"] = 14
        self.arlen["inventory"]["manaDrinkCount"] = 1
        self.assertEqual(self.step(), "conjure_requested")
        self.assertEqual(self.calls, [(8087, "buddy-actions.create_item", dict(
            sessionId="aw-4", selfGuid="4", spellId=5505,
        ))])

    def test_initial_resupply_does_not_wait_to_conjure_a_second_stack(self):
        self.mira["inventory"]["manaDrinkCount"] = 3
        self.arlen["world"]["self"].update(power=45, maxPower=1297)
        carried(self.mira, 2136, 3)
        self.assertEqual(self.step(), "water_trade_started")
        self.assertEqual(self.calls[1][2]["itemCount"], 3)
        self.assertEqual(self.calls[1][2]["partnerGuid"], "1")

        self.calls.clear()
        self.mira["inventory"]["manaDrinkCount"] = 0
        self.mira["world"]["backpack"].clear()
        self.arlen["inventory"]["manaDrinkCount"] = 3
        self.assertEqual(self.step(), "conjure_requested")
        self.assertEqual(self.calls[0][2]["spellId"], 5506)

    def test_chooses_highest_currently_usable_rank(self):
        self.assertEqual(self.step(), "conjure_requested")
        self.assertEqual(self.calls[0][2]["spellId"], 5506)

    def test_skips_well_stocked_distant_fighting_or_drinking_recipients(self):
        for overrides in (
                {"x": 15}, {"unitFlags": 0x80000},
        ):
            with self.subTest(overrides=overrides):
                self.arlen["world"]["self"].update(x=3, unitFlags=0, **{
                    key: value for key, value in overrides.items() if key not in ("x", "unitFlags")
                })
                self.arlen["world"]["self"].update(overrides)
                self.assertEqual(self.step(), "water_stock_sufficient")
        self.arlen["world"]["self"].update(x=3, unitFlags=0)
        self.arlen["mana_rest_active"] = True
        self.assertEqual(self.step(), "water_stock_sufficient")
        self.arlen["mana_rest_active"] = False
        self.mira["mana_rest_active"] = True
        self.assertEqual(self.step(), "mage_busy")
        self.assertEqual(self.calls, [])

    def test_full_bags_with_no_water_do_not_attempt_a_trade_or_conjure(self):
        carried(self.mira, 2136, 8)
        self.arlen["inventory"]["inventoryBags"][0]["slots"][:] = [
            dict(entry=117, stackCount=5, metadataKnown=True, itemGuid="11"),
        ]
        self.assertEqual(self.step(), "recipient_needs_bag_space")
        self.assertEqual(self.calls, [])

    def test_full_bags_can_accept_a_matching_partial_stack(self):
        carried(self.mira, 2136, 8)
        self.arlen["inventory"]["manaDrinkCount"] = 2
        self.arlen["inventory"]["inventoryBags"][0]["slots"][:] = [
            dict(entry=2136, stackCount=2, metadataKnown=True, itemGuid="14"),
        ]
        self.assertEqual(self.step(), "water_trade_started")
        self.assertEqual(self.calls[1][2]["itemEntry"], 2136)

    def test_observes_peer_with_current_action_position(self):
        carried(self.mira, 2136, 8)
        self.mira["world"]["self"]["x"] = 500
        self.mira["actions"]["position"] = dict(known=True, mapKnown=True, mapId=34, x=0)
        self.arlen["world"]["nearbyPlayers"][0] = dict(self.mira["world"]["self"], x=0)
        self.assertEqual(self.step(), "water_trade_started")

    def test_pending_conjure_is_not_repeated(self):
        self.mira["actions"]["lastSpellOperation"] = dict(capability="create_item", phase="waiting_created_item")
        self.assertEqual(self.step(), "conjure_pending")
        self.assertEqual(self.calls, [])


if __name__ == "__main__":
    unittest.main()
