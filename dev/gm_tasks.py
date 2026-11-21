"""Mechanical host for editable, observation-driven GM task policies."""
import argparse
import concurrent.futures
import contextlib
import hashlib
import importlib.machinery
import importlib.util
import json
import math
import os
from pathlib import Path
import re
import shlex
import signal
import struct
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parent.parent
_loader = importlib.machinery.SourceFileLoader('gm_task_runtime', str(ROOT/'dev/task-runtime'))
_spec = importlib.util.spec_from_loader(_loader.name, _loader)
runtime = importlib.util.module_from_spec(_spec)
_loader.exec_module(runtime)

INSTRUCTIONS = '''You can directly execute ordered GM tasks with development.gmTasks.
For recovery/regroup requests, preserve the whole requested outcome, including destination,
full health/resources and original roster. Write a goal JSON, inspect/edit the shared Lua
policy, run the same task host available to Lothar, then inspect its observed completion.
Use gmTasks.inspect for current GM and per-binding public party/vitals observations.
The GM being outside the group does not make teammates unavailable: inspect their
public party observations before deriving the requested original roster.
A transport acknowledgement is not arrival or revival. Do not substitute a single .revive
for a multi-step request. If an effect is uncertain, inspect/resume the task rather than
resending it blindly. Keep gm_command empty when a task handles the request; this does not
remove your broad rank-2 one-command access for unrelated requests. Use authenticated game
client packets only, never the realm console or server internals.'''


def tools(root, guid, rpc_url):
    if str(guid) != '87':
        return None
    root = Path(root)
    command = ['python3', '-B', str(root/'dev/gm-task')]
    return dict(instructions=INSTRUCTIONS, policy=str(root/'behaviors/gm-recovery.lua'),
        holdPolicy=str(root/'behaviors/gm-recovery-hold.lua'),
        transportConfig=str(root/'data/converse/lotharen/gm-transport.json'),
        stateRoot=str(root/'data/converse/lotharen/gm-tasks'),
        goalSchema=dict(id='unique task id', player=dict(guid='3', name='Joel', tolerance=7),
            destination='Alternatively an explicit {mapId,x,y,z,tolerance,label,provenance}; never both.',
            members=[dict(guid='<actual player GUID>',name='<actual name>',rpcUrl='<own public RPC, omit for human>')],
            provenance='Use a current observed player ground pose or explicitly justified cleared destination.'),
        start=command+['start','--rpc-url',rpc_url,'--goal','<GOAL.json>','--state-dir','<STATE_DIR>',
            '--transport',str(root/'data/converse/lotharen/gm-transport.json')],
        run=command+['run','--rpc-url',rpc_url,'--goal','<GOAL.json>','--state-dir','<STATE_DIR>',
            '--transport',str(root/'data/converse/lotharen/gm-transport.json')],
        bindings=command+['bindings'],source=command+['source'],
        inspect=command+['inspect','--rpc-url',rpc_url],
        status=command+['status','--state-dir','<STATE_DIR>'],
        cancel=command+['cancel','--state-dir','<STATE_DIR>'])


def _varint(value):
    out=bytearray()
    while value>127:
        out.append((value&127)|128);value>>=7
    return bytes(out)+bytes([value])


