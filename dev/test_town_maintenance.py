"""Offline finite inventory/mail operator regressions; no real RPC calls."""
import copy
import importlib.machinery
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock
from test_town_policy import service_source, source_reply, policy_reply, bridge_factory

loader = importlib.machinery.SourceFileLoader("town_test", str(Path(__file__).with_name("town-maintenance")))
spec = importlib.util.spec_from_loader(loader.name, loader)
m = importlib.util.module_from_spec(spec)
loader.exec_module(m)
URL = "http://127.0.0.1:8191/rpc"
VENDOR = 0xf130000a23000001
MAILBOX = 0xf110000001000002
POLE_BAG = 0x4000000000000100
JUNK = 0x4000000000000101


def initial():
    bag = {"slotKnown": True, "contentsKnown": True, "slotCount": 16,
           "slots": [{"slot": i} for i in range(16)]}
    bag["slots"][2].update(itemGuid=str(POLE_BAG), entry=41599, stackCount=1, metadataKnown=True)
    bag["slots"][3].update(itemGuid=str(JUNK), entry=7070, stackCount=2, metadataKnown=True)
    state = {"sessionId": "body1", "selfGuid": "6", "revision": "2", "inventoryKnown": True,
             "inventoryRevision": "11", "inventoryBags": [bag]+[{"bag": i, "slotKnown": True, "contentsKnown": True} for i in range(1, 5)],
             "copperKnown": True, "copper": 50, "copperRevision": "7", "items": []}
    for index in (2, 3):
        item = m.ref(bag["slots"][index])
        state["items"].append({"item": item, "metadataKnown": True, "normalBag": index == 2,
                               "containerSlots": 20 if index == 2 else 0,
                               "safeJunkSaleEligible": index == 3, "unitSellPriceCopper": 4 if index == 3 else 0})
    return state


class GearCompatibilityTests(unittest.TestCase):
    def test_actual_gear_receipt_does_not_make_existing_inventory_consumer_unreadable(self):
        state=initial();state['operation']={'kind':'equip_gear','status':'CONFIRMED'}
        self.assertEqual(m.checked_state(state,6)['operation']['kind'],'equip_gear')
        state['operation']['kind']='unknown_item_action'
        with self.assertRaises(m.Error):m.checked_state(state,6)


