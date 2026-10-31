"""Team supervisor tests use only injected public RPC/process/lifecycle fakes."""
import copy
import importlib.machinery
import importlib.util
import json
from pathlib import Path
import tempfile
import time
import unittest
import types
from unittest.mock import patch

loader=importlib.machinery.SourceFileLoader('team_control_tested',str(Path(__file__).with_name('team-control')))
spec=importlib.util.spec_from_loader(loader.name,loader)
m=importlib.util.module_from_spec(spec);loader.exec_module(m)


def initial(guid=1,sid='aw-111'):
    player=dict(guid=str(guid),present=True,mapKnown=True,mapId=0,aliveKnown=True,alive=True,
                healthKnown=True,health=100,maxHealth=100,unitFlagsKnown=True,unitFlags=0,
                movementFlagsKnown=True,movementFlags=0,positionKnown=True,x=0.,y=0.,z=0.)
    state={name:dict(sessionId=sid) for name in m.READS}
    state['actions'].update(selfGuid=str(guid),following=True,healing=guid==1,assisting=True,buffing=True,
        recovery=dict(enabled=True),position=dict(known=True,mapKnown=True,mapId=0,x=0.,y=0.,z=0.))
    state['loot']['enabled']=True;state['trade']['phase']='IDLE'
    state['world']=dict(self=player,owner=dict(player,guid='3',x=10.))
    state['party']=dict(sessionId=sid,group=dict(known=True,inGroup=True,members=[dict(guid=str(guid),self=True,online=True),dict(guid='3',online=True)]))
    state['inventory'].update(selfGuid=str(guid),inventoryKnown=True,equipmentKnown=True,
        inventoryBags=[dict(bag=i,slotKnown=True,contentsKnown=True,slotCount=0,slots=[]) for i in range(5)],
        equipment=[dict(slot=i,slotKnown=True) for i in range(19)])
    state['quests'].update(questLogKnown=True,questLog=[dict(slot=i) for i in range(25)])
    state.update(sessionId=sid,observedAt=time.monotonic());return state


def request(guid=1,seq=1,action='RESET',**fields):
    return dict(dict(addonSession='aabbccddeeff',requestSequence=seq,requestId=f'aabbccddeeff.{seq}',subjectGuid=str(guid),
                action=action,available=True,ageMs=0),**fields)


class FakeRPC:
    def __init__(self,state=None):
        self.scene=state or initial();self.calls=[];self.requests=[request()];self.epoch='SeparateChatEpoch123';self.fail=None
    def call(self,method,params,timeout=2):
        self.calls.append((method,copy.deepcopy(params)))
        if self.fail:self.fail(method,params)
        if method=='buddy-chat.management_requests':
            if params.get('sessionId',self.epoch)!=self.epoch:raise m.Error('Wrong CHAT epoch')
            after=m.number(params.get('afterSequence',0));latest=len(self.requests)
            if after>latest:raise m.Error('Cursor newer than CHAT epoch')
            rows=[dict(r,sequence=i+1) for i,r in enumerate(self.requests) if i+1>after][:params.get('limit',64)]
            return dict(sessionId=self.epoch,selfGuid=self.scene['actions']['selfGuid'],requests=rows,
                        latestSequence=latest,nextSequence=after+len(rows))
        if method=='buddy-chat.poll':return dict(sessionId=self.epoch,nextSequence=0,messages=[])
        if method=='buddy-chat.publish_team_status':return dict(transportWritten=True)
        if method=='buddy-party.state':return copy.deepcopy(self.scene['party'])
        if method=='buddy-world.snapshot':return copy.deepcopy(self.scene['world'])
        if method=='buddy-actions.status':return copy.deepcopy(self.scene['actions'])
        for name in m.READS:
            if method==f'buddy-{name}.state':return copy.deepcopy(self.scene[name])
        for key,rpc,field in m.CONTROLS:
            if method==rpc:
                if key=='recovery':self.scene['actions']['recovery']['enabled']=params['enabled']
                elif key=='loot':self.scene['loot']['enabled']=params['enabled']
                else:self.scene['actions'][field]=params['enabled']
                return copy.deepcopy(self.scene['actions'] if key!='loot' else self.scene['loot'])
        if method=='buddy-actions.acquire_interaction':
            self.scene['actions'].update(interactionOwner='exploration',interactionLeaseRemainingMs=30000,moving=False)
            return dict(token='owned-token',state=copy.deepcopy(self.scene['actions']))
        if method=='buddy-actions.travel_to':
            self.scene['actions']['position'].update({k:params[k] for k in ('x','y','z')})
            return dict(self.scene['actions'],reason='exploration_within_distance')
        if method=='buddy-actions.release_interaction':
            assert params==dict(owner='exploration',token='owned-token')
            self.scene['actions'].update(interactionOwner='',moving=False,traveling=False)
            return copy.deepcopy(self.scene['actions'])
        raise AssertionError(method)