class SessionTransport:
    """An exact packet write through the existing authenticated module callback."""
    def __init__(self, rpc, config):
        self.rpc,self.config=rpc,config
        prefix=config.get('prefix',[])
        if not isinstance(prefix,list) or not all(isinstance(x,str) and '\0' not in x for x in prefix):
            raise ValueError('transport prefix must be an argv list')
        self.prefix=prefix
    def send(self, opcode, payload, expected_session):
        live=self.rpc.call('buddy-actions.status',{})
        party=self.rpc.call('buddy-party.state',{})
        sid=live.get('sessionId')
        if (str(live.get('selfGuid'))!='87' or sid!=expected_session or party.get('sessionId')!=sid
                or not isinstance(sid,str) or not re.fullmatch(r'aw-[0-9]+',sid)):
            raise ValueError('authenticated Lotharen body changed before packet write')
        message=b'\x08'+_varint(opcode)+b'\x12'+_varint(len(payload))+payload
        wire=struct.pack('>BI',0,len(message))+message
        socket=self.config.get('socketTemplate','/tmp/{session}/0/session.sock').format(session=sid)
        curl=[self.config.get('curl','curl'),'--silent','--show-error','--fail','--max-time','5',
            '--http2-prior-knowledge','--unix-socket',socket,'--header','Content-Type: application/grpc',
            '--header','TE: trailers','--data-binary','@-','--dump-header','-','--output','/dev/null',
            'http://localhost/agentwow.module.v1.Session/SendPacket']
        argv=self.prefix+[shlex.join(curl)] if self.prefix else curl
        reply=subprocess.run(argv,input=wire,capture_output=True,timeout=10)
        if reply.returncode or not re.search(rb'^grpc-status: 0\r?$',reply.stdout,re.M|re.I):
            raise RuntimeError('game packet outcome unconfirmed; observe before deciding whether another action is needed')
        return dict(sessionId=sid,transportWritten=True,opcode=opcode)
    def gm(self,command,sid):
        if not isinstance(command,str) or re.fullmatch(r'\.[A-Za-z][ -~]{0,253}',command) is None or command!=command.rstrip():
            raise ValueError('one ASCII native GM command required')
        return self.send(0x95,struct.pack('<II',1,7)+command.encode('ascii')+b'\0',sid)
    def select(self,guid,sid):
        return self.send(0x13d,struct.pack('<Q',int(guid)),sid)



def writer_folder(root,sid,config):
    if not isinstance(sid,str) or not re.fullmatch(r'aw-[0-9]+',sid):raise ValueError('current authenticated GM session required')
    return Path(config.get('writerRoot',str(Path(root)/'data/gm-task-writers')))/sid


def claim_writer(store,state_dir=None):
    data=store.load() or dict(version=1,active=False)
    requested=str(Path(state_dir).absolute()) if state_dir else None
    if data.get('active') and data.get('stateDir')!=requested:
        raise ValueError('GM recovery task already owns this body: '+str(data.get('stateDir'))+'; inspect/resume/cancel that task before another command')
    if requested:
        data.update(active=True,stateDir=requested,pid=os.getpid());store.save(data)
    return data


def single_command(rpc,config,command,root=ROOT):
    live=rpc.call('buddy-actions.status',{});sid=live.get('sessionId')
    with runtime.Store(writer_folder(root,sid,config).absolute()) as owner:
        claim_writer(owner)
        SessionTransport(rpc,config).gm(command,sid)
    return sid


def normalized(world, actions=None):
    p=(actions or {}).get('position',{})
    use=p.get('known') is True and p.get('mapKnown') is True
    result={k:world.get(k) for k in ('guid','present','aliveKnown','alive','healthKnown','maxHealth','health','powerKnown','powerType','power','maxPower','positionRevision','movementFlagsKnown','movementFlags','mapKnown')}
    result.update(known=world.get('present') is True,positionKnown=use or world.get('positionKnown') is True,
        hp=world.get('health',0) if world.get('healthKnown') else None,maxHp=world.get('maxHealth',0) if world.get('healthKnown') else None,hpKnown=world.get('healthKnown') is True,
        mana=world.get('power',0) if world.get('powerKnown') else None,maxMana=world.get('maxPower',0) if world.get('powerKnown') else None,manaKnown=world.get('powerKnown') is True,
        combatKnown=world.get('unitFlagsKnown') is True,inCombat=bool(world.get('unitFlags',0)&0x80000),
        manaUser=world.get('powerType',0)==0,positionSource='client_actions_position' if use else 'server_world_observation')
    for key in ('mapId','x','y','z'):
        result[key]=(p if use else world).get(key,0) if result['positionKnown'] else None
    if actions:
        result['sessionId']=actions.get('sessionId')
        result['visitId']=(actions.get('encounterVisit') or {}).get('revision')
        result['observationRevision']=world.get('positionRevision')
        result['positionRevision']=p.get('revision',world.get('positionRevision'))
    return result


