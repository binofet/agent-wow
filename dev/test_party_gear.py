"""Exact normal gear swaps, simulated RPC and private journals only."""
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
            equipped=self.current['equipment'][req['equipmentSlot']]
            old_entry=equipped['entry']
            self.current['inventoryBags'][0]['slots'][2].update(itemGuid=str(OLD),entry=old_entry)
            equipped.update(itemGuid=str(NEW),entry=req['source']['entry'])
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
    def test_direct_gear_requires_buff_control_hold_too(self):
        original=self.rpc.call
        def call(method,params):
            if method=='buddy-world.snapshot':return {'self':dict(guid='5',present=True,mapKnown=True,aliveKnown=True,alive=True,
                unitFlagsKnown=True,movementFlagsKnown=True)}
            if method=='buddy-actions.status':return dict(buffing=True,position=dict(known=True,mapKnown=True,x=0.,y=0.,z=0.))
            return original(method,params)
        self.rpc.call=call
        with m.Journal(self.path,self.rpc.url,5) as journal:
            with self.assertRaises(m.Error):self.worker(journal).safe()
        self.assertFalse(self.writes())
    def review_fixture(self,journal):
        old=journal.data['attempts'][0]['request']['sessionId'];new='reviewed-new-body';now=m.time.time()
        baseline=initial();baseline['sessionId']=old
        current=copy.deepcopy(baseline);current['sessionId']=new
        own=dict(guid='5',present=True,mapKnown=True,mapId=0,positionKnown=True,positionRevision='1',x=0.,y=0.,z=0.,
            aliveKnown=True,alive=True,unitFlagsKnown=True,movementFlagsKnown=True)
        world=dict(self=own,inventoryKnown=True,inventoryRevision=current['inventoryRevision'],inventoryBags=copy.deepcopy(current['inventoryBags']),
            selfEquipmentKnown=True,selfEquipmentRevision=current['equipmentRevision'],selfEquipment=copy.deepcopy(current['equipment']))
        docs=dict(completedPass=dict(at=now-5,finishedAt=now-4,phase='finished',actors=[dict(guid=5,sessionId=old,afterInventory=baseline)]),
            lifecycle=dict(at=now-3,finishedAt=now-2,phase='all_three_ready',actors=[dict(guid=5,oldSession=old,newSession=new,
                stop=dict(returncode=0,stdout='Stopped: PID 12345; server logout completion and process exit verified.\nLog: /private/client.log\n'),
                before=dict(party=dict(sessionId=old)),newBaseline=dict(party=dict(sessionId=new)))]),
            current=dict(at=now-1,guid=5,sessionId=new,partyBefore=dict(sessionId=new),partyAfter=dict(sessionId=new),
                inventory=current,world=world,actions=dict(position=dict(known=True,mapKnown=True,mapId=0,x=0.,y=0.,z=0.))))
        artifacts={}
        def write():
            for kind,value in docs.items():
                path=self.path.parent/(kind+'.json');raw=m.json.dumps(value).encode();path.write_bytes(raw);path.chmod(0o600)
                artifacts[kind]=dict(path=str(path),sha256=m.hashlib.sha256(raw).hexdigest())
        write();return docs,artifacts,write
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

    def test_backend_qualified_mainhand_uses_same_exact_swap_and_durable_protection(self):
        s=self.rpc.current
        s['equipment'][6]=dict(slot=6,slotKnown=True)
        s['equipment'][15]=dict(slot=15,slotKnown=True,itemGuid=str(OLD),entry=2092,stackCount=1,metadataKnown=True)
        s['equipment'][16]=dict(slot=16,slotKnown=True,itemGuid=str(OLD+1),entry=50055,stackCount=1,metadataKnown=True)
        s['inventoryBags'][0]['slots'][2]['entry']=2224
        row=s['items'][0];row.update(name='Militia Dagger',gearEquipmentSlot=15,safeJunkSaleEligible=False)
        row['item']['entry']=2224
        other_hand=copy.deepcopy(s['equipment'][16])
        with m.Journal(self.path,self.rpc.url,5) as j:
            result=self.worker(j).run()
            self.assertEqual(result['newConfirmedOperations'],1)
            self.assertEqual(result['equippedItems'][0]['name'],'Militia Dagger')
            self.assertEqual(self.writes()[0]['equipmentSlot'],15)
            self.assertEqual(s['equipment'][15]['itemGuid'],str(NEW))
            self.assertEqual(s['inventoryBags'][0]['slots'][2]['itemGuid'],str(OLD))
            self.assertEqual(s['equipment'][16],other_hand)
        with m.Journal(self.path,self.rpc.url,5) as j:
            self.worker(j).run();self.assertEqual(len(self.writes()),1)

    def test_mainhand_requires_backend_upgrade_and_unsupported_offhand_is_never_dispatched(self):
        for mode in ('ineligible','not_upgrade','offhand'):
            with self.subTest(mode=mode),tempfile.TemporaryDirectory() as temp:
                self.rpc=RPC();row=self.rpc.current['items'][0]
                row['gearEquipmentSlot']=16 if mode=='offhand' else 15
                if mode=='ineligible':row['gearEquipEligible']=False
                if mode=='not_upgrade':row['conservativeUpgrade']=False
                with m.Journal(Path(temp).resolve()/'gear.json',self.rpc.url,5) as j:
                    try:self.worker(j).run()
                    except m.Error:pass
                self.assertFalse(self.writes())

    def test_startup_metadata_revision_change_before_dispatch_does_not_reserve_or_consume_item(self):
        original=self.rpc.call;reads=[0]
        def call(method,req):
            if method=='buddy-inventory.state':
                reads[0]+=1
                if reads[0]==4:self.rpc.current['revision']='19'  # Only template metadata arrived.
            return original(method,req)
        self.rpc.call=call
        with m.Journal(self.path,self.rpc.url,5) as j:
            result=self.worker(j).run()
            self.assertEqual(result['phase'],'blocked')
            self.assertFalse(j.data['attempts']);self.assertFalse(self.writes())
            self.assertNotIn('operation',self.rpc.current)
        # A fresh pass may quote the same still-owned candidate; there is no
        # fictitious ambiguous mutation to reconcile or old request to replay.
        with m.Journal(self.path,self.rpc.url,5) as j:
            result=self.worker(j).run()
            self.assertEqual(result['newConfirmedOperations'],1)
            self.assertEqual(len(self.writes()),1)
            self.assertEqual(self.writes()[0]['expectedRevision'],'19')

    def test_final_read_only_safety_or_candidate_failure_is_definite_blocked_not_pending(self):
        for mode in ('guard','candidate','body','read_error','active'):
            with self.subTest(mode=mode),tempfile.TemporaryDirectory() as temp:
                self.rpc=RPC();original=self.rpc.call;reads=[0];guards=[0]
                def call(method,req):
                    if method=='buddy-inventory.state':
                        reads[0]+=1
                        if reads[0]==4:
                            if mode=='candidate':self.rpc.current['items'][0]['conservativeUpgrade']=False
                            elif mode=='body':self.rpc.current['sessionId']='replacement'
                            elif mode=='read_error':raise m.Error('read-only response unavailable')
                            elif mode=='active':self.rpc.current['active']=True
                    return original(method,req)
                def guard():
                    guards[0]+=1
                    if mode=='guard' and guards[0]==3:raise m.Error('human intent revoked during final guard')
                self.rpc.call=call
                with m.Journal(Path(temp).resolve()/'gear.json',self.rpc.url,5) as j:
                    self.rpc.journal=j
                    result=m.Worker(self.rpc,5,j,guard=guard).run()
                    self.assertEqual(result['phase'],'blocked')  # Town's existing certain cleanup/restore path.
                    self.assertFalse(j.data['attempts']);self.assertFalse(self.writes())

    def test_stop_or_deadline_after_durable_reservation_records_no_dispatch_and_can_reselect(self):
        for mode in ('stop','deadline'):
            with self.subTest(mode=mode),tempfile.TemporaryDirectory() as temp:
                self.rpc=RPC();clock=[0.];stop=[False];triggered=[False];path=Path(temp).resolve()/'gear.json'
                with m.Journal(path,self.rpc.url,5) as j:
                    save=j.save
                    def save_then_interrupt():
                        save()
                        if j.data['attempts'] and j.data['attempts'][-1]['phase']=='reserved' and not triggered[0]:
                            triggered[0]=True
                            if mode=='stop':stop[0]=True
                            else:clock[0]=31
                    j.save=save_then_interrupt
                    result=self.worker(j,stopped=lambda:stop[0],now=lambda:clock[0]).run()
                    self.assertEqual(result['phase'],'blocked');self.assertFalse(self.writes())
                    old=copy.deepcopy(j.data['attempts'][0])
                    self.assertEqual(old['phase'],'not_dispatched')
                    self.assertIs(old['localAbort']['rpcInvoked'],False)
                    self.assertNotIn('receipt',old)
                clock[0]=0.;stop[0]=False
                with m.Journal(path,self.rpc.url,5) as j:
                    result=self.worker(j,stopped=lambda:stop[0],now=lambda:clock[0]).run()
                    self.assertEqual(result['newConfirmedOperations'],1)
                    self.assertEqual(j.data['attempts'][0],old)
                    self.assertNotEqual(self.writes()[0]['requestId'],old['request']['requestId'])
                    self.assertEqual(self.writes()[0]['source'],old['request']['source'])
                    self.assertEqual(len(self.writes()),1)

    def test_local_no_dispatch_label_cannot_hide_an_invoked_or_receipted_request(self):
        for mode in ('missing_proof','invoked','receipt','unknown_reason'):
            with self.subTest(mode=mode),tempfile.TemporaryDirectory() as temp:
                self.rpc=RPC();self.rpc.mode='unknown';path=Path(temp).resolve()/'gear.json'
                with m.Journal(path,self.rpc.url,5) as j:
                    self.worker(j).run();row=j.data['attempts'][0]
                    row.update(phase='not_dispatched',localAbort=dict(reason='stop_before_dispatch',rpcInvoked=False))
                    if mode=='missing_proof':del row['localAbort']
                    elif mode=='invoked':row['localAbort']['rpcInvoked']=True
                    elif mode=='unknown_reason':row['localAbort']['reason']='later_idle_so_assumed_safe'
                    else:row['receipt']['status']='OUTCOME_UNCONFIRMED'
                    with self.assertRaises(m.Error):j.validate()

    def test_reviewed_terminated_body_retirement_preserves_unknown_row_and_allows_only_new_request(self):
        self.rpc.mode='unknown'
        with m.Journal(self.path,self.rpc.url,5) as j:
            self.worker(j).run();prior=copy.deepcopy(j.data['attempts'][0]);docs,artifacts,_=self.review_fixture(j)
            result=j.retire_prior_session(prior['request']['requestId'],artifacts)
            self.assertEqual(result['phase'],'retired_prior_session')
            self.assertEqual(result['retirement']['priorRow'],prior)
            self.assertEqual(result['retirement']['originalPhase'],'unconfirmed')
            self.assertEqual(result['retirement']['outcome'],'UNKNOWN')
            self.assertNotIn('localAbort',result);self.assertEqual(result['receipt'],prior['receipt'])
            self.assertEqual(self.writes(),[prior['request']])  # Review itself issues no RPC.
            self.rpc.current=copy.deepcopy(docs['current']['inventory']);self.rpc.mode='confirmed'
        with m.Journal(self.path,self.rpc.url,5) as j:
            result=self.worker(j).run()
            self.assertEqual(result['newConfirmedOperations'],1)
            self.assertNotEqual(self.writes()[-1]['requestId'],prior['request']['requestId'])
            self.assertEqual(self.writes()[-1]['sessionId'],'reviewed-new-body')
            self.assertEqual(j.data['attempts'][0]['retirement']['priorRow'],prior)

    def test_prior_session_review_refuses_missing_termination_or_changed_owned_state_without_writing(self):
        for mode in ('active','operation','lease','same_session','source_changed','unproven_logout','wrong_old_session',
                     'raw_vector_changed','equipment_changed','missing_metadata','unknown_raw','body_changed','bad_order','not_after_exit','wrong_hash'):
            with self.subTest(mode=mode),tempfile.TemporaryDirectory() as temp:
                self.path=Path(temp).resolve()/'gear.json';self.rpc=RPC();self.rpc.mode='unknown'
                with m.Journal(self.path,self.rpc.url,5) as j:
                    self.worker(j).run();docs,artifacts,write=self.review_fixture(j);before=self.path.read_bytes()
                    cur=docs['current'];inv=cur['inventory'];rollout=docs['lifecycle']['actors'][0]
                    if mode=='active':inv['active']=True
                    elif mode=='operation':inv['operation']=dict(kind='equip_gear',status='OUTCOME_UNCONFIRMED')
                    elif mode=='lease':cur['actions']['interactionOwner']='trade'
                    elif mode=='same_session':cur['sessionId']=rollout['oldSession']
                    elif mode=='source_changed':inv['inventoryBags'][0]['slots'][2]['itemGuid']=str(NEW+100)
                    elif mode=='unproven_logout':rollout['stop']['stdout']='SMSG_LOGOUT_COMPLETE mentioned, exit unknown'
                    elif mode=='wrong_old_session':rollout['oldSession']='foreign-old-body'
                    elif mode=='raw_vector_changed':cur['world']['inventoryBags'][0]['slots'][2]['entry']=80
                    elif mode=='equipment_changed':inv['equipment'][6]['entry']=80
                    elif mode=='missing_metadata':inv['equipment'][6]['metadataKnown']=False
                    elif mode=='unknown_raw':cur['world']['selfEquipmentKnown']=False
                    elif mode=='body_changed':cur['partyAfter']['sessionId']='different-body'
                    elif mode=='bad_order':cur['at']-=60
                    elif mode=='not_after_exit':cur['at']=docs['lifecycle']['finishedAt']
                    write()
                    if mode=='wrong_hash':artifacts['lifecycle']['sha256']='0'*64
                    with self.assertRaises(m.Error):j.retire_prior_session(j.data['attempts'][0]['request']['requestId'],artifacts)
                    self.assertEqual(self.path.read_bytes(),before)
                    self.assertEqual(j.data['attempts'][0]['phase'],'unconfirmed')
                    self.assertEqual(len(self.writes()),1)

    def test_generic_issued_ambiguity_is_not_automatically_retired_on_reconnect(self):
        self.rpc.mode='unknown'
        with m.Journal(self.path,self.rpc.url,5) as j:
            self.worker(j).run();old=copy.deepcopy(j.data['attempts'][0])
            self.rpc.current=initial();self.rpc.current['sessionId']='fresh-body'
            result=self.worker(j).run()
            self.assertEqual(result['phase'],'unconfirmed');self.assertEqual(len(self.writes()),1)
            row=j.data['attempts'][0]
            self.assertEqual(row['phase'],'new_session_requires_review');self.assertNotIn('retirement',row)
            self.assertEqual(row['request'],old['request'])

    def test_ordered_historical_retirement_evidence_survives_later_ordinary_inventory_changes(self):
        self.rpc.mode='unknown'
        with m.Journal(self.path,self.rpc.url,5) as j:
            self.worker(j).run();docs,artifacts,write=self.review_fixture(j)
            for document in docs.values():
                document['at']-=3600
                if 'finishedAt' in document:document['finishedAt']-=3600
            write();historical=copy.deepcopy(docs['current']);old_request=copy.deepcopy(j.data['attempts'][0]['request'])
            self.rpc.current=copy.deepcopy(historical['inventory'])
            empty=next(slot for slot in self.rpc.current['inventoryBags'][0]['slots'] if not int(slot.get('itemGuid',0)))
            empty.update(itemGuid=str(NEW+100),entry=1971,stackCount=1,metadataKnown=True)
            self.rpc.current.update(revision='20',inventoryRevision='2')  # A later normal quest acceptance.
            retired=j.retire_prior_session(old_request['requestId'],artifacts)
            self.assertEqual(retired['retirement']['outcome'],'UNKNOWN')
            self.assertEqual(docs['current'],historical)  # Never freshen or rewrite captured history.
            self.assertEqual(len(self.writes()),1)  # Reviewing history has no RPC side effects.
            self.rpc.mode='confirmed';result=self.worker(j).run()
            self.assertEqual(result['newConfirmedOperations'],1)
            self.assertEqual(self.writes()[-1]['expectedRevision'],'20')
            self.assertEqual(self.writes()[-1]['expectedInventoryRevision'],'2')
            self.assertNotEqual(self.writes()[-1]['requestId'],old_request['requestId'])

    def test_retirement_terminal_validation_preserves_prior_uncertainty_and_session_binding(self):
        self.rpc.mode='unknown'
        with m.Journal(self.path,self.rpc.url,5) as j:
            self.worker(j).run();_,artifacts,_=self.review_fixture(j)
            j.retire_prior_session(j.data['attempts'][0]['request']['requestId'],artifacts)
            valid=copy.deepcopy(j.data['attempts'][0])
            for mode in ('success_claim','same_session','prior_request_changed','original_phase','no_logout','no_artifact'):
                with self.subTest(mode=mode):
                    j.data['attempts'][0]=copy.deepcopy(valid);row=j.data['attempts'][0];review=row['retirement']
                    if mode=='success_claim':review['outcome']='CONFIRMED'
                    elif mode=='same_session':review['newSessionId']=row['request']['sessionId']
                    elif mode=='prior_request_changed':review['priorRow']['request']['requestId']='modified-old-request'
                    elif mode=='original_phase':review['originalPhase']='confirmed'
                    elif mode=='no_logout':review['oldLogoutObserved']=False
                    else:del review['artifacts']['lifecycle']
                    with self.assertRaises(m.Error):j.validate()
            j.data['attempts'][0]=valid

if __name__=='__main__':unittest.main()