class FakeAdapter:
    def __init__(self,rpc):self.rpc=rpc;self.calls=[];self.stop_bad=None;self.on_start=None
    def status(self):return dict(guid=1,state='trackedReady',fingerprint='a'*64,sessionId=self.rpc.scene['sessionId'],ownedProfile=True,
                                 pid=100 if self.rpc.scene['sessionId']=='aw-111' else 101,startTicks=200 if self.rpc.scene['sessionId']=='aw-111' else 201,coreSha256='known-core',ready=True)
    def call(self,g,op,params):
        self.calls.append((g,op,copy.deepcopy(params)))
        if op=='status':return self.status()
        if op=='stop':return self.stop_bad or dict(guid=g,outcome='stopped',normalLogoutObserved=True,oldPidGone=True,rpcClosed=True,
                                                  sessionContainersGone=True,stopReceiptRef=params['operationId'])
        if op=='start':
            for name in m.READS:self.rpc.scene[name]['sessionId']='aw-222'
            self.rpc.scene['party']['sessionId']='aw-222';self.rpc.scene['sessionId']='aw-222'
            a=self.rpc.scene['actions'];a.update(following=False,healing=False,assisting=False,buffing=False,recovery=dict(enabled=False))
            self.rpc.scene['loot']['enabled']=False
            if self.on_start:self.on_start()
            return self.status()
        raise AssertionError(op)


class FakeControllers:
    def __init__(self):self.calls=[];self.on_stop=None;self.on_restore=None
    def capture(self,g):self.calls.append('capture');return [dict(folder='fixed-owned-brain')]
    def stop(self,rows,reserve,guard):
        guard();reserve('controller_stop',dict(fixed=True));self.calls.append('stop')
        if self.on_stop:self.on_stop()
    def restore(self,rows,reserve,guard):
        guard();reserve('controller_start',dict(fixed=True));self.calls.append('restore')
        if self.on_restore:self.on_restore()