class Host:
    def __init__(self,store,rpc,transport,bridge,goal,policy,hold,*,sleep=time.sleep,clock=time.time):
        self.store,self.rpc,self.transport,self.bridge=store,rpc,transport,bridge
        self.goal,self.policy,self.hold=goal,Path(policy),Path(hold)
        self.sleep,self.clock=sleep,clock
        self.member_rpcs={str(m['guid']):runtime.RPC(m['rpcUrl']) for m in goal.get('members',[]) if m.get('rpcUrl')}
        self.data=store.load() or dict(version=1,goal=goal,memory={},last=None,reservations={},holds={},phase='starting')
        if self.data.get('goal')!=goal:raise ValueError('state directory belongs to another explicit task goal')
        self.stopping=False
        self.route_cache={}
    def persist(self):self.store.save(self.data)
    def route(self,rpc,view,target):
        if not target or not view.get('positionKnown') or view.get('mapId')!=target.get('mapId',0):return None
        key=(view.get('sessionId'),view.get('positionRevision'),view.get('observationRevision'),target.get('mapId',0),*(target.get(k,0) for k in ('x','y','z')))
        if key not in self.route_cache:
            try:
                route=rpc.call('buddy-actions.plan_route_leg',dict(sessionId=view['sessionId'],selfGuid=view['guid'],expectedPositionRevision=str(view['positionRevision']),mapId=target.get('mapId',0),**{k:target.get(k,0) for k in ('x','y','z')}))
                endpoint=route.setdefault('endpoint',{});endpoint['positionKnown']=endpoint.get('known') is True
                for field in ('mapId','x','y','z'):endpoint.setdefault(field,0)
                self.route_cache[key]=route
            except Exception as error:self.route_cache[key]=dict(sessionId=view['sessionId'],goalReached=False,error=str(error))
        return self.route_cache[key]
    def observe(self):
        actions=self.rpc.call('buddy-actions.status',{});world=self.rpc.call('buddy-world.snapshot',{});party=self.rpc.call('buddy-party.state',{})
        if str(actions.get('selfGuid'))!='87' or str(world.get('self',{}).get('guid'))!='87' or party.get('sessionId')!=actions.get('sessionId'):
            raise ValueError('current authenticated GM observations disagree')
        gm=normalized(world['self'],actions);gm['guid']='87'
        visible={str(p.get('guid')):p for p in [world.get('owner',{}),*world.get('nearbyPlayers',[])] if p.get('present')}
        members=[];group=None
        def own(item):
            guid,rpc=item;a=rpc.call('buddy-actions.status',{});w=rpc.call('buddy-world.snapshot',{});p=rpc.call('buddy-party.state',{})
            if str(a.get('selfGuid'))!=guid or str(w.get('self',{}).get('guid'))!=guid or p.get('sessionId')!=a.get('sessionId'):raise ValueError('member current-body identity mismatch:'+guid)
            return guid,normalized(w['self'],a),p
        own_rows=dict((g,(v,p)) for g,v,p in concurrent.futures.ThreadPoolExecutor(max_workers=max(1,len(self.member_rpcs))).map(own,self.member_rpcs.items()))
        for member in self.goal.get('members',[]):
            guid=str(member['guid']);row=own_rows.get(guid)
            if row:
                view,p=row
                if group is None or str(p.get('group',{}).get('leaderGuid'))==str(self.data.get('originalLeader','')):group=p.get('group',{})
            else:view=normalized(visible.get(guid,{'guid':guid}))
            view.update(guid=guid,name=member['name'],visibleToGm=guid in visible)
            members.append(view)
        group=group or party.get('group',{})
        if not self.data.get('originalGroup') and group.get('known') and group.get('inGroup'):
            self.data['originalGroup']=group;self.data['originalLeader']=group.get('leaderGuid')
        anchor=self.goal.get('player')
        destination=normalized(visible.get(str(anchor.get('guid')),{})) if anchor else None
        if destination is not None:destination.update(guid=str(anchor['guid']),visibleToGm=str(anchor['guid']) in visible)
        threats=[]
        for c in world.get('nearbyCreatures',[]):
            if c.get('aliveKnown') and c.get('alive'):
                row=normalized(c);row.update(entry=c.get('entry'),factionTemplate=c.get('factionTemplate'),engaged=c.get('attackVictimKnown') and int(c.get('attackVictimGuid','0'))!=0)
                threats.append(row)
        selection=dict(known=world['self'].get('targetKnown') is True,targetGuid=str(world['self'].get('targetGuid','0')),sessionId=gm['sessionId'],source='server_world_target')
        if self.data.get('selection',{}).get('sessionId')==gm['sessionId']:
            selection=dict(self.data['selection'],observedTargetKnown=selection['known'],observedTargetGuid=selection['targetGuid'])
        route_target=self.goal.get('walkTarget') or self.goal.get('destination') or destination
        route=self.route(self.rpc,gm,route_target)
        for member in members:
            rpc=self.member_rpcs.get(member['guid'])
            if rpc:member['landingRoute']=self.route(rpc,member,route_target)
        return dict(gm=gm,members=members,group=group,destinationPlayer=destination,threats=threats,destinationRoute=route,
            selection=selection,provenance=dict(observedAtUnixMs=int(self.clock()*1000),gmSessionId=actions['sessionId'],partyRevision=party.get('revision'),destination=self.goal.get('provenance')))
    def install_holds(self):
        wrapper=self.hold.read_text()
        for guid,rpc in self.member_rpcs.items():
            record=self.data['holds'].get(guid)
            if record and record.get('restored'):continue
            state=rpc.call('buddy-actions.status',{})
            identity=dict(characterGuid=guid,sessionId=state['sessionId'])
            if str(state.get('selfGuid'))!=guid:raise ValueError('hold member identity changed')
            current=rpc.call('session.policy_source',identity)
            if record is None:
                record=dict(identity=identity,source=current['source'],revision=current['revision'],
                    standingFollowing=state.get('following') is True,followOwnerGuid=state.get('followOwnerGuid','3'))
                self.data['holds'][guid]=record;self.persist()
            elif record['identity']!=identity:raise ValueError('hold body session changed:'+guid)
            held='local original=(function()\n'+record['source']+'\nend)()\nlocal hold=(function()\n'+wrapper+'\nend)()\nreturn hold(original)\n'
            record['heldSourceSHA256']=hashlib.sha256(held.encode()).hexdigest()
            current_hash=hashlib.sha256(current['source'].encode()).hexdigest()
            if current_hash==record['heldSourceSHA256']:
                # Reconcile an earlier reload whose response was lost.
                record['heldRevision']=current['revision']
            elif current['source']==record['source']:
                self.persist()
                receipt=rpc.call('session.reload_policy',dict(identity,source=held,expectedRevision=current['revision']))
                record['heldRevision']=receipt['revision']
            else:raise ValueError('member policy changed independently:'+guid)
            self.persist()
            if state.get('following') is True:rpc.call('buddy-actions.follow',dict(enabled=False))
            record['followPaused']=True;self.persist()
    def restore_holds(self):
        errors=[]
        for guid,record in self.data.get('holds',{}).items():
            if record.get('restored'):continue
            try:
                rpc=self.member_rpcs[guid];current=rpc.call('session.policy_source',record['identity'])
                source_hash=hashlib.sha256(current['source'].encode()).hexdigest()
                if current['source']!=record['source']:
                    if source_hash!=record.get('heldSourceSHA256'):raise ValueError('policy changed independently; original source was not overwritten')
                    rpc.call('session.reload_policy',dict(record['identity'],source=record['source'],expectedRevision=current['revision']))
                if record.get('followPaused'):
                    state=rpc.call('buddy-actions.status',{})
                    if (state.get('following') is True)!=(record.get('standingFollowing') is True):
                        rpc.call('buddy-actions.follow',dict(enabled=record.get('standingFollowing') is True))
                record['restored']=True
            except Exception as error:errors.append(guid+': '+str(error))
        self.data['restoreErrors']=errors;self.persist();return errors
    def dispatch(self,action,observations):
        action_id=action.get('id')
        if not isinstance(action_id,str) or not action_id:raise ValueError('Lua action must carry a stable task id')
        if action_id in self.data['reservations']:
            self.data['last']=self.data['reservations'][action_id];return
        receipt=dict(id=action_id,outcome='reserved',action=action,at=self.clock(),sessionId=observations['gm']['sessionId'])
        self.data['reservations'][action_id]=receipt;self.data['last']=receipt;self.persist()
        try:
            kind=action['kind'];sid=observations['gm']['sessionId']
            if kind=='gm_command':result=self.transport.gm(action['command'],sid)
            elif kind=='select_target':
                result=self.transport.select(action['targetGuid'],sid)
                self.data['selection']=dict(known=False,targetGuid=str(action['targetGuid']),sessionId=sid,transportWritten=True)
            else:raise ValueError('unknown mechanical GM action:'+str(kind))
            receipt.update(outcome='written',receipt=result)
        except Exception as error:receipt.update(outcome='unconfirmed',error=str(error))
        self.persist()
    def step(self):
        observations=self.observe()
        self.data['lastObservations']=observations
        frame=dict(goal=self.goal,memory=self.data['memory'],observations=observations,last=self.data['last'],pending=[],now=self.clock(),cancelled=self.stopping)
        out=self.bridge.evaluate(self.policy.read_text(),frame)
        if not isinstance(out,dict) or not isinstance(out.get('memory'),dict):raise ValueError('Lua task result must contain memory')
        self.data.update(memory=out['memory'],phase=out.get('phase'),done=out.get('done') is True,error=out.get('error'),success=out.get('success') is True)
        self.persist()
        if out.get('action'):self.dispatch(out['action'],observations)
        return out
    def run(self):
        try:
            self.install_holds()
            while True:
                if (self.store.folder/'cancel.json').exists():self.stopping=True
                out=self.step()
                if out.get('done') or out.get('cancelled') and out.get('cleanupComplete'):
                    errors=self.restore_holds()
                    if errors:
                        out.update(done=False,success=False,cleanupComplete=False,error='policy_restore_incomplete')
                        self.data.update(done=False,success=False,error=out['error'],resumable=True);self.persist()
                    return out
                if out.get('error'):
                    # A blocked phase is resumable, not permission to unpause a
                    # half-transported party. Keep the hold until observed Lua
                    # cleanup or a resumed task reaches its terminal barrier.
                    self.data['resumable']=True;self.persist();return out
                self.sleep(.15)
        except Exception as error:
            self.data.update(error=str(error),phase='host_error',resumable=True)
            self.persist();raise