class FakeRPC:
    def __init__(self):
        self.current, self.calls, self.before, self.after = initial(), [], None, None
        self.policy_source = service_source()
        self.ambiguous = False
        self.mail = {"sessionId": "body1", "listKnown": True, "listCurrent": True, "listRevision": "2",
                     "mailboxGuid": str(MAILBOX), "bagMails": [{"mailId": 7, "attachments": [
                         {"attachmentIndex": i, "itemGuidLow": 2000+i, "entry": 41599, "count": 1} for i in range(4)]}],
                     "bagClaims": [], "bagsClaimReserved": 0}
        self.vendor = True

    def mutations(self):
        return [(method, req) for method, req in self.calls if method in
                ("buddy-inventory.move", "buddy-inventory.equip", "buddy-inventory.sell", "buddy-mail.claim_bag_attachment")]

    def call(self, method, params):
        self.calls.append((method, copy.deepcopy(params)))
        if method == 'session.policy_source':return source_reply(self.policy_source,self.current)
        if method == 'session.attach':return policy_reply(self.policy_source,self.current)
        if method == "buddy-inventory.state":
            return copy.deepcopy(self.current)
        if method == "buddy-inventory.discover":
            return {"state": copy.deepcopy(self.current), "nearby": [{"interactable": self.vendor, "distance": 1,
                "creature": {"guid": str(VENDOR), "entry": VENDOR >> 24 & 0xffffff, "present": True,
                    "mapKnown": True, "positionKnown": True, "aliveKnown": True, "alive": True,
                    "healthKnown": True, "health": 100, "npcFlags": 128}}]}
        if method in ("buddy-mail.list", "buddy-mail.state"):
            return copy.deepcopy(self.mail)
        if self.before:
            self.before(method, params)
        bags, slots = m.layout(self.current)
        before_rev = int(self.current["inventoryRevision"])
        op = {"requestId": params["requestId"], "kind": method.rsplit(".", 1)[1], "status": "CONFIRMED",
              "source": copy.deepcopy(params.get("source")), "inventoryConfirmed": True, "leaseReleased": True,
              "transportAttempted": True, "transportWritten": True, "baselineInventoryRevision": str(before_rev),
              "observedInventoryRevision": str(before_rev+1)}
        if method == "buddy-mail.claim_bag_attachment":
            low = params["itemGuidLow"]
            selected_mail = next(row for row in self.mail["bagMails"] if row["mailId"] == params["mailId"])
            attachment = next(row for row in selected_mail["attachments"] if row.get("attachmentIndex", 0) == params["attachmentIndex"])
            assert attachment["itemGuidLow"] == low and attachment["entry"] == 41599 and attachment["count"] == 1
            g = 0x4000000000000000 + low
            (b, s), target = next(((b, s), i) for (b, s), i in slots.items() if not int(i.get("itemGuid", 0)))
            target.update(itemGuid=str(g), entry=41599, stackCount=1, metadataKnown=True)
            self.current["items"].append({"item": m.ref(target), "metadataKnown": True, "normalBag": True, "containerSlots": 20})
            op = {"request": copy.deepcopy(params), "status": "BAG_CLAIM_CONFIRMED", "transportAttempted": True,
                  "transportWritten": True, "serverAcknowledged": True, "inventoryConfirmed": True, "leaseReleased": True,
                  "baselineInventoryRevision": str(before_rev), "observedInventoryRevision": str(before_rev+1),
                  "itemGuid": str(g), "entry": 41599, "count": 1, "observedBag": b, "observedSlot": s}
            self.mail["lastBagClaim"] = op
            self.mail["bagClaims"].append(op)
            self.mail["bagsClaimReserved"] += 1
            selected_mail["attachments"] = [a for a in selected_mail["attachments"] if a["itemGuidLow"] != low]
            self.mail["bagMails"] = [row for row in self.mail["bagMails"] if row["attachments"]]
        else:
            src = params["source"]
            source = slots[src["bag"], src["slot"]]
            if method == "buddy-inventory.move":
                b, s = params["destinationBag"], params["destinationSlot"]
                slots[b, s].update({**source, "bag": b, "slot": s})
                op.update(destinationBag=b, destinationSlot=s)
                self.current["items"] = [{**i, "item": {**i["item"], "bag": b, "slot": s}} if i["item"]["itemGuid"] == src["itemGuid"] else i for i in self.current["items"]]
            elif method == "buddy-inventory.equip":
                b = params["equipmentSlot"]-18
                eligibility = next(i for i in self.current["items"] if i["item"] == src)
                capacity = eligibility["containerSlots"]
                bags[b].update(containerGuid=src["itemGuid"], entry=src["entry"], metadataKnown=True, contentsKnown=True,
                               slotCount=capacity, slots=[{"bag": b, "slot": i} for i in range(capacity)])
                op.update(equipmentSlot=params["equipmentSlot"], equipmentConfirmed=True)
                self.current["items"] = [i for i in self.current["items"] if i["item"]["itemGuid"] != src["itemGuid"]]
            elif method == "buddy-inventory.sell":
                credit = params["expectedUnitSellPriceCopper"]*src["stackCount"]
                old = self.current["copper"]
                op.update(vendorGuid=params["vendorGuid"], unitSellPriceCopper=params["expectedUnitSellPriceCopper"], expectedCreditCopper=credit,
                          baselineCopper=old, observedCopper=old+credit, baselineCopperRevision=self.current["copperRevision"],
                          observedCopperRevision=str(int(self.current["copperRevision"])+1), copperConfirmed=True)
                self.current.update(copper=old+credit, copperRevision=op["observedCopperRevision"])
                self.current["items"] = [i for i in self.current["items"] if i["item"]["itemGuid"] != src["itemGuid"]]
            else:
                raise AssertionError(method)
            source.clear(); source.update(bag=src["bag"], slot=src["slot"])
            self.current["operation"] = op
        self.current.update(inventoryRevision=str(before_rev+1), revision=str(int(self.current["revision"])+1))
        if self.after:
            self.after(method, op)
        if self.ambiguous:
            raise m.Error("ambiguous_transport")
        return copy.deepcopy(self.mail if method == "buddy-mail.claim_bag_attachment" else self.current)