class TeamTests(unittest.TestCase):
    def rig(self):
        rpc=FakeRPC();saved=[];controllers=FakeControllers();adapter=FakeAdapter(rpc)
        def reader(r,g):s=copy.deepcopy(r.scene);s['observedAt']=time.monotonic();return s
        host=m.Supervisor(save=lambda d:saved.append(copy.deepcopy(d)),rpcs={1:rpc},adapter=adapter,controllers=controllers,reader=reader)
        row=host.accept(request(),1,rpc.epoch);return host,rpc,adapter,controllers,row,saved

    def test_request_global_dedup_and_payload_binding_survive_restart(self):
        host,rpc,_,_,row,saved=self.rig()
        self.assertIsNone(host.accept(request(),4,'OtherEpoch1234'))
        with self.assertRaises(m.Error):host.accept(request(guid=5),4,'OtherEpoch1234')
        restart=m.Supervisor(data=copy.deepcopy(host.data),save=lambda d:None,rpcs={1:rpc})
        self.assertIsNone(restart.accept(request(),1,rpc.epoch))
        self.assertEqual(restart.data['requests'][row['requestId']]['phase'],'OUTCOME_UNCONFIRMED')
        self.assertTrue(saved)

    def test_stale_request_and_expired_initial_age_never_mutate(self):
        host,rpc,adapter,ctl,_,_=self.rig()
        row=host.accept(request(seq=2,ageMs=29999),1,rpc.epoch)
        row['expiresAt']=time.time()-1
        host.execute(row)
        self.assertEqual(row['phase'],'FAILED');self.assertNotIn('stop',ctl.calls)
        self.assertFalse(any(op in ('stop','start') for _,op,_ in adapter.calls))
        stale=host.accept(request(seq=3,available=False),1,rpc.epoch)
        self.assertEqual(stale['phase'],'FAILED')

    def test_reset_reserves_before_every_write_and_positive_retirement_then_new_ready(self):
        host,rpc,adapter,ctl,row,saved=self.rig()
        host.execute(row)
        self.assertEqual(row['phase'],'COMPLETE',row)
        self.assertEqual(ctl.calls,['capture','stop','restore'])
        life=[v for _,op,v in adapter.calls if op in ('stop','start')]
        self.assertEqual(life[0]['operationId'],life[1]['operationId'])
        self.assertEqual(str(m.uuid.UUID(life[0]['operationId'])),life[0]['operationId'])
        self.assertNotIn(row['requestId'],life[0]['operationId'])
        self.assertEqual(m.intents(rpc.scene),row['controls'])
        self.assertTrue(any(x['kind']=='normal_stop' for v in saved for x in v['requests'].get(row['requestId'],{}).get('writes',[])))

    def test_noop_or_unproven_stop_cannot_start(self):
        for field in ('normalLogoutObserved','oldPidGone','rpcClosed','sessionContainersGone','stopReceiptRef'):
            with self.subTest(field=field):
                host,_,adapter,_,row,_=self.rig()
                adapter.stop_bad=dict(outcome='stopped',normalLogoutObserved=True,oldPidGone=True,rpcClosed=True,sessionContainersGone=True,stopReceiptRef='proof')
                adapter.stop_bad[field]=False
                host.execute(row);self.assertEqual(row['phase'],'OUTCOME_UNCONFIRMED')
                self.assertFalse(any(op=='start' for _,op,_ in adapter.calls))
        host,_,adapter,_,row,_=self.rig();adapter.stop_bad=dict(outcome='alreadyAbsent')
        host.execute(row);self.assertFalse(any(op=='start' for _,op,_ in adapter.calls))

    def test_human_command_after_stop_prevents_start_or_restore(self):
        host,_,adapter,ctl,row,_=self.rig();ctl.on_stop=lambda:setattr(host,'owner_generation',1)
        host.execute(row);self.assertNotIn('restore',ctl.calls)
        self.assertFalse(any(op=='start' for _,op,_ in adapter.calls))

    def test_human_command_during_new_body_read_prevents_follow_restore(self):
        host,rpc,_,ctl,row,_=self.rig();reader=host.reader;seen=[0]
        def read(r,g):
            result=reader(r,g)
            if result['sessionId']=='aw-222':
                seen[0]+=1
                if seen[0]==2:host.owner_generation+=1
            return result
        host.reader=read;host.execute(row)
        self.assertEqual(row['phase'],'OUTCOME_UNCONFIRMED')
        self.assertFalse(any(name=='buddy-actions.follow' for name,_ in rpc.calls))
        self.assertNotIn('restore',ctl.calls)

    def test_new_body_progress_mismatch_stops_before_control_restore(self):
        host,rpc,adapter,ctl,row,_=self.rig();adapter.on_start=lambda:rpc.scene['quests']['questLog'][0].update(questId=59)
        host.execute(row);self.assertEqual(row['phase'],'OUTCOME_UNCONFIRMED')
        self.assertNotIn('restore',ctl.calls)
        self.assertFalse(any(name in {v[1] for v in m.CONTROLS} for name,_ in rpc.calls))

    def test_reset_only_sticky_latches_retire_by_positive_logout_not_synthetic_ack(self):
        host,rpc,adapter,_,row,_=self.rig()
        host.rpcs[4]=FakeRPC(initial(4,'aw-444'));host.rpcs[5]=FakeRPC(initial(5,'aw-555'))
        host.controllers.capture=lambda g:[dict(folder='fixed-shared',script='party-maintenance')]
        rpc.scene['actions'].update(autoAttackUnconfirmed=True,autoAttacking=True,motionUnconfirmed=True)
        original=copy.deepcopy(rpc.scene)
        m.reset_admission(rpc.scene,1);self.assertEqual(rpc.scene,original)
        with self.assertRaises(m.Error):m.safe(rpc.scene,1)
        old_start=adapter.on_start
        def started():
            rpc.scene['actions'].update(autoAttackUnconfirmed=False,autoAttacking=False,motionUnconfirmed=False)
        adapter.on_start=started
        host.execute(row);self.assertEqual(row['phase'],'COMPLETE',row)
        self.assertEqual(row['oldBodyLatches'],dict(autoAttackUnconfirmed=True,motionUnconfirmed=True))
        self.assertTrue(row['oldBodyRetiredByNormalLogout'])
        self.assertFalse(any(name=='buddy-actions.travel_to' for name,_ in rpc.calls))

    def test_reset_latch_exception_never_admits_actual_danger_or_pending_mutations(self):
        changes=[lambda s:s['world']['self'].update(movementFlags=1),lambda s:s['actions'].update(moving=True),
            lambda s:s['world']['self'].update(unitFlags=0x80000),lambda s:s['world']['self'].update(health=84),
            lambda s:s['actions'].update(castPending=True),lambda s:s['actions'].update(traveling=True),
            lambda s:s['actions'].update(autoAttackRequested=True),lambda s:s['professions'].update(active=True),
            lambda s:s['loot'].update(pendingCorpseGuid='42'),lambda s:s['trade'].update(phase='OPEN'),
            lambda s:s['inventory'].update(operation=dict(status='OUTCOME_UNCONFIRMED'))]
        for change in changes:
            s=initial();s['actions'].update(autoAttackUnconfirmed=True,motionUnconfirmed=True);change(s)
            with self.assertRaises(m.Error):m.reset_admission(s,1)

    def test_cast_or_ordinary_follow_after_intent_restore_does_not_fake_failed_body(self):
        host,rpc,_,_,row,_=self.rig()
        old=rpc.call
        def call(method,params,**kw):
            result=old(method,params,**kw)
            if method=='buddy-actions.buffs':rpc.scene['actions']['castPending']=True
            if method=='buddy-actions.follow':rpc.scene['actions']['moving']=True
            return result
        rpc.call=call;host.execute(row);self.assertEqual(row['phase'],'COMPLETE',row)

    def test_ambiguous_stop_is_durable_no_replay_even_new_request(self):
        host,rpc,adapter,_,row,_=self.rig()
        old=adapter.call
        def call(g,op,params):
            if op=='stop':raise m.Error('Transport unavailable')
            return old(g,op,params)
        adapter.call=call;host.execute(row);writes=copy.deepcopy(row['writes'])
        restart=m.Supervisor(data=copy.deepcopy(host.data),rpcs={1:rpc},reader=host.reader)
        self.assertEqual(restart.data['requests'][row['requestId']]['writes'],writes)
        second=host.accept(request(seq=2),1,rpc.epoch)
        host.publish=lambda:None
        host.poll()
        self.assertEqual(second['phase'],'FAILED')

    def test_fresh_relay_epoch_and_request_binding_checked_before_first_write(self):
        for mode in ('epoch','expired','source'):
            host,rpc,_,ctl,row,_=self.rig()
            if mode=='epoch':rpc.epoch='ReplacementChat123'
            if mode=='expired':rpc.requests[0]['available']=False
            if mode=='source':rpc.requests[0]['subjectGuid']='5'
            host.execute(row);self.assertNotIn('stop',ctl.calls)
            self.assertFalse(row['writes'])

    def test_changed_chat_epoch_bootstraps_before_old_cursor(self):
        host,rpc,_,_,row,_=self.rig()
        host.data['relays']['1']=dict(epoch='PreviousChat123',cursor=70,chatCursor=99)
        rpc.epoch='NewChatEpoch123';rpc.requests=[];host.publish=lambda:None
        row['phase']='FAILED';host.poll()
        self.assertEqual(host.data['relays']['1']['epoch'],rpc.epoch)
        rpc.requests=[request(seq=2)];host.worker=type('Busy',(),{'is_alive':lambda self:True})()
        host.poll();self.assertIn('aabbccddeeff.2',host.data['requests'])
        self.assertFalse(any(m.number(p.get('afterSequence',0))==70 for name,p in rpc.calls if name=='buddy-chat.management_requests'))

    def test_unstuck_uses_observed_owner_only_and_exact_cleanup(self):
        host,rpc,_,_,row,_=self.rig();row['action']='UNSTUCK';rpc.requests[0]['action']='UNSTUCK'
        host.execute(row);self.assertEqual(row['phase'],'COMPLETE',row)
        writes=[(method,p) for method,p in rpc.calls if method=='buddy-actions.travel_to']
        self.assertEqual(len(writes),1);self.assertEqual(writes[0][1],dict(token='owned-token',mapId=0,arrivalDistance=3,x=10.,y=0.,z=0.))
        self.assertTrue(row['arrivalObservedUnderLease']);self.assertTrue(row['cleanupConfirmed'])
        self.assertEqual([x['kind'] for x in row['writes']],['acquire_exploration','normal_travel','release_exploration'])

    def test_unstuck_only_known_follow_motion_can_handoff_normal_stop(self):
        host,rpc,_,_,row,_=self.rig();row['action']='UNSTUCK';rpc.requests[0]['action']='UNSTUCK'
        rpc.scene['actions']['moving']=True
        host.execute(row);self.assertEqual(row['phase'],'COMPLETE',row)
        self.assertEqual(sum(name=='buddy-actions.acquire_interaction' for name,_ in rpc.calls),1)
        host,rpc,_,_,row,_=self.rig();row['action']='UNSTUCK';rpc.scene['actions'].update(moving=True,following=False)
        host.execute(row);self.assertFalse(row['writes'])

    def test_unstuck_human_stop_watchdog_only_releases_exact_token_no_reissue(self):
        host,rpc,_,_,row,_=self.rig();row['action']='UNSTUCK';rpc.requests[0]['action']='UNSTUCK'
        original=rpc.call
        def call(method,params,**kwargs):
            if method=='buddy-actions.travel_to':
                rpc.calls.append((method,copy.deepcopy(params)));host.owner_generation+=1
                time.sleep(.65)
                return dict(reason='exploration_following_client_navmesh_estimate',moving=False,traveling=False)
            return original(method,params,**kwargs)
        rpc.call=call;host.execute(row)
        self.assertEqual(row['phase'],'OUTCOME_UNCONFIRMED')
        self.assertTrue(row['watchdogStopped']);self.assertTrue(row['cleanupConfirmed'])
        self.assertEqual(sum(name=='buddy-actions.travel_to' for name,_ in rpc.calls),1)
        self.assertEqual(sum(name=='buddy-actions.release_interaction' for name,_ in rpc.calls),1)

    def test_missing_owner_or_unbounded_destination_does_not_warp(self):
        for change in (dict(present=False),dict(x=201.),dict(movementFlags=1),dict(mapId=1)):
            host,rpc,_,_,row,_=self.rig();row['action']='UNSTUCK';rpc.scene['world']['owner'].update(change)
            host.execute(row);self.assertEqual(row['phase'],'NEEDS_GM_HELP')
            self.assertFalse(row['writes']);self.assertFalse(any(name=='buddy-actions.travel_to' for name,_ in rpc.calls))

    def test_ambiguous_travel_never_retries_and_cleanup_reserved_once(self):
        host,rpc,_,_,row,_=self.rig();row['action']='UNSTUCK';rpc.requests[0]['action']='UNSTUCK'
        rpc.fail=lambda name,p: (_ for _ in ()).throw(m.Error('Unconfirmed')) if name=='buddy-actions.travel_to' else None
        host.execute(row);self.assertEqual(row['phase'],'OUTCOME_UNCONFIRMED')
        self.assertEqual(sum(name=='buddy-actions.travel_to' for name,_ in rpc.calls),1)
        self.assertEqual(sum(name=='buddy-actions.release_interaction' for name,_ in rpc.calls),1)
        self.assertTrue(row['cleanupConfirmed'])

    def test_no_synthetic_activity_from_enabled_intents(self):
        s=initial();self.assertEqual(m.activity(s),'ONLINE')
        s['actions']['moving']=True;self.assertEqual(m.activity(s),'FOLLOWING')
        s['world']['self']['unitFlags']=0x80000;self.assertEqual(m.activity(s),'FIGHTING')
        s['world']['self']['unitFlags']=0;s['actions']['castPending']=True;s['actions']['lastCastSpellId']=2050
        self.assertEqual(m.activity(s),'HEALING')
        s['actions'].update(lastCastSpellId=1459,lastCastTargetGuid='1',lastBuff=dict(phase='waiting_server',transportWritten=True,spellId=1459,targetGuid='1'))
        self.assertEqual(m.activity(s),'BUFFING')
        s['actions']['lastBuff']['phase']='confirmed';self.assertEqual(m.activity(s),'BUSY')

    def test_nix_only_owns_its_fixed_brain_and_no_main_shared_controllers(self):
        self.assertEqual(m.Controllers.folders(8),[(m.ROOT/'data/converse/nixwick','converse',8)])
        self.assertEqual(len(m.Controllers.folders(1)),4)

    def test_full_progress_vectors_and_item_conservation(self):
        s=initial();baseline=m.progress(s)
        s['inventory']['inventoryBags'][0].update(slotCount=1,slots=[dict(itemGuid='42',metadataKnown=True,entry=7005,stackCount=1)])
        self.assertNotEqual(m.progress(s),baseline)
        s['inventory']['inventoryBags'][0]['contentsKnown']=False
        with self.assertRaises(m.Error):m.progress(s)

    def test_private_bounded_ledger_roundtrip_and_no_symlink(self):
        with tempfile.TemporaryDirectory() as d:
            path=Path(d)/'operations.json';value=dict(rows=['x'*200 for _ in range(64)])
            m.save_json(path,value);self.assertEqual(m.load_json(path),value)
            link=Path(d)/'alias';link.symlink_to(path)
            with self.assertRaises((m.Error,OSError)):m.load_json(link)

    def test_real_read_frame_binds_all_modules_and_world_self(self):
        rpc=FakeRPC();s=m.scene(rpc,1);m.safe(s,1)
        rpc.scene['inventory']['sessionId']='old-body'
        with self.assertRaises(m.Error):m.scene(rpc,1)
        rpc=FakeRPC();rpc.scene['trade']['sessionId']='historical-aw'
        with self.assertRaises(m.Error):m.scene(rpc,1)

    def test_actual_controller_status_shapes_are_ready_without_inventing_top_level_town_phase(self):
        for script in ('converse','party-town','party-maintenance','party-quests'):
            key='character_identity' if script=='converse' else 'identity'
            row=dict(script=script,key=key,identity={'fixed':'identity'},instance='a'*32)
            status=dict(pid=123,instance='b'*32,updatedAt=200,**{key:row['identity']})
            if script=='converse':status.update(phase='listening',updated=200)
            elif script=='party-town':status.update(routineActive=True,own=dict(phase='idle_no_qualified_town_upkeep'))
            else:status['phase']='running'
            self.assertTrue(m.Controllers.ready(row,status,123,199),script)
            self.assertFalse(m.Controllers.ready(row,status,123,201),script)
            self.assertFalse(m.Controllers.ready(row,status,999,199),script)
            status['instance']=row['instance'];self.assertFalse(m.Controllers.ready(row,status,123,199),script)

    def test_real_controller_capture_rejects_busy_town_or_state_dir_mismatch(self):
        def rig(busy=False,bad_path=False):
            statuses={};commands={}
            for i,(folder,script,subject) in enumerate(m.Controllers.folders(1),100):
                identity=(dict(profile='default',name='Arlen',guid=1,owner_guid=3,rpc_url='http://127.0.0.1:8086/rpc',**{'class':'priest'}) if script=='converse'
                    else dict(version=1,mode='party_town',characterGuid=1,rpcUrl='http://127.0.0.1:8086/rpc') if script=='party-town'
                    else m.c.PARTY_MAINTENANCE_IDENTITY if script=='party-maintenance' else m.c.PARTY_QUEST_IDENTITY)
                key='character_identity' if script=='converse' else 'identity'
                statuses[folder/'status.json']=dict(pid=i,instance=f'{i:032x}',updatedAt=time.time(),**{key:identity},
                    own=dict(busy=busy if script=='party-town' else False),activityId='',characters=[])
                commands[i]=' '.join([m.sys.executable,'-B',str(m.ROOT/'dev'/script),'run','--state-dir',str(m.ROOT/'data/foreign' if bad_path and script=='converse' else folder)])
            return statuses,commands
        for busy,bad in ((False,False),(True,False),(False,True)):
            statuses,commands=rig(busy,bad);ctl=m.Controllers()
            with patch.object(m,'load_json',side_effect=lambda p:statuses[p]),patch.object(ctl,'held',return_value=True),patch.object(ctl,'alive',return_value=True),patch.object(ctl,'command',side_effect=lambda p:commands[p]):
                if busy or bad:
                    with self.assertRaises(m.Error):ctl.capture(1)
                else:self.assertEqual(len(ctl.capture(1)),4)

    def test_cooperative_stop_marker_remains_bound_to_original_instance(self):
        with tempfile.TemporaryDirectory() as d:
            folder=Path(d);identity=dict(fixed='brain');row=dict(folder=str(folder),script='converse',pid=123,instance='a'*32,
                key='character_identity',identity=identity,argv=[m.sys.executable,'-B',str(m.ROOT/'dev/converse'),'run'])
            live=dict(pid=123,instance=row['instance'],character_identity=identity,phase='listening')
            stopped=dict(live,phase='stopped');calls=[0]
            def status(path):
                calls[0]+=1
                return live if calls[0]==1 else stopped
            ctl=m.Controllers();reserved=[]
            with patch.object(m,'load_json',side_effect=status),patch.object(ctl,'command',return_value=' '.join(row['argv'])),patch.object(ctl,'held',return_value=False),patch.object(ctl,'alive',return_value=False),patch.object(m.subprocess,'run') as run:
                ctl.stop([row],lambda *args:reserved.append(args),lambda:None)
            self.assertEqual(m.load_json(folder/'stop.json'),dict(instance='a'*32))
            self.assertEqual(reserved[0][0],'controller_stop')
            run.assert_not_called()

    def test_exact_native_framework_interpreter_not_python3_basename(self):
        self.assertTrue(m.trusted_interpreter(m.sys.executable))
        if m.sys.platform=='darwin':
            actual=Path('/opt/homebrew/Cellar/python@3.14/3.14.8/Frameworks/Python.framework/Versions/3.14/Resources/Python.app/Contents/MacOS/Python')
            if actual.exists():
                self.assertEqual(actual.name,'Python');self.assertTrue(m.trusted_interpreter(str(actual)))
        self.assertFalse(m.trusted_interpreter('python3'))
        with tempfile.TemporaryDirectory() as d:
            fake=Path(d)/'python3';fake.write_text('not the current executable')
            self.assertFalse(m.trusted_interpreter(str(fake)))

    def test_main_adapter_fixed_cli_argv_and_exact_json_contract(self):
        result=types.SimpleNamespace(returncode=0,stdout=json.dumps(dict(guid=5,outcome='stopped')))
        with patch.object(m.subprocess,'run',return_value=result) as run:
            m.Adapter().call(5,'stop',dict(operationId='op',expectedFingerprint='binding'))
        argv=run.call_args.args[0]
        self.assertEqual(m.shlex.split(argv[-1]),['python3','-B',m.MAIN_ADAPTER,'stop','--guid','5'])
        self.assertEqual(json.loads(run.call_args.kwargs['input']),dict(operationId='op',expectedFingerprint='binding'))
        self.assertNotIn('guid',json.loads(run.call_args.kwargs['input']))

    def nix(self):
        return dict(guid=8,rpcPort=8402,online=True,ownedProfile=True,pid=101,startTicks=200,exeDevice=1,exeInode=2,
                    logDevice=1,logInode=3,logOffset=123,coreSha256=m.NIX_CORE,launcherSha256=m.NIX_LAUNCHER,sessionId='aw-888')

    def test_nix_exact_original_tuple_normalization_and_stop_translation(self):
        raw=self.nix();pending=dict(raw,lifecycleOperation=dict(requestId='prior',phase='STOP_RESERVED'))
        normalized=m.Adapter.nix_status(pending)
        self.assertEqual(normalized['state'],'trackedReady')
        self.assertEqual(set(normalized['adapterExpected']),m.NIX_KEYS)
        self.assertNotIn('lifecycleOperation',normalized['adapterExpected'])
        op=str(m.uuid.uuid4());reply=dict(expected=raw,normalLogoutObserved=True,oldPidGone=True,rpcClosed=True,sessionContainersGone=True,stopReceiptRef=op)
        with patch.object(m.subprocess,'run',return_value=types.SimpleNamespace(returncode=0,stdout=json.dumps(reply))) as run:
            parsed=m.Adapter().call(8,'stop',dict(operationId=op,expectedFingerprint=normalized['fingerprint'],expected=raw))
        self.assertEqual(parsed['outcome'],'stopped')
        self.assertEqual(json.loads(run.call_args.kwargs['input']),dict(requestId=op,expected=raw))
        command=m.shlex.split(run.call_args.args[0][-1]);self.assertEqual(command[:3],['python3','-B','-c'])
        self.assertIn(m.NIX_ADAPTER_SHA,command[3])
        self.assertNotIn('password',command[3])
        for key,value in [('coreSha256','wrong'),('launcherSha256','wrong'),('ownedProfile',False),('guid',5),('pid',None)]:
            with self.subTest(key=key):
                wrong=dict(raw);wrong[key]=value
                with self.assertRaises(m.Error):m.Adapter.nix_status(wrong)

    def test_global_retired_addon_nonce_and_cross_relay_lower_sequence_are_not_actions(self):
        host,rpc,_,_,_,_=self.rig()
        host.accept(request(seq=10),4,'OtherChat123')
        older=host.accept(request(seq=9),5,'ThirdChat123');self.assertEqual(older['phase'],'FAILED')
        new=request(seq=1);new.update(addonSession='111111111111',requestId='111111111111.1')
        host.accept(new,4,'OtherChat123')
        retired=host.accept(request(seq=11),1,rpc.epoch);self.assertEqual(retired['phase'],'FAILED')

    def test_definite_navigation_no_route_has_help_outcome_not_success_or_warp(self):
        host,rpc,_,_,row,_=self.rig();row['action']='UNSTUCK';rpc.requests[0]['action']='UNSTUCK'
        original=rpc.call
        def call(method,params,**kwargs):
            if method=='buddy-actions.travel_to':
                rpc.calls.append((method,copy.deepcopy(params)))
                return dict(reason='exploration_navigation_unavailable',moving=False,traveling=False,motionUnconfirmed=False)
            return original(method,params,**kwargs)
        rpc.call=call;host.execute(row)
        self.assertEqual(row['phase'],'NEEDS_GM_HELP',row)
        self.assertTrue(row['cleanupConfirmed']);self.assertFalse(row['arrivalObservedUnderLease'])
        self.assertEqual(sum(name=='buddy-actions.travel_to' for name,_ in rpc.calls),1)

    def test_same_body_portal_or_unknown_map_cannot_confirm_coincident_unstuck_xyz(self):
        for mode in ('portal','raw_map','owner_map','unknown_position'):
            host,rpc,_,_,row,_=self.rig();row['action']='UNSTUCK';rpc.requests[0]['action']='UNSTUCK'
            original=rpc.call
            def call(method,params,**kwargs):
                value=original(method,params,**kwargs)
                if method=='buddy-actions.travel_to':
                    if mode=='portal':
                        rpc.scene['actions']['position']['mapId']=1
                        rpc.scene['world']['self']['mapId']=1
                        rpc.scene['world']['owner']['mapId']=1
                    elif mode=='raw_map':rpc.scene['world']['self']['mapId']=1
                    elif mode=='owner_map':rpc.scene['world']['owner']['mapId']=1
                    else:rpc.scene['actions']['position']['known']=False
                return value
            rpc.call=call;host.execute(row)
            self.assertEqual(row['phase'],'OUTCOME_UNCONFIRMED',mode)
            self.assertNotEqual(row.get('arrivalObservedUnderLease'),True,mode)
            self.assertEqual(sum(name=='buddy-actions.travel_to' for name,_ in rpc.calls),1)
            self.assertTrue(row['cleanupConfirmed'])

    def test_unsafe_actual_states_and_unknowns_do_not_pass_guard(self):
        for name,change in [('combat',lambda s:s['world']['self'].update(unitFlags=0x80000)),
            ('health',lambda s:s['world']['self'].update(health=101)),('unknown',lambda s:s['world']['self'].update(movementFlagsKnown=False)),
            ('aa',lambda s:s['actions'].update(autoAttackUnconfirmed=True)),('loot',lambda s:s['loot'].update(pendingCorpseGuid='42')),
            ('de',lambda s:s['professions'].update(operation=dict(status='OUTCOME_UNCONFIRMED'))),
            ('trade',lambda s:s['trade'].update(phase='OPEN'))]:
            with self.subTest(name=name):
                s=initial();change(s)
                with self.assertRaises(m.Error):m.safe(s,1)

    def test_actual_training_and_quest_unknown_enums_stay_blocked_definite_prewrite_block_is_idle(self):
        for module,field,status in [('training','lastPurchase','REQUESTED'),('training','lastPurchase','UNCONFIRMED_TIMEOUT'),
            ('training','lastPurchase','TRAINER_SUCCEEDED'),('quests','lastAcceptance','WRITE_UNCONFIRMED'),
            ('quests','lastReward','REWARD_UNCONFIRMED'),('quests','lastExploration','EXPLORATION_WRITE_UNCONFIRMED')]:
            with self.subTest(module=module,status=status):
                s=initial();s[module][field]=dict(status=status)
                with self.assertRaises(m.Error):m.safe(s,1)
        for module in ('professions','inventory','quest-items'):
            s=initial();s[module]['operation']=dict(status='BLOCKED',leaseReleased=False)
            m.safe(s,1)
            s[module]['operation']['transportWritten']=True
            with self.assertRaises(m.Error):m.safe(s,1)


if __name__=='__main__':unittest.main()