def bindings(root=ROOT):
    # These are the host's ordinary nonsecret worker identity records, not
    # account credentials, model prompts, or server-side records.
    folder=Path(root)/'data/converse'
    files=[folder/'status.json',*folder.glob('*/status.json')]
    result=[]
    for path in files:
        try:
            identity=json.loads(path.read_text()).get('character_identity',{})
            if not identity.get('guid') or not identity.get('name') or not identity.get('rpc_url'):continue
            runtime.RPC(identity['rpc_url'])
            result.append({k:identity[k] for k in ('guid','name','rpc_url')})
        except (OSError,ValueError,KeyError):continue
    return result



def inspect_clients(rpc_url,root=ROOT,*,rpc_factory=None,timeout=2):
    """Read configured public clients in parallel; never choose a party/task."""
    factory=rpc_factory or runtime.RPC
    configured=bindings(root)
    rows=[dict(guid='87',name='Lotharen',rpc_url=rpc_url)]
    rows.extend(row for row in configured if row['rpc_url']!=rpc_url)
    def read(binding):
        row=dict(guid=str(binding['guid']),name=binding['name'],rpcUrl=binding['rpc_url'],errors=[],observations={})
        try:rpc=factory(binding['rpc_url'])
        except Exception as error:
            row['errors'].append(dict(method='client_binding',error=str(error)));return row
        for method,key in (('buddy-actions.status','actions'),('buddy-world.snapshot','world'),('buddy-party.state','party')):
            try:
                value=rpc.call(method,{},timeout=timeout)
                if key=='world':
                    value={k:value[k] for k in ('self','owner','nearbyPlayers','nearbyCreatures','visitRevision') if k in value}
                elif key=='actions':
                    value={k:value[k] for k in ('sessionId','selfGuid','position','following','moving','motionUnconfirmed','reason') if k in value}
                row['observations'][key]=value
            except Exception as error:row['errors'].append(dict(method=method,error=str(error)))
        observations=row['observations'];a=observations.get('actions',{});w=observations.get('world',{});party=observations.get('party',{})
        row['identityMatches']=str(a.get('selfGuid'))==row['guid']==str(w.get('self',{}).get('guid')) and bool(a.get('sessionId')) and a.get('sessionId')==party.get('sessionId')
        return row
    with concurrent.futures.ThreadPoolExecutor(max_workers=max(1,len(rows))) as pool:
        observed=list(pool.map(read,rows))
    return dict(observedAtUnixMs=int(time.time()*1000),gm=observed[0],bindings=observed[1:],
        semantics='Read-only public observations. Group membership belongs to each observed client; a groupless GM does not imply unavailable teammates. Errors are explicit and no recovery or game action was started.')