class TownTests(unittest.TestCase):
    def setUp(self):
        self.bridge = mock.patch.object(m.destination_policy.runtime,'Bridge',side_effect=bridge_factory(m.destination_policy.runtime.Bridge))
        self.bridge.start();self.addCleanup(self.bridge.stop)
        self.temp = tempfile.TemporaryDirectory(dir=Path(tempfile.gettempdir()).resolve())
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name)/"private"/"town.json"
        self.rpc = FakeRPC()

    def worker(self, action, **kwargs):
        with m.Journal(self.path, URL, 6) as journal:
            worker = m.Worker(self.rpc, 6, journal, sleep=lambda _: None)
            return getattr(worker, action)(**kwargs)

    def test_accepted_lua_changes_exact_attachment_order_on_actual_claim(self):
        self.assertEqual(self.worker('claim_one', mailbox_guid=MAILBOX)['phase'], 'confirmed')
        self.assertEqual(self.rpc.mutations()[0][1]['attachmentIndex'], 0)
        self.rpc, self.path = FakeRPC(), self.path.with_name('other-attachment.json')
        source = service_source()
        needle = 'candidate.attachmentIndex<selected.attachmentIndex'
        self.assertEqual(source.count(needle), 1)
        self.rpc.policy_source = source.replace(needle, 'candidate.attachmentIndex>selected.attachmentIndex')
        self.assertEqual(self.worker('claim_one', mailbox_guid=MAILBOX)['phase'], 'confirmed')
        request = self.rpc.mutations()[0][1]
        self.assertEqual((request['mailId'], request['attachmentIndex'], request['itemGuidLow']), (7, 3, 2003))
        row = json.loads(self.path.read_text())['attempts'][0]
        self.assertEqual(row['policyBinding'], row['choicePolicyBinding'])

    def test_all_200_eligible_attachments_reach_real_lua_without_python_truncation(self):
        self.rpc.mail['listRevision'] = '18446744073709551615'
        self.rpc.mail['bagMails'] = [dict(mailId=mid, attachments=[dict(attachmentIndex=index,
            itemGuidLow=4294967295 if (mid, index) == (1, 0) else 2000+mid*4+index,
            entry=41599, count=1) for index in (3, 2, 1, 0)]) for mid in range(50, 0, -1)]
        actual = m.destination_policy.inventory_maintenance
        with mock.patch.object(m.destination_policy, 'inventory_maintenance', wraps=actual) as planner:
            self.assertEqual(self.worker('claim_one', mailbox_guid=MAILBOX)['phase'], 'confirmed')
        facts = planner.call_args.args[2]
        self.assertEqual(len(facts['candidates']), 200)
        self.assertEqual(facts['listRevision'], '18446744073709551615')
        self.assertEqual(facts['mailboxGuid'], str(MAILBOX))
        request = self.rpc.mutations()[0][1]
        self.assertEqual((request['mailId'], request['attachmentIndex'], request['itemGuidLow']), (1, 0, 4294967295))
        self.assertEqual(request['listRevision'], '18446744073709551615')

    def test_unavailable_or_invented_lua_attachment_has_no_python_min_fallback(self):
        choices = ('{}', '{choice={kind="no_attachment"}}',
            '{choice={kind="claim_attachment",mailId=999,attachmentIndex=0,itemGuidLow=2000}}',
            '{choice={kind="claim_attachment",mailId=7,attachmentIndex=false,itemGuidLow=2000}}')
        for index, choice in enumerate(choices):
            self.rpc, self.path = FakeRPC(), self.path.with_name('bad-choice-'+str(index)+'.json')
            self.rpc.policy_source = 'return {service_destination=function() return '+choice+' end}'
            with self.assertRaises(m.Error): self.worker('claim_one', mailbox_guid=MAILBOX)
            self.assertEqual(self.rpc.mutations(), [])
            if self.path.exists(): self.assertEqual(json.loads(self.path.read_text())['attempts'], [])

    def test_attachment_selection_cannot_adopt_transient_new_policy_then_send_under_old_policy(self):
        self.rpc.current['inventoryBags'][0]['slots'][2] = {'slot': 2}
        self.rpc.current['items'] = self.rpc.current['items'][1:]
        original_source, original_call, selected_new = self.rpc.policy_source, self.rpc.call, [False]
        def call(method, params):
            value = original_call(method, params)
            if method == 'buddy-mail.list': self.rpc.policy_source = original_source+'\n-- transient replacement'
            if method == 'session.policy_source' and self.rpc.policy_source != original_source: selected_new[0] = True
            if method == 'buddy-inventory.state' and selected_new[0]: self.rpc.policy_source = original_source
            return value
        self.rpc.call = call
        self.assertEqual(self.worker('run', mailbox_guid=MAILBOX, claims_only=True)['phase'], 'not_dispatched')
        self.assertTrue(selected_new[0])
        self.assertEqual(self.rpc.policy_source, original_source)
        self.assertEqual(self.rpc.mutations(), [])
        row = json.loads(self.path.read_text())['attempts'][0]
        self.assertNotEqual(row['policyBinding']['revision'], row['choicePolicyBinding']['revision'])
        self.assertEqual(self.worker('reconcile')['phase'], 'not_dispatched')

    def test_attachment_source_rechecked_after_its_own_durable_reservation(self):
        original_save = m.Journal.save
        def save(journal):
            original_save(journal)
            if journal.data['attempts'] and journal.data['attempts'][-1]['phase'] == 'reserved':
                self.rpc.policy_source += '\n-- changed after attachment reservation'
        with mock.patch.object(m.Journal, 'save', save):
            self.assertEqual(self.worker('claim_one', mailbox_guid=MAILBOX)['phase'], 'not_dispatched')
        self.assertEqual(self.rpc.mutations(), [])
        self.assertEqual(self.worker('reconcile')['phase'], 'not_dispatched')

    def test_current_accepted_lua_changes_priority_and_exact_bag_destination(self):
        result = self.worker("run", vendor_guid=VENDOR)
        self.assertEqual(result["phase"], "complete")
        self.assertEqual([method for method, _ in self.rpc.mutations()], ["buddy-inventory.equip", "buddy-inventory.sell"])
        self.rpc, self.path = FakeRPC(), self.path.with_name("different-order.json")
        source = service_source()
        self.assertEqual(source.count('local priorities={"equip","claim","sell"}'), 1)
        self.rpc.policy_source = source.replace('local priorities={"equip","claim","sell"}',
            'local priorities={"sell","equip","claim"}').replace('slot<empty', 'slot>empty')
        result = self.worker("run", vendor_guid=VENDOR)
        self.assertEqual(result["phase"], "complete")
        writes = self.rpc.mutations()
        self.assertEqual([method for method, _ in writes], ["buddy-inventory.sell", "buddy-inventory.equip"])
        self.assertEqual(writes[1][1]["equipmentSlot"], 22)

    def test_missing_or_unhealthy_priority_source_has_no_python_fallback(self):
        for change in ("missing", "faulted", "replacement", "inventory"):
            self.rpc, self.path = FakeRPC(), self.path.with_name(change+"-source.json")
            if change == "missing": self.rpc.policy_source = 'return {service_destination=function() return {} end}'
            original = self.rpc.call
            def call(method, params):
                value = original(method, params)
                if method == 'session.attach':
                    if change == 'faulted': value['policy']['faulted'] = True
                    elif change == 'replacement': value['policy']['generation'] = '2'
                if method == 'buddy-inventory.state' and change == 'inventory' and any(m == 'session.attach' for m, _ in self.rpc.calls):
                    value['inventoryRevision'] = '999'
                return value
            self.rpc.call = call
            with self.assertRaises(m.Error): self.worker('run', vendor_guid=VENDOR)
            self.assertEqual(self.rpc.mutations(), [])
            if self.path.exists(): self.assertEqual(json.loads(self.path.read_text())['attempts'], [])

    def test_selected_source_replacement_after_reservation_is_durable_local_no_send(self):
        original_save = m.Journal.save
        def save(journal):
            original_save(journal)
            if journal.data['attempts'] and journal.data['attempts'][-1]['phase'] == 'reserved':
                self.rpc.policy_source += '\n-- replaced during reservation fsync'
        with mock.patch.object(m.Journal, 'save', save): result = self.worker('run', vendor_guid=VENDOR)
        self.assertEqual(result['phase'], 'not_dispatched')
        self.assertEqual(self.rpc.mutations(), [])
        row = json.loads(self.path.read_text())['attempts'][0]
        self.assertTrue(row['localNotDispatched'])
        self.assertNotIn('receipt', row)
        self.assertEqual(self.worker('reconcile')['phase'], 'not_dispatched')

    def test_source_fault_during_sell_rediscovery_is_checked_before_dispatch(self):
        source = service_source().replace('local priorities={"equip","claim","sell"}',
            'local priorities={"sell","equip","claim"}')
        self.rpc.policy_source = source
        original, discovers = self.rpc.call, []
        def call(method, params):
            value = original(method, params)
            if method == 'buddy-inventory.discover': discovers.append(params)
            if method == 'session.attach' and len(discovers) >= 2: value['policy']['faulted'] = True
            return value
        self.rpc.call = call
        self.assertEqual(self.worker('run', vendor_guid=VENDOR)['phase'], 'not_dispatched')
        self.assertEqual(self.rpc.mutations(), [])

    def test_claim_source_replacement_after_mail_wait_and_list_never_dispatches(self):
        self.rpc.current['inventoryBags'][0]['slots'][2] = {'slot': 2}
        self.rpc.current['items'] = self.rpc.current['items'][1:]
        self.rpc.mail['listAgeMs'] = '0'
        self.rpc.mail['bagMails'][0]['attachments'] = self.rpc.mail['bagMails'][0]['attachments'][:1]
        original = self.rpc.call
        def call(method, params):
            value = original(method, params)
            if method == 'buddy-mail.list': self.rpc.policy_source += '\n-- replaced after the mail wait/list'
            return value
        self.rpc.call = call
        result = self.worker('run', mailbox_guid=MAILBOX, claims_only=True)
        self.assertEqual(result['phase'], 'not_dispatched')
        self.assertEqual(self.rpc.mutations(), [])
        self.assertEqual(self.worker('reconcile')['phase'], 'not_dispatched')
        first = json.loads(self.path.read_text())['attempts'][0]
        self.rpc.call, self.rpc.policy_source = original, service_source()
        self.assertEqual(self.worker('run', mailbox_guid=MAILBOX, claims_only=True)['phase'], 'complete')
        claims = [request for method, request in self.rpc.mutations() if method == 'buddy-mail.claim_bag_attachment']
        self.assertEqual(len(claims), 1)
        self.assertEqual(claims[0]['itemGuidLow'], first['request']['itemGuidLow'])
        self.assertNotEqual(claims[0]['requestId'], first['requestId'])
        self.assertEqual(json.loads(self.path.read_text())['attempts'][0], first)

    def test_local_refusal_save_failure_retains_reservation_without_sending(self):
        original_save = m.Journal.save
        def save(journal):
            row = journal.data['attempts'][-1] if journal.data['attempts'] else {}
            if row.get('phase') == 'not_dispatched': raise OSError('full disk')
            original_save(journal)
            if row.get('phase') == 'reserved': self.rpc.policy_source += '\n-- source changed after durable reservation'
        with mock.patch.object(m.Journal, 'save', save), self.assertRaises(OSError):
            self.worker('run', vendor_guid=VENDOR)
        self.assertEqual(self.rpc.mutations(), [])
        self.assertEqual(json.loads(self.path.read_text())['attempts'][0]['phase'], 'reserved')
        self.worker('run', vendor_guid=VENDOR)
        self.assertEqual(self.rpc.mutations(), [])

    def test_pending_unknown_precedes_policy_and_cannot_be_replayed(self):
        self.rpc.after = lambda _, op: op.update(status='OUTCOME_UNCONFIRMED', inventoryConfirmed=False, leaseReleased=False)
        self.rpc.ambiguous = True
        self.assertEqual(self.mutate()['phase'], 'transport_unknown')
        original = json.loads(self.path.read_text())['attempts'][0]
        self.rpc.policy_source = 'return {}'
        result = self.worker('run', vendor_guid=VENDOR)
        self.assertEqual(result['phase'], 'outcome_unconfirmed')
        self.assertEqual(len(self.rpc.mutations()), 1)
        self.assertFalse(any(method == 'session.policy_source' for method, _ in self.rpc.calls))
        self.assertEqual(json.loads(self.path.read_text())['attempts'][0]['request'], original['request'])

    def mutate(self, kind="sell", **kw):
        opts = {"source_bag": 0, "source_slot": 3, "item_guid": str(JUNK), "vendor_guid": VENDOR}
        if kind == "equip":
            opts.update(source_slot=2, item_guid=str(POLE_BAG), equipment_slot=19)
        if kind == "move":
            opts.update(destination_bag=0, destination_slot=5)
        return self.worker("mutation", kind=kind, **{**opts, **kw})

    def test_intent_durable_before_single_sell_and_actual_credit_proof(self):
        def check(method, req):
            saved = json.loads(self.path.read_text())
            self.assertEqual(saved["attempts"][-1]["request"], req)
            self.assertEqual(saved["attempts"][-1]["phase"], "reserved")
            self.assertEqual(self.path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(self.path.parent.stat().st_mode & 0o777, 0o700)
        self.rpc.before = check
        self.assertEqual(self.mutate()["phase"], "confirmed")
        self.assertEqual(len(self.rpc.mutations()), 1)
        req = self.rpc.mutations()[0][1]
        self.assertEqual(req["source"], {"bag": 0, "slot": 3, "itemGuid": str(JUNK), "entry": 7070, "stackCount": 2})
        self.assertEqual((req["expectedUnitSellPriceCopper"], req["expectedCopperRevision"]), (4, "7"))
        self.assertEqual(self.rpc.current["copper"], 58)

    def test_ambiguous_response_reconciles_without_resend(self):
        self.rpc.ambiguous = True
        self.assertEqual(self.mutate()["phase"], "transport_unknown")
        self.assertEqual(self.mutate()["phase"], "confirmed")
        self.assertEqual(len(self.rpc.mutations()), 1)

    def test_missing_or_incomplete_receipt_never_authorizes_second_operation(self):
        self.rpc.after = lambda _, op: op.update(inventoryConfirmed=False)
        self.assertEqual(self.mutate()["phase"], "outcome_unconfirmed")
        self.rpc.current.pop("operation")
        self.assertEqual(self.mutate()["phase"], "receipt_mismatch")
        self.assertEqual(len(self.rpc.mutations()), 1)

    def test_same_session_and_body_required_even_after_confirmed_operation(self):
        self.mutate()
        self.rpc.current["sessionId"] = "body2"
        with self.assertRaisesRegex(m.Error, "session_changed"):
            self.mutate("equip")
        self.assertEqual(len(self.rpc.mutations()), 1)
        self.rpc.current["selfGuid"] = "7"
        with self.assertRaises(m.Error):
            self.worker("reconcile")

    def test_sell_confirmation_requires_exact_all_receipts(self):
        for change in ({"copperConfirmed": False}, {"leaseReleased": False}, {"expectedCreditCopper": 4},
                       {"baselineCopperRevision": "8"}, {"observedInventoryRevision": "11"}, {"source": {"bag": 0, "slot": 3, "itemGuid": str(JUNK), "entry": 7070, "stackCount": 1}}):
            with self.subTest(change=change):
                self.setUp()
                self.rpc.after = lambda _, op, change=change: op.update(change)
                self.assertNotEqual(self.mutate()["phase"], "confirmed")
                self.mutate()
                self.assertEqual(len(self.rpc.mutations()), 1)

    def test_sale_uses_service_full_predicate_positive_price_and_actual_vendor(self):
        for change in ({"safeJunkSaleEligible": False}, {"unitSellPriceCopper": 0}, {"metadataKnown": False}):
            with self.subTest(change=change):
                self.setUp(); self.rpc.current["items"][1].update(change)
                with self.assertRaises(m.Error):
                    self.mutate()
                self.assertEqual(self.rpc.mutations(), [])
        self.setUp(); self.rpc.vendor = False
        with self.assertRaises(m.Error):
            self.mutate()
        self.assertEqual(self.rpc.mutations(), [])

    def test_equip_only_exact_owned_bag_to_empty_equipment_slot(self):
        self.assertEqual(self.mutate("equip")["phase"], "confirmed")
        self.assertEqual(self.rpc.mutations()[0][1]["equipmentSlot"], 19)
        self.assertEqual(self.rpc.current["inventoryBags"][1]["containerGuid"], str(POLE_BAG))
        for opts in ({"equipment_slot": 18}, {"equipment_slot": 23}, {"item_guid": str(JUNK)}, {"source_slot": 1}):
            self.setUp()
            with self.assertRaises(m.Error):
                self.mutate("equip", **opts)
            self.assertEqual(self.rpc.mutations(), [])

    def test_move_exact_stack_to_known_empty_general_slot(self):
        self.assertEqual(self.mutate("move")["phase"], "confirmed")
        self.assertEqual(self.rpc.current["inventoryBags"][0]["slots"][5]["itemGuid"], str(JUNK))
        self.setUp()
        with self.assertRaises(m.Error):
            self.mutate("move", destination_slot=2)
        self.assertEqual(self.rpc.mutations(), [])

    def test_unknown_duplicate_partial_and_aliasing_inventory_never_sends(self):
        changes = [lambda s: s.update(inventoryKnown=False), lambda s: s.update(inventoryRevision="0"),
                   lambda s: s["inventoryBags"].pop(), lambda s: s["inventoryBags"][4].update(bag=3),
                   lambda s: s["inventoryBags"][0]["slots"].pop(),
                   lambda s: s["inventoryBags"][0]["slots"][1].update(itemGuid=str(JUNK)),
                   lambda s: s["inventoryBags"][1].update(containerGuid=str(JUNK), entry=41599, metadataKnown=True, slotCount=1, slots=[{"bag": 1}])]
        for change in changes:
            self.setUp(); change(self.rpc.current)
            with self.assertRaises(m.Error):
                self.mutate()
            self.assertEqual(self.rpc.mutations(), [])

    def test_claim_exact_attachment_durable_and_equip_without_duplicates(self):
        self.rpc.current["inventoryBags"][0]["slots"][2] = {"slot": 2}
        self.rpc.current["items"] = self.rpc.current["items"][1:]
        self.rpc.before = lambda _, req: self.assertEqual(json.loads(self.path.read_text())["attempts"][-1]["request"], req)
        result = self.worker("run", mailbox_guid=MAILBOX, claims_only=True)
        self.assertEqual(result["phase"], "complete")
        self.assertEqual(result["operations"], {"claim": 4, "equip": 4, "sell": 0})
        self.assertEqual(len(self.rpc.mutations()), 8)
        self.assertEqual(self.worker("run", mailbox_guid=MAILBOX, claims_only=True)["phase"], "complete")
        self.assertEqual(len(self.rpc.mutations()), 8)

    def test_four_claims_wait_for_list_throttle_and_refresh_compacted_indices(self):
        clock, sleeps, listed, claims = [100.0], [], [], []
        self.rpc.current["inventoryBags"][0]["slots"][2] = {"slot": 2}
        self.rpc.current["items"] = self.rpc.current["items"][1:]
        original = self.rpc.call
        last_list = [clock[0]-.4]

        def call(method, params):
            if method == "buddy-mail.state":
                self.rpc.mail["listAgeMs"] = str(int(round((clock[0]-last_list[0])*1000)))
            elif method == "buddy-mail.list":
                if clock[0]-last_list[0] < 1:
                    raise m.Error("ResourceExhausted_mail_list_request_throttled")
                last_list[0] = clock[0]
                listed.append(clock[0])
                self.rpc.mail.update(listAgeMs="0", listRevision=str(int(self.rpc.mail["listRevision"])+1))
                # A fresh server list compacts indices after each attachment is
                # removed. Cached pre-claim indices are not valid for the next.
                for row in self.rpc.mail["bagMails"]:
                    for index, attachment in enumerate(row["attachments"]):
                        attachment["attachmentIndex"] = index
            elif method == "buddy-mail.claim_bag_attachment":
                attachment = self.rpc.mail["bagMails"][0]["attachments"][0]
                self.assertEqual(params["attachmentIndex"], 0)
                self.assertEqual(params["itemGuidLow"], attachment["itemGuidLow"])
                self.assertEqual(params["listRevision"], self.rpc.mail["listRevision"])
                self.assertEqual(params["expectedInventoryRevision"], self.rpc.current["inventoryRevision"])
                self.assertEqual(len(listed), len(claims)+1)
                claims.append(params["requestId"])
            return original(method, params)

        def sleep(delay):
            self.assertGreater(delay, 0)
            self.assertLessEqual(delay, 1.1)
            sleeps.append(delay)
            clock[0] += delay
            # A normal inventory observation may arrive during the wait. It
            # must precede, rather than invalidate, the claim's new baseline.
            self.rpc.current["inventoryRevision"] = str(int(self.rpc.current["inventoryRevision"])+1)
            self.rpc.current["revision"] = str(int(self.rpc.current["revision"])+1)

        self.rpc.call = call
        with m.Journal(self.path, URL, 6) as journal:
            worker = m.Worker(self.rpc, 6, journal, now=lambda: clock[0], sleep=sleep)
            result = worker.run(mailbox_guid=MAILBOX, claims_only=True)
        self.assertEqual(result["phase"], "complete")
        self.assertEqual(result["operations"], {"claim": 4, "equip": 4, "sell": 0})
        self.assertEqual((len(listed), len(claims), len(set(claims)), len(sleeps)), (4, 4, 4, 4))
        self.assertAlmostEqual(sleeps[0], .65)
        self.assertTrue(all(b-a >= 1 for a, b in zip(listed, listed[1:])))
        self.assertEqual(sum(b.get("slotCount", 0) for b in self.rpc.current["inventoryBags"]), 96)
        self.assertEqual(len(self.rpc.mutations()), 8)

    def test_claim_ack_inventory_identity_and_session_required(self):
        for change in ({"serverAcknowledged": False}, {"leaseReleased": False}, {"entry": 7005}, {"count": 2}, {"itemGuid": str(JUNK)}):
            self.setUp(); self.rpc.after = lambda _, op, change=change: op.update(change)
            result = self.worker("claim_one", mailbox_guid=MAILBOX)
            self.assertNotEqual(result["phase"], "confirmed")
            self.worker("claim_one", mailbox_guid=MAILBOX)
            self.assertEqual(len(self.rpc.mutations()), 1)

    def test_claim_receipt_accepts_protojson_omitted_zero_index(self):
        self.rpc.after = lambda _, op: op["request"].pop("attachmentIndex")
        self.assertEqual(self.worker("claim_one", mailbox_guid=MAILBOX)["phase"], "confirmed")

    def test_run_sells_at_most_160_exact_safe_stacks(self):
        state = self.rpc.current
        state["items"] = []
        for b, bag in enumerate(state["inventoryBags"]):
            count = 16 if b == 0 else 36
            if b:
                bag.update(containerGuid=str(0x400000000000f000+b), metadataKnown=True, entry=4496)
            bag.update(slotCount=count, slots=[])
            for slot in range(count):
                item = {"bag": b, "slot": slot, "itemGuid": str(0x4000000000010000+b*100+slot), "entry": 7070,
                        "stackCount": 1, "metadataKnown": True}
                bag["slots"].append(item)
                state["items"].append({"item": m.ref(item), "metadataKnown": True, "safeJunkSaleEligible": True, "unitSellPriceCopper": 1})
        result = self.worker("run")
        self.assertEqual(result["operations"], {"claim": 0, "equip": 0, "sell": 160})
        self.assertEqual(len(self.rpc.mutations()), 160)
        self.assertLess(self.path.stat().st_size, 1048576)

    def test_bag_equipment_requires_bounded_actual_capacity_and_complete_slots(self):
        for size in (0, 37):
            with self.subTest(size=size):
                self.setUp(); self.rpc.current["items"][0]["containerSlots"] = size
                with self.assertRaises(m.Error):
                    self.mutate("equip")
                self.assertEqual(self.rpc.mutations(), [])
        self.setUp()
        self.rpc.current["inventoryBags"][1].update(containerGuid=str(0x4000000000000999), metadataKnown=True,
            entry=41599, slotCount=8, slots=[{"bag": 1}])
        with self.assertRaises(m.Error):
            self.mutate()
        self.assertEqual(self.rpc.mutations(), [])


    def test_two_eight_slot_satchel_equips_use_actual_template_and_persist_receipts(self):
        self.rpc.current["selfGuid"] = "8"
        self.rpc.current["inventoryBags"][0]["slots"][2]["entry"] = 4498
        first = self.rpc.current["items"][0]
        first.update(containerSlots=8); first["item"]["entry"] = 4498
        second = {"slot": 4, "itemGuid": str(POLE_BAG+8), "entry": 4498, "stackCount": 1, "metadataKnown": True}
        self.rpc.current["inventoryBags"][0]["slots"][4] = second
        self.rpc.current["items"].append({"item": m.ref(second), "metadataKnown": True, "normalBag": True, "containerSlots": 8})
        with m.Journal(self.path, URL, 8) as journal:
            worker = m.Worker(self.rpc, 8, journal)
            for slot, guid, destination in ((2, POLE_BAG, 19), (4, POLE_BAG+8, 20)):
                result = worker.mutation("equip", source_bag=0, source_slot=slot, item_guid=str(guid), equipment_slot=destination)
                self.assertEqual(result["phase"], "confirmed")
            self.assertEqual([r["bagTemplate"] for r in journal.data["attempts"]], [{"entry":4498,"containerSlots":8}]*2)
        with m.Journal(self.path, URL, 8) as journal:
            self.assertEqual(len(journal.data["attempts"]), 2)
        bags, slots = m.layout(self.rpc.current)
        self.assertEqual(sum(b.get("slotCount", 0) for b in bags.values()), 32)
        self.assertEqual([(bags[b]["entry"], bags[b]["slotCount"]) for b in (1,2)], [(4498,8),(4498,8)])
        self.assertEqual(len(self.rpc.mutations()), 2)

    def test_generic_bag_receipt_wrong_entry_or_capacity_stays_unconfirmed_without_replay(self):
        for change in (lambda b: b.update(entry=41599), lambda b: b.update(slotCount=7, slots=[{"bag":1,"slot":i} for i in range(7)])):
            with self.subTest(change=change):
                self.setUp()
                self.rpc.current["inventoryBags"][0]["slots"][2]["entry"] = 4498
                self.rpc.current["items"][0]["item"]["entry"] = 4498
                self.rpc.current["items"][0]["containerSlots"] = 8
                self.rpc.after = lambda method, request: change(self.rpc.current["inventoryBags"][1])
                self.assertEqual(self.mutate("equip")["phase"], "outcome_unconfirmed")
                self.assertEqual(len(self.rpc.mutations()), 1)
                self.worker("reconcile")
                self.assertEqual(len(self.rpc.mutations()), 1)

    def test_nonqualified_bag_or_occupied_slot_never_dispatches(self):
        for change in (lambda s: s["items"][0].update(normalBag=False), lambda s: s["inventoryBags"][1].update(containerGuid=str(POLE_BAG+4),entry=4498,metadataKnown=True,slotCount=8,slots=[{"bag":1,"slot":i} for i in range(8)])):
            with self.subTest(change=change):
                self.setUp(); change(self.rpc.current)
                with self.assertRaises(m.Error): self.mutate("equip")
                self.assertEqual(self.rpc.mutations(), [])

    def test_pending_inventory_blocks_mail_and_discovery_cannot_replace_ready_guard(self):
        for field in ("active", "closing", "unknown"):
            self.setUp()
            if field == "unknown":
                self.rpc.current["operation"] = {"kind": "sell", "status": "OUTCOME_UNCONFIRMED"}
            else:
                self.rpc.current[field] = True
            with self.assertRaises(m.Error):
                self.worker("claim_one", mailbox_guid=MAILBOX)
            self.assertEqual(self.rpc.mutations(), [])
        self.setUp()
        call = self.rpc.call
        def discovery_busy(method, params):
            response = call(method, params)
            if method == "buddy-inventory.discover":
                response["state"]["active"] = True
            return response
        self.rpc.call = discovery_busy
        with self.assertRaises(m.Error):
            self.mutate()
        self.assertEqual(self.rpc.mutations(), [])

    def test_duplicate_attachment_or_unusable_capacity_never_claims(self):
        self.rpc.mail["bagMails"][0]["attachments"][1]["itemGuidLow"] = 2000
        with self.assertRaises(m.Error):
            self.worker("claim_one", mailbox_guid=MAILBOX)
        self.assertEqual(self.rpc.mutations(), [])
        self.setUp()
        for slot, item in enumerate(self.rpc.current["inventoryBags"][0]["slots"]):
            item.update(itemGuid=str(0x4000000000001000+slot), entry=7070, stackCount=1, metadataKnown=True)
        self.rpc.current["items"] = []
        result = self.worker("run", mailbox_guid=MAILBOX, claims_only=True)
        self.assertEqual(result["phase"], "blocked")
        self.assertEqual(self.rpc.mutations(), [])

    def test_terminal_journal_missing_confirmation_proof_is_rejected(self):
        self.mutate()
        saved = json.loads(self.path.read_text())
        saved["attempts"][0]["receipt"]["copperConfirmed"] = False
        self.path.write_text(json.dumps(saved))
        with self.assertRaises(m.Error):
            self.mutate("equip")
        self.assertEqual(len(self.rpc.mutations()), 1)

    def test_run_is_finite_and_stops_on_first_ambiguous_result(self):
        self.rpc.ambiguous = True
        result = self.worker("run")
        self.assertEqual(result["phase"], "transport_unknown")
        self.assertEqual(len(self.rpc.mutations()), 1)
        self.rpc.ambiguous = False
        result = self.worker("run")
        self.assertEqual(result["phase"], "complete")
        self.assertEqual(len(self.rpc.mutations()), 2)
        self.assertEqual([n for n, _ in self.rpc.mutations()], ["buddy-inventory.equip", "buddy-inventory.sell"])
        self.assertEqual(self.worker("run")["phase"], "complete")
        self.assertEqual(len(self.rpc.mutations()), 2)

    def test_run_does_not_travel_when_no_vendor_is_in_range(self):
        self.rpc.vendor = False
        result = self.worker("run")
        self.assertEqual(result["phase"], "blocked")
        self.assertEqual([n for n, _ in self.rpc.mutations()], ["buddy-inventory.equip"])
        self.assertFalse(any("travel" in n for n, _ in self.rpc.calls))

    def test_private_journal_lock_permissions_identity_and_symlink(self):
        self.mutate()
        with m.Journal(self.path, URL, 6):
            with self.assertRaises(OSError):
                with m.Journal(self.path, URL, 6):
                    pass
        os.chmod(self.path, 0o644)
        with self.assertRaises(m.Error):
            with m.Journal(self.path, URL, 6):
                pass
        os.chmod(self.path, 0o600)
        with self.assertRaises(m.Error):
            with m.Journal(self.path, URL, 7):
                pass
        alias = self.path.with_name("alias.json"); alias.symlink_to(self.path)
        with self.assertRaises(OSError):
            with m.Journal(alias, URL, 6):
                pass

    def test_rpc_strict_loopback_methods_json_and_redirect_boundary(self):
        for url in ("https://127.0.0.1:8191/rpc", "http://localhost:8191/rpc", "http://127.0.0.1:8191/rpc?x=1"):
            with self.assertRaises(m.Error):
                m.RPC(url)
        for raw in ('{"a":1,"a":2}', '{"x":NaN}'):
            with self.assertRaises(ValueError):
                m.base.decode_json(raw)
        with self.assertRaises(m.Error):
            m.base.NoRedirect().redirect_request(None, None, None, None, None, None)
        rpc = m.RPC(URL)
        with self.assertRaises(m.Error):
            rpc.call("session.logout", {})

    def test_optional_account_label_is_local_identity_only(self):
        with m.Journal(self.path, URL, 6, "local-note") as journal:
            self.assertEqual(journal.data["localAccountLabel"], "local-note")
            m.Worker(self.rpc, 6, journal).mutation("sell", source_bag=0, source_slot=3, item_guid=str(JUNK), vendor_guid=VENDOR)
        with self.assertRaises(m.Error):
            with m.Journal(self.path, URL, 6, "other-note"):
                pass


if __name__ == "__main__":
    unittest.main()
