"""Compact, evidence-only combat context for a character's existing brain.

The native journal retains packet evidence. This module compresses that evidence
semantically; it never treats a cast launch or a sampled HP delta as damage/healing.
"""
import collections
import functools
import json
from pathlib import Path
import time


@functools.lru_cache(maxsize=1)
def public_spell_names():
    """Optional packaged labels, never effect/area/castability classification."""
    try:
        names = {}
        with (Path(__file__).resolve().parent.parent/'pkg/spellcatalog/spells.jsonl').open() as source:
            for line in source:
                row=json.loads(line)
                if type(row.get('id')) is int and 0 < row['id'] <= 0xffffffff and isinstance(row.get('name'),str):
                    names[row['id']]=row['name']
        return names
    except (OSError,ValueError,UnicodeError): return {}


def decimal(value):
    if isinstance(value, bool):
        raise ValueError('boolean is not an event identity')
    text = str(value)
    if not text.isdecimal() or str(int(text)) != text or int(text) >= 2**64:
        raise ValueError('noncanonical uint64')
    return text


class CombatContext:
    def __init__(self, guid, *, prompt_bytes=10000, review_seconds=60, clock=time.monotonic, spell_names=None):
        self.guid = decimal(guid)
        self.prompt_bytes = prompt_bytes
        self.review_seconds = review_seconds
        self.clock = clock
        self.session = ''
        self.cursor = 0
        self.tail_bound = False
        self.coverage_start = 0
        self.available = False
        self.reason = 'combat_event_feed_not_polled'
        self.next_poll = 0
        self.gaps = []
        self.groups = {}
        self.first_sequence = None
        self.event_count = 0
        self.unknown_count = 0
        self.last_event_time = None
        self.scene = {}
        self.observed_at = None
        self.in_combat = False
        self.was_alive = None
        self.fight_serial = 0
        self.started_at = None
        self.completed = None
        self.critical = None
        self.last_gate = False
        self.reviewed = set()
        self.review_attempts = {}
        self.last_review_at = float('-inf')
        self.seen_situations = set()
        self.enemy_entries = set()
        self.pending_errors = collections.Counter()
        self.damage_members = {self.guid}
        self.party_membership_known = False
        self.spell_names = dict(spell_names) if spell_names is not None else None

    def bind(self, sid):
        if not isinstance(sid, str) or not sid:
            raise ValueError('combat feed requires an owned session')
        if self.session == sid:
            return
        # A replacement body has a new cursor domain; never reuse its old cursor.
        self.session, self.cursor = sid, 0
        self.tail_bound, self.coverage_start = False, 0
        self.available = False
        self.reason = 'new_body_awaiting_combat_feed'
        self.gaps, self.groups = [], {}
        self.first_sequence, self.event_count, self.unknown_count = None, 0, 0
        self.in_combat, self.was_alive = False, None
        self.started_at, self.completed, self.critical = None, None, None
        self.last_gate = False
        self.enemy_entries, self.pending_errors = set(), collections.Counter()
        self.scene = {}
        self.observed_at = None
        self.damage_members, self.party_membership_known = {self.guid}, False

    def ingest(self, batch):
        if not isinstance(batch, dict) or batch.get('sessionId') != self.session or decimal(batch.get('selfGuid', 0)) != self.guid:
            raise ValueError('combat event body identity mismatch')
        latest = int(decimal(batch.get('latestSequence', 0)))
        nxt = int(decimal(batch.get('nextSequence', self.cursor)))
        if not self.cursor <= nxt <= latest:
            raise ValueError('combat cursor went backwards or beyond journal')
        events, gaps = batch.get('events', []), batch.get('gaps', [])
        if not isinstance(events, list) or not isinstance(gaps, list):
            raise ValueError('invalid combat event page')
        # Validate the whole page before advancing or incorporating any evidence.
        previous = self.cursor
        for row in events:
            seq = int(decimal(row.get('sequence', 0)))
            if not previous < seq <= nxt:
                raise ValueError('non-increasing combat event sequence')
            previous = seq
            for field in ('sourceGuid', 'targetGuid'):
                decimal(row.get(field, 0))
            if row.get('amountKnown') is True and (type(row.get('amount', 0)) is not int or row.get('amount', 0) < 0):
                raise ValueError('invalid observed combat amount')
        previous = self.cursor
        for row in events:
            seq = int(row['sequence'])
            if seq > previous+1 and not any(int(g.get('fromSequence',0)) <= previous+1 and int(g.get('throughSequence',0)) >= seq-1 for g in gaps):
                self.gaps.append({'fromSequence':str(previous+1),'throughSequence':str(seq-1),'reason':'unreported_sequence_gap'})
            previous = seq
        self.gaps.extend(gaps)
        for row in events:
            self._event(row)
        if nxt > previous:
            self.gaps.append({'fromSequence':str(previous+1),'throughSequence':str(nxt),'reason':'page_advanced_without_events'})
        self.cursor = nxt
        self.available, self.reason = True, ''
        return bool(batch.get('hasMore'))

    def _event(self, row):
        seq = int(row['sequence'])
        self.first_sequence = self.first_sequence or seq
        self.event_count += 1
        self.last_event_time = row.get('receivedAtUnixMs')
        kind = 'unknown' if row.get('malformed') else str(row.get('kind', 'unknown'))
        if kind == 'unknown' or row.get('malformed'):
            self.unknown_count += 1
        source, target = str(row.get('sourceGuid', '0')), str(row.get('targetGuid', '0'))
        for actor in (source, target):
            numeric = int(actor)
            if numeric >> 48 == 0xf130:
                self.enemy_entries.add(str((numeric >> 24) & 0xffffff))
        spell, result = row.get('spellId', 0), row.get('result', 0) if row.get('resultKnown') else None
        key = (kind, source, target, spell, result, row.get('amountKnown') is True and not row.get('malformed'))
        group = self.groups.setdefault(key, dict(kind=kind, source=source, target=target,
            spell=spell, result=result, amountKnown=key[-1], packets=0, amount=0,
            first=str(seq), last=str(seq),firstTimeMs=row.get('receivedAtUnixMs'),lastTimeMs=row.get('receivedAtUnixMs')))
        group['packets'] += 1
        group['last'] = str(seq)
        group['lastTimeMs'] = row.get('receivedAtUnixMs')
        if key[-1]:
            group['amount'] += row.get('amount', 0)
        if kind == 'cast_failure' and source == self.guid:
            self.pending_errors[str(result)] += 1
        if kind == 'death' and target == self.guid:
            self.critical = {'key': f'{self.session}:death:{seq}', 'reason': 'own_death'}

    def observe(self, world, actions, party=None):
        own = world.get('self', {})
        if str(own.get('guid')) != self.guid:
            raise ValueError('combat snapshot body mismatch')
        members = {str(x.get('guid')) for x in (party or {}).get('members', [])}
        members.add(self.guid)
        self.party_membership_known = (party or {}).get('known') is True and (party or {}).get('inGroup') is True
        damage_members = {self.guid}
        if self.party_membership_known:
            for row in (party or {}).get('members',[]):
                try: damage_members.add(decimal(row.get('guid',0)))
                except (ValueError,TypeError): pass
        damage_members.discard('0')
        players = [own, world.get('owner', {}), *world.get('nearbyPlayers', [])]
        rows = {}
        combat = False
        for p in players:
            guid = str(p.get('guid'))
            if guid not in members or p.get('present') is not True:
                continue
            row = {k: p[k] for k in ('guid', 'health', 'maxHealth', 'healthKnown', 'power',
                'maxPower', 'powerKnown', 'alive', 'aliveKnown', 'vitalsAgeMs') if k in p}
            if p.get('healthKnown'):
                row['health'], row['maxHealth'] = p.get('health', 0), p.get('maxHealth', 0)
            if p.get('powerKnown'):
                row['power'], row['maxPower'] = p.get('power', 0), p.get('maxPower', 0)
            if p.get('aliveKnown'):
                row['alive'] = p.get('alive', False)
            if p.get('unitFlagsKnown'):
                row['inCombat'] = bool(p.get('unitFlags', 0) & 0x80000)
                combat |= row['inCombat']
            rows[guid] = row
        # Use the harness's current projected own pose, not the spawn snapshot.
        pos = actions.get('position', {})
        self.scene = dict(players=rows, position={k: pos[k] for k in
            ('known', 'mapKnown', 'mapId', 'x', 'y', 'z', 'ageMs', 'estimated') if k in pos},
            lastCast={k: actions[k] for k in ('lastCastSpellId', 'lastCastTargetGuid', 'lastCastFailure') if k in actions},
            movement={k: actions[k] for k in ('reason', 'navReason', 'targetMovement', 'bodyMovement', 'motionUnconfirmed', 'castPending', 'following') if k in actions})
        if combat and not self.in_combat:
            self.groups = {}
            self.coverage_start = self.cursor
            self.first_sequence, self.event_count, self.unknown_count = None, 0, 0
            self.pending_errors, self.enemy_entries = collections.Counter(), set()
            self.completed = None
            self.damage_members = damage_members
        else:
            self.damage_members.update(damage_members)
        for npc in world.get('nearbyCreatures', []):
            if npc.get('attackVictimKnown') and str(npc.get('attackVictimGuid')) in members:
                if npc.get('entry'):
                    self.enemy_entries.add(str(npc['entry']))
        alive = own.get('alive') is True if own.get('aliveKnown') else None
        if self.was_alive is True and alive is False:
            self.critical = {'key': f'{self.session}:observed_death:{self.fight_serial}', 'reason': 'own_death'}
        self.was_alive = alive
        gate = actions.get('motionUnconfirmed') is True
        if gate and not self.last_gate:
            self.critical = {'key':f'{self.session}:motion_gate:{self.cursor}', 'reason':'movement_unconfirmed'}
        self.last_gate = gate
        if combat and not self.in_combat:
            self.fight_serial += 1
            self.started_at = self.clock()
        if not combat and self.in_combat:
            outcome = 'own_death' if alive is False else 'combat_ended'
            situation = (self.scene['position'].get('mapId'), tuple(sorted(self.enemy_entries)),
                         outcome, tuple(sorted(self.pending_errors)))
            self.completed = dict(key=f'{self.session}:fight:{self.fight_serial}', reason=outcome,
                situation=situation, endedAt=self.clock(), seconds=round(self.clock()-(self.started_at or self.clock()), 1))
        self.in_combat = combat
        self.observed_at = self.clock()

    def unavailable(self, reason):
        self.available, self.reason = False, reason

    def poll(self, rpc, actions, party=None):
        sid = actions.get('sessionId')
        if not sid or str(actions.get('selfGuid')) != self.guid:
            self.available, self.reason = False, 'current_owned_body_unavailable'
            return
        self.bind(sid)
        now = self.clock()
        if now < self.next_poll:
            return
        self.next_poll = now + 1
        try:
            self.observe(rpc.call('buddy-world.snapshot', {}), actions, party)
            if not self.tail_bound:
                head = rpc.call('buddy-world.combat_events',
                    {'sessionId':sid,'afterSequence':'0','limit':1})
                if head.get('sessionId') != sid or decimal(head.get('selfGuid',0)) != self.guid:
                    raise ValueError('combat event body identity mismatch')
                self.cursor = int(decimal(head.get('latestSequence',0)))
                self.coverage_start = self.cursor
                self.gaps.extend(head.get('gaps', []))
                self.tail_bound = True
                self.available, self.reason = True, ''
                return
            # Time-bounded cursor paging catches up on later ticks without gaps.
            until = self.clock() + .25
            while True:
                more = self.ingest(rpc.call('buddy-world.combat_events',
                    {'sessionId':sid, 'afterSequence':str(self.cursor), 'limit':128}))
                if not more or self.clock() >= until:
                    break
        except Exception as error:
            self.available, self.reason = False, 'combat_feed_unavailable: '+str(error)[:240]

    def incoming_damage(self, window, coverage, raw):
        """Aggregate actual incoming DAMAGE before the general event list clips.

        One observer/cursor domain only. Party identity comes from observed
        roster membership during this accumulation window, not nearby players.
        """
        totals = {}
        for group in self.groups.values():
            if group['kind'] not in ('spell_damage','melee_damage') or group['target'] not in self.damage_members:
                continue
            key=(group['target'],group['spell'],group['kind'])
            row=totals.setdefault(key,dict(targetGuid=group['target'],spellId=group['spell'],kind=group['kind'],
                targetRelation='self' if group['target']==self.guid else 'observed_party_member_in_window',
                packetCount=0,knownAmount=0,knownAmountPackets=0,unknownAmountPackets=0,
                firstSequence=group['first'],lastSequence=group['last'],
                firstReceivedAtUnixMs=group['firstTimeMs'],lastReceivedAtUnixMs=group['lastTimeMs'],sources={}))
            source=row['sources'].setdefault(group['source'],dict(sourceGuid=group['source'],sourceKnown=group['source']!='0',
                packetCount=0,knownAmount=0,knownAmountPackets=0,unknownAmountPackets=0))
            for total in (row,source):
                total['packetCount']+=group['packets']
                if group['amountKnown']:
                    total['knownAmount']+=group['amount'];total['knownAmountPackets']+=group['packets']
                else: total['unknownAmountPackets']+=group['packets']
            if int(group['first'])<int(row['firstSequence']):
                row['firstSequence'],row['firstReceivedAtUnixMs']=group['first'],group['firstTimeMs']
            if int(group['last'])>int(row['lastSequence']):
                row['lastSequence'],row['lastReceivedAtUnixMs']=group['last'],group['lastTimeMs']
        ranked=sorted(totals.values(),key=lambda row:(-row['knownAmount'],-row['packetCount'],row['targetGuid'],row['spellId'],row['kind']))
        result=dict(observerSelfGuid=self.guid,sessionId=self.session,available=self.available,reason=self.reason,
            accumulation='since_latest_sampled_party_combat_start' if self.started_at is not None else 'since_listener_attachment',
            windowRemainsOpenAfterSampledCombatEnd=self.completed is not None and not self.in_combat,
            window=window,afterSequence=coverage['afterSequence'],throughSequence=coverage['throughSequence'],
            gaps=coverage['gaps'][-8:],gapCount=len(coverage['gaps']),gapsOmitted=max(0,len(coverage['gaps'])-8),
            completeGapsContextPath=['combat','coverage','gaps'],rawEvidence=raw,
            partyMembershipKnownAtLatestSnapshot=self.party_membership_known,
            partyGuidsObservedInWindow=sorted(self.damage_members-{self.guid}),
            observedSelfDamagePackets=sum(row['packetCount'] for row in ranked if row['targetGuid']==self.guid),
            observedPeerDamagePackets=sum(row['packetCount'] for row in ranked if row['targetGuid']!=self.guid),
            knownSelfAmount=sum(row['knownAmount'] for row in ranked if row['targetGuid']==self.guid),
            knownPeerAmount=sum(row['knownAmount'] for row in ranked if row['targetGuid']!=self.guid),
            ranked=[],omittedRows=len(ranked),
            semantics='Observed incoming damage from ONE client journal, ranked by known amount. '
                'Unknown amounts are counted, never estimated. Counts are packet records, not casts or inferred ticks/AoE. '
                'This sampled accumulation window is not a lifetime or guaranteed complete fight total. '
                'Never add overlapping observers. Static spell names do not establish area, position or causality.')
        names=(self.spell_names if self.spell_names is not None else public_spell_names()) if ranked else {}
        for row in ranked[:8]:
            sources=sorted(row['sources'].values(),key=lambda item:(-item['knownAmount'],-item['packetCount'],item['sourceGuid']))
            row=dict(row,sources=sources[:4],omittedSources=max(0,len(sources)-4))
            if row['spellId'] in names:
                row['spellName']=names[row['spellId']];row['spellNameSource']='static_spell_catalog_label'
            result['ranked'].append(row)
            if len(json.dumps(result,separators=(',',':')).encode())>min(6000,max(1600,self.prompt_bytes//2)):
                result['ranked'].pop();break
        result['omittedRows']=len(ranked)-len(result['ranked'])
        return result

    def context(self):
        focus = str(self.scene.get('lastCast',{}).get('lastCastTargetGuid','0'))
        groups = sorted(self.groups.values(), key=lambda g: (g['source'] != self.guid and g['target'] != self.guid,
            g['target'] != focus, g['kind'] not in ('cast_failure', 'death'), -int(g['last'])))
        result = dict(available=self.available, reason=self.reason, sessionId=self.session,
            selfGuid=self.guid, visibility='internal_model_context_only', inCombat=self.in_combat,
            source='server_packet_journal_and_labelled_state_observations',
            window={'fight':self.fight_serial,'active':self.in_combat,
                    'boundary':'sampled_party_combat_transition','lastOutcome':self.completed},
            eventCount=self.event_count, undecodedEvents=self.unknown_count,
            snapshotAgeMs=None if self.observed_at is None else round((self.clock()-self.observed_at)*1000),
            coverage={'afterSequence':str(self.coverage_start), 'throughSequence':str(self.cursor), 'gaps':self.gaps},
            historyBeforeAttachmentAvailable=True,
            rawEvidence={'method':'buddy-world.combat_events','sessionId':self.session,
                'afterSequence':str(self.coverage_start), 'retained':True, 'scope':'current_body_session',
                'params':{'sessionId':self.session,'afterSequence':str(self.coverage_start),'limit':128}},
            scene=self.scene, events=[], summaryBudgetBytes=self.prompt_bytes,
            omittedGroups=0, semantics='Amounts are observed effects only. Cast start/go is not a confirmed effect. '
                'Counts are packet records, not unique casts; failure broadcasts may duplicate a rejection. '
                'HP changes are state observations, not inferred damage. Read raw cursor ranges to investigate.')
        result['incomingDamage']=self.incoming_damage(result['window'],result['coverage'],result['rawEvidence'])
        for row in groups:
            result['events'].append(row)
            if len(json.dumps(result, separators=(',', ':')).encode()) > self.prompt_bytes:
                result['events'].pop()
                break
        result['omittedGroups'] = len(groups)-len(result['events'])
        return result

    def review_candidate(self):
        if not self.available:
            return None
        if self.critical and self.critical['key'] not in self.reviewed and self.clock() >= self.review_attempts.get(self.critical['key'], 0):
            return dict(self.critical, summary=self.context())
        done = self.completed
        if not done or done['key'] in self.reviewed or self.clock() < self.review_attempts.get(done['key'], 0) or self.clock()-self.last_review_at < self.review_seconds:
            return None
        if done['situation'] in self.seen_situations:
            return None
        return dict(key=done['key'], reason=done['reason'], summary=self.context())

    def reserve_review(self, key):
        if self.clock() < self.review_attempts.get(key, 0):
            return False
        self.review_attempts[key] = self.clock() + self.review_seconds
        return True

    def mark_reviewed(self, key):
        self.reviewed.add(key)
        self.last_review_at = self.clock()
        if self.completed and self.completed['key'] == key:
            self.seen_situations.add(self.completed['situation'])