def public_status(data):
    return dict({k:data.get(k) for k in ('version','goal','phase','done','success','error','memory','last','lastObservations','restoreErrors','updatedAt','resumable')},heldMembers=[dict(guid=g,heldRevision=r.get('heldRevision'),restored=r.get('restored',False),followPaused=r.get('followPaused',False)) for g,r in data.get('holds',{}).items()])


def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('command',choices=('inspect','bindings','source','start','run','status','cancel'))
    parser.add_argument('--state-dir',type=Path);parser.add_argument('--goal',type=Path);parser.add_argument('--transport',type=Path)
    parser.add_argument('--rpc-url');parser.add_argument('--policy',type=Path,default=ROOT/'behaviors/gm-recovery.lua')
    parser.add_argument('--policy-binary',type=Path,default=ROOT/'data/bin/luapolicy')
    args=parser.parse_args(argv)
    if args.command=='inspect':
        if not args.rpc_url:parser.error('inspect requires --rpc-url')
        print(json.dumps(inspect_clients(args.rpc_url)));return 0
    if args.command=='bindings':print(json.dumps(dict(bindings=bindings())));return 0
    if args.command=='source':print(json.dumps(dict(source=str(args.policy),sha256=hashlib.sha256(args.policy.read_bytes()).hexdigest())));return 0
    if args.state_dir is None:parser.error('this command requires --state-dir')
    if args.command=='start':
        if not args.goal or not args.transport or not args.rpc_url:parser.error('start requires --goal, --transport and --rpc-url')
        with runtime.Store(args.state_dir.absolute()) as store:
            metadata=store.folder/'process.json'
            if metadata.exists():
                previous=json.loads(metadata.read_text())
                try:os.kill(int(previous['pid']),0)
                except ProcessLookupError:pass
                else:raise ValueError('task process already running; inspect status')
            child=[sys.executable,'-B',str(ROOT/'dev/gm-task'),'run','--state-dir',str(args.state_dir.absolute()),'--goal',str(args.goal.absolute()),'--transport',str(args.transport.absolute()),'--rpc-url',args.rpc_url,'--policy',str(args.policy.absolute()),'--policy-binary',str(args.policy_binary.absolute())]
        # Release the task lock before the child acquires it. Holding it while
        # spawning could make the valid child fail its own exclusive startup.
        with (store.folder/'task.log').open('ab',buffering=0) as log:
            process=subprocess.Popen(child,cwd=ROOT,stdin=subprocess.DEVNULL,stdout=log,stderr=log,start_new_session=True)
        store.write('process.json',json.dumps(dict(pid=process.pid,argv=child,startedAt=time.time())).encode(),16384)
        print(json.dumps(dict(started=True,pid=process.pid,stateDir=str(store.folder),completion='not_yet_observed')))
        return 0
    if args.command=='cancel':
        args.state_dir.mkdir(mode=0o700,parents=True,exist_ok=True);(args.state_dir/'cancel.json').write_text('{}\n');print('{"cancelRequested":true}');return 0
    if args.command=='status':
        try:data=json.loads((args.state_dir/'state.json').read_text())
        except FileNotFoundError:
            process=json.loads((args.state_dir/'process.json').read_text());print(json.dumps(dict(phase='starting',done=False,process=process)));return 0
        print(json.dumps(public_status(data)));return 0
    if not args.goal or not args.transport or not args.rpc_url:parser.error('run requires --goal, --transport and --rpc-url')
    goal=json.loads(args.goal.read_text());config=json.loads(args.transport.read_text())
    with runtime.Store(args.state_dir.absolute()) as store:
        rpc=runtime.RPC(args.rpc_url);sid=rpc.call('buddy-actions.status',{}).get('sessionId')
        with runtime.Store(writer_folder(ROOT,sid,config).absolute()) as owner:
            owned=claim_writer(owner,store.folder)
            host=Host(store,rpc,SessionTransport(rpc,config),runtime.Bridge(args.policy_binary,ROOT/'modules'),goal,args.policy,ROOT/'behaviors/gm-recovery-hold.lua')
            for sig in (signal.SIGTERM,signal.SIGINT):signal.signal(sig,lambda *_:setattr(host,'stopping',True))
            result=host.run()
            if result.get('done') or result.get('cancelled') and result.get('cleanupComplete'):
                owned['active']=False;owner.save(owned)
            print(json.dumps(public_status(host.data)));return 0 if result.get('success') else 1
