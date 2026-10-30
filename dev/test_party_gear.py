"""Exact normal armor swaps, simulated RPC and private journals only."""
import copy
import importlib.machinery
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

def load(name,file):
    loader=importlib.machinery.SourceFileLoader(name,str(Path(__file__).with_name(file)))
    spec=importlib.util.spec_from_loader(loader.name,loader);m=importlib.util.module_from_spec(spec);loader.exec_module(m);return m
m=load('tested_party_gear','party-gear')
finite=load('gear_existing_fakes','test_town_maintenance.py')
NEW,OLD=0x4000000000000171,0x4000000000000172

def initial():
    s=finite.initial();s['selfGuid']='5';s['equipmentKnown']=True;s['equipmentRevision']='3'
    s['equipment']=[dict(slot=i,slotKnown=True) for i in range(19)]
    s['equipment'][6].update(itemGuid=str(OLD),entry=139,stackCount=1,metadataKnown=True)
    s['inventoryBags'][0]['slots'][2].update(itemGuid=str(NEW),entry=2962,stackCount=1)
    s['items']=[dict(item=m.town.ref(s['inventoryBags'][0]['slots'][2]),metadataKnown=True,name='Burnt Leather Breeches',
        gearEquipEligible=True,conservativeUpgrade=True,gearEquipmentSlot=6,expectedEquippedItemGuid=str(OLD),
        safeJunkSaleEligible=True,unitSellPriceCopper=1)]
    return s

class RPC:
    url='http://127.0.0.1:8088/rpc'
    def __init__(self):self.current,self.calls,self.mode,self.journal=initial(),[],'confirmed',None
    def call(self,method,req):
        self.calls.append((method,copy.deepcopy(req)))
        if method=='buddy-inventory.state':return copy.deepcopy(self.current)
        assert method=='buddy-inventory.equip_gear'
        assert self.journal.data['attempts'][-1]['request']==req
        assert self.journal.data['attempts'][-1]['phase']=='reserved'
        op=dict(requestId=req['requestId'],kind='equip_gear',status='OUTCOME_UNCONFIRMED',source=copy.deepcopy(req['source']),
            equipmentSlot=req['equipmentSlot'],displacedItemGuid=req['expectedEquippedItemGuid'],leaseReleased=False,
            baselineInventoryRevision=req['expectedInventoryRevision'],baselineEquipmentRevision=req['expectedEquipmentRevision'])
        self.current['operation']=op
        if self.mode=='blocked':op.update(status='BLOCKED',leaseReleased=True,transportAttempted=False)
        if self.mode in ('confirmed','lost_reply','wrong_displaced','no_lease','wrong_receipt','no_equipment_proof','false_transport'):
            self.current['inventoryBags'][0]['slots'][2].update(itemGuid=str(OLD),entry=139)
            self.current['equipment'][6].update(itemGuid=str(NEW),entry=2962)
            self.current['inventoryRevision']='12';self.current['equipmentRevision']='4';self.current['revision']='5'
            self.current['items']=[]
            op.update(status='CONFIRMED',inventoryConfirmed=True,equipmentConfirmed=True,leaseReleased=True,
                observedInventoryRevision='12',observedEquipmentRevision='4',transportWritten=self.mode!='false_transport')
            if self.mode=='wrong_displaced':self.current['inventoryBags'][0]['slots'][2].update(itemGuid='0',entry=0,stackCount=0,metadataKnown=False)
            if self.mode=='no_lease':op['leaseReleased']=False
            if self.mode=='wrong_receipt':op['displacedItemGuid']='0'
            if self.mode=='no_equipment_proof':op['equipmentConfirmed']=False
        if self.mode=='lost_reply':raise m.Error('reply lost after actual swap')
        return copy.deepcopy(self.current)

class GearTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup)
        self.path=Path(self.temp.name).resolve()/'gear.json';self.rpc=RPC()
    def worker(self,journal,**kw):
        self.rpc.journal=journal
        return m.Worker(self.rpc,5,journal,guard=lambda:None,**kw)
    def writes(self):return [r for method,r in self.rpc.calls if method=='buddy-inventory.equip_gear']
    def test_conservative_exact_swap_keeps_displaced_pants_and_persists_no_replay(self):
        with m.Journal(self.path,self.rpc.url,5) as j:
            result=self.worker(j).run()
            self.assertEqual((result['phase'],result['newConfirmedOperations']),('complete',1))
            self.assertEqual(result['equippedItems'][0]['name'],'Burnt Leather Breeches')
            self.assertTrue(result['equippedItems'][0]['displacedRetained'])
            self.assertTrue(self.writes()[0]['requireConservativeUpgrade'])
            self.assertEqual(self.writes()[0]['expectedEquippedItemGuid'],str(OLD))
        with m.Journal(self.path,self.rpc.url,5) as j:
            self.worker(j).run();self.assertEqual(len(self.writes()),1)
        self.assertEqual(self.rpc.current['inventoryBags'][0]['slots'][2]['itemGuid'],str(OLD))
    def test_positive_server_proof_survives_lost_reply_or_false_transport_flag(self):
        for mode in ('lost_reply','false_transport'):
            with self.subTest(mode=mode),tempfile.TemporaryDirectory() as temp:
                self.rpc=RPC();self.rpc.mode=mode
                with m.Journal(Path(temp).resolve()/'gear.json',self.rpc.url,5) as j:
                    result=self.worker(j).run();self.assertEqual(result['newConfirmedOperations'],1)
    def test_ambiguous_or_incomplete_swap_never_replays_even_after_restart_or_new_body(self):
        for mode in ('unknown','wrong_displaced','no_lease','wrong_receipt','no_equipment_proof'):
            with self.subTest(mode=mode),tempfile.TemporaryDirectory() as temp:
                self.rpc=RPC();self.rpc.mode=mode;path=Path(temp).resolve()/'gear.json'
                with m.Journal(path,self.rpc.url,5) as j:
                    result=self.worker(j).run();self.assertEqual(result['phase'],'unconfirmed');self.assertFalse(result['equippedItems'])
                with m.Journal(path,self.rpc.url,5) as j:
                    self.worker(j).run();self.assertEqual(len(self.writes()),1)
                    self.rpc.current['sessionId']='replacement'
                    self.worker(j).run();self.assertEqual(len(self.writes()),1)
                    self.assertEqual(j.data['attempts'][0]['phase'],'new_session_requires_review')
    def test_nonupgrade_unknown_equipment_and_wrong_source_do_not_write(self):
        for mode in ('nonupgrade','ineligible','metadata','equipment','partial','wrong_source','wrong_target'):
            with self.subTest(mode=mode),tempfile.TemporaryDirectory() as temp:
                self.rpc=RPC();s=self.rpc.current;item=s['items'][0]
                if mode=='nonupgrade':item['conservativeUpgrade']=False
                elif mode=='ineligible':item['gearEquipEligible']=False
                elif mode=='metadata':item['metadataKnown']=False
                elif mode=='equipment':s['equipmentKnown']=False
                elif mode=='partial':s['equipment'].pop()
                elif mode=='wrong_source':item['item']['entry']=999
                else:item['expectedEquippedItemGuid']='0'
                with m.Journal(Path(temp).resolve()/'gear.json',self.rpc.url,5) as j:
                    try:self.worker(j).run()
                    except m.Error:pass
                self.assertFalse(self.writes())
    def test_identity_private_journal_and_held_canonical_town_lock(self):
        for guid,url in ((3,'http://127.0.0.1:8088/rpc'),(5,'http://127.0.0.1:8086/rpc')):
            with self.assertRaises(m.Error):m.Journal(self.path,url,guid)
        with m.Journal(self.path,self.rpc.url,5) as j:
            self.worker(j).run()
        self.path.chmod(0o644)
        with self.assertRaises(m.Error):
            with m.Journal(self.path,self.rpc.url,5):pass
        path=Path(self.temp.name).resolve()/'canonical.json'
        with m.town.Journal(path,self.rpc.url,5):
            with self.assertRaises(BlockingIOError):
                with m.town.Journal(path,self.rpc.url,5):pass
    def test_guard_revocation_and_late_body_change_before_submission_do_not_write(self):
        for mode in ('revoked','new_body'):
            with self.subTest(mode=mode),tempfile.TemporaryDirectory() as temp:
                self.rpc=RPC();calls=[0]
                def guard():
                    calls[0]+=1
                    if calls[0]==2:
                        if mode=='revoked':raise m.Error('human stop')
                        self.rpc.current['sessionId']='new_body'
                with m.Journal(Path(temp).resolve()/'gear.json',self.rpc.url,5) as j:
                    worker=m.Worker(self.rpc,5,j,guard=guard)
                    try:worker.run()
                    except m.Error:pass
                self.assertFalse(self.writes())

    def test_actual_json_string_guid_blocked_request_is_not_repeated_under_new_ids(self):
        self.rpc.mode='blocked'
        with m.Journal(self.path,self.rpc.url,5) as j:
            self.worker(j).run()
            self.rpc.current['items'][0]['item']['itemGuid']=NEW
            self.worker(j).run()
            self.assertEqual(len(self.writes()),1)
            self.assertEqual(j.data['attempts'][0]['phase'],'blocked')

    def test_durable_displaced_equipment_protection_and_pending_ambiguity_survive_restart(self):
        root=Path(self.temp.name).resolve()
        path=root/'data/autonomy/town-maintenance/5-8088/gear.json'
        with m.Journal(path,self.rpc.url,5) as j:self.worker(j).run()
        with patch.object(m,'ROOT',root):
            pending,protected=m.ledger_facts(5)
            self.assertFalse(pending);self.assertEqual(protected,{OLD})
            with m.Journal(path,self.rpc.url,5) as j:
                j.data['attempts'][0]['phase']='unconfirmed';j.save()
            self.assertTrue(m.ledger_facts(5)[0])

    def test_slow_observation_or_guard_exhausting_budget_cannot_dispatch_a_late_swap(self):
        clock=[0.];guards=[0]
        def guard():
            guards[0]+=1
            if guards[0]==2:clock[0]=31
        with m.Journal(self.path,self.rpc.url,5) as j:
            worker=m.Worker(self.rpc,5,j,guard=guard,now=lambda:clock[0])
            self.assertEqual(worker.run()['phase'],'blocked')
            self.assertFalse(self.writes());self.assertFalse(j.data['attempts'])

if __name__=='__main__':unittest.main()
