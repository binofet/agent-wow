"""Read current Lua stipend choices; native mail owns exact claims and receipts."""
import hashlib
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parent.parent
_loader = importlib.machinery.SourceFileLoader('stipend_policy_runtime', str(ROOT/'dev/task-runtime'))
_spec = importlib.util.spec_from_loader(_loader.name, _loader)
runtime = importlib.util.module_from_spec(_spec)
_loader.exec_module(runtime)
Error = runtime.Error


def _decimal(value, maximum):
    return (isinstance(value, str) and re.fullmatch('[1-9][0-9]*', value) is not None
            and len(value) <= len(str(maximum)) and int(value) <= maximum)


def _mail_id(value):
    # uint32 protojson is normally numeric; retain its exact decimal identity
    # through Lua just like the native uint64 list/mailbox fields.
    return str(value) if type(value) is int and 0 <= value <= 2**32-1 else value


def _facts(mail):
    if not isinstance(mail, dict):
        raise Error('observed_mail_state_required')
    rows = mail.get('stipends', [])
    # Only remove unrelated receipt fields; never filter candidate rows.
    if isinstance(rows, list) and len(rows) <= 50:
        rows = [dict(mailId=_mail_id(row.get('mailId', 0)), copper=row.get('copper', 0))
                if isinstance(row, dict) else row for row in rows]
    return dict(sessionId=mail.get('sessionId'), listKnown=mail.get('listKnown', False),
                listCurrent=mail.get('listCurrent', False), listRevision=mail.get('listRevision', '0'),
                mailboxGuid=mail.get('mailboxGuid', '0'), claimReserved=mail.get('claimReserved', False),
                operationInFlight=mail.get('operationInFlight', False), stipends=rows)


def _accepted(rpc, identity):
    view = rpc.call('session.policy_source', identity)
    source, revision = view.get('source'), view.get('revision')
    generation = str(view.get('generation', ''))
    limit = view.get('sourceLimitBytes', runtime.MAX_SOURCE)
    if (view.get('sessionId') != identity['sessionId'] or str(view.get('characterGuid')) != identity['characterGuid']
            or not isinstance(source, str) or not source or not isinstance(revision, str)
            or re.fullmatch('[a-f0-9]{64}', revision) is None
            or hashlib.sha256(source.encode()).hexdigest() != revision
            or not _decimal(generation, 2**64-1) or type(limit) is not int or limit <= 0
            or len(source.encode()) > limit):
        raise Error('current_stipend_policy_source_required')
    return source, revision, generation, limit


def choose(rpc, mail, self_guid, *, bridge=None):
    try:
        facts = _facts(mail)
        identity = dict(sessionId=facts['sessionId'], characterGuid=str(self_guid))
        if (not isinstance(identity['sessionId'], str) or not 1 <= len(identity['sessionId']) <= 128
                or not _decimal(identity['characterGuid'], 2**64-1)):
            raise Error('current_mail_body_required')
        source, revision, generation, limit = _accepted(rpc, identity)
        wrapped = ('local policy=(function()\n'+source+'\nend)()\nreturn {decide=function(input) '
            'if type(policy.stipend_selection)~="function" then return {available=false} end '
            'return {available=true,decision=policy.stipend_selection(input)} end}\n')
        bridge = bridge or runtime.Bridge(os.environ.get('BUDDY_LUAPOLICY_BINARY',
            os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY', ROOT/'data/bin/luapolicy')), ROOT/'modules',
            source_bytes=max(limit, len(wrapped.encode())), execution_ms=1000)
        # This is the maintained full-source consumer bound, not a change to
        # standalone 5ms evaluation or the default 32KiB value frame.
        result = bridge.evaluate(wrapped, dict(sessionId=identity['sessionId'], selfGuid=identity['characterGuid'], mail=facts))
        if not isinstance(result, dict) or result.get('available') is not True:
            raise Error('stipend_selection_export_unavailable')
        decision = result.get('decision')
        if not isinstance(decision, dict):
            raise Error('bounded_stipend_policy_decision_required')
        if decision.get('kind') == 'wait':
            reason = decision.get('reason')
            if set(decision) != {'kind', 'reason'} or not isinstance(reason, str) or not 1 <= len(reason) <= 128:
                raise Error('bounded_stipend_policy_wait_required')
        elif decision.get('kind') == 'claim':
            if (set(decision) != {'kind', 'sessionId', 'listRevision', 'mailboxGuid', 'mailId'}
                    or not isinstance(facts['sessionId'], str) or not 1 <= len(facts['sessionId']) <= 128
                    or not _decimal(decision.get('listRevision'), 2**64-1)
                    or not _decimal(decision.get('mailboxGuid'), 2**64-1)
                    or not _decimal(decision.get('mailId'), 2**32-1)
                    or any(decision[key] != facts[key] for key in ('sessionId', 'listRevision', 'mailboxGuid'))
                    or not isinstance(facts['stipends'], list) or len(facts['stipends']) > 50
                    or sum(isinstance(row, dict) and row.get('mailId') == decision['mailId'] for row in facts['stipends']) != 1):
                raise Error('lua_must_select_one_exact_observed_stipend')
        else:
            raise Error('stipend_policy_claim_or_wait_required')
        selection = dict(decision, policySHA256=revision,
                         _proof=dict(revision=revision, generation=generation, selfGuid=identity['characterGuid'], mailFacts=facts))
        if not current(rpc, selection, self_guid):
            raise Error('stipend_policy_body_or_mail_changed')
        return selection
    except Exception as error:
        raise Error('stipend_policy_unavailable: ' + str(error)) from error


def current(rpc, selection, self_guid):
    """Rebind accepted source/body and exact mail facts; this grants no write."""
    try:
        proof = selection['_proof']
        facts = proof['mailFacts']
        identity = dict(sessionId=facts['sessionId'], characterGuid=str(self_guid))
        observed = _facts(rpc.call('buddy-mail.state', {}))
        if (identity['characterGuid'] != proof['selfGuid'] or
                json.dumps(observed, sort_keys=True, allow_nan=False) != json.dumps(facts, sort_keys=True, allow_nan=False)):
            return False
        # The native claim rechecks mail terms. Keep the accepted publication
        # read after these observations, nearest the caller's dispatch boundary.
        _, revision, generation, _ = _accepted(rpc, identity)
        attached = rpc.call('session.attach', identity)
        policy = attached.get('policy', {})
        if (revision != proof['revision'] or generation != proof['generation'] or revision != selection['policySHA256']
                or attached.get('sessionId') != identity['sessionId']
                or str(attached.get('characterGuid')) != identity['characterGuid']
                or policy.get('revision') != revision or str(policy.get('generation')) != generation):
            return False
        return True
    except Exception:
        return False


def _mailbox_custody(mail):
    if not isinstance(mail, dict):
        raise Error('current_mail_identity_required')
    facts = dict(sessionId=mail.get('sessionId'), claimReserved=mail.get('claimReserved', False),
                 operationInFlight=mail.get('operationInFlight', False))
    if (not isinstance(facts['sessionId'], str) or not 1 <= len(facts['sessionId']) <= 128
            or type(facts['claimReserved']) is not bool or type(facts['operationInFlight']) is not bool):
        raise Error('same_session_idle_mail_state_required')
    return facts


def _mailbox_frame(mail, world, actions, self_guid):
    custody = _mailbox_custody(mail)
    if not isinstance(world, dict) or not isinstance(actions, dict) or not isinstance(world.get('self'), dict):
        raise Error('observed_mailbox_world_and_actions_required')
    own, pos = world['self'], actions.get('position', {})
    # Older responses may omit these fields. Actual reported identity may not
    # contradict the Mail/World/accepted-source body that brackets this pose.
    if ('sessionId' in actions and actions['sessionId'] != custody['sessionId'] or
            'selfGuid' in actions and (not _decimal(actions['selfGuid'], 2**64-1)
                or actions['selfGuid'] != str(self_guid) or actions['selfGuid'] != own.get('guid'))):
        raise Error('reported_actions_identity_changed')
    if not isinstance(pos, dict):
        raise Error('observed_mailbox_position_required')
    boxes = world.get('mailboxes', [])
    if isinstance(boxes, list) and len(boxes) <= 32:
        boxes = [dict(guid=row.get('guid', '0'), entry=row.get('entry', 0), present=row.get('present', False),
                      typeKnown=row.get('typeKnown', False), gameObjectType=row.get('gameObjectType', 0),
                      mapKnown=row.get('mapKnown', False), mapId=row.get('mapId', 0),
                      positionKnown=row.get('positionKnown', False), x=row.get('x', 0), y=row.get('y', 0), z=row.get('z', 0),
                      positionRevision=row.get('positionRevision', '0')) if isinstance(row, dict) else row for row in boxes]
    body = dict(guid=own.get('guid', '0'), present=own.get('present', False), mapKnown=own.get('mapKnown', False),
                mapId=own.get('mapId', 0), positionKnown=own.get('positionKnown', False),
                positionRevision=own.get('positionRevision', '0'), aliveKnown=own.get('aliveKnown', False),
                alive=own.get('alive', False), unitFlagsKnown=own.get('unitFlagsKnown', False), unitFlags=own.get('unitFlags', 0))
    position = dict(known=pos.get('known', False), mapKnown=pos.get('mapKnown', False), mapId=pos.get('mapId', 0),
                    x=pos.get('x', 0), y=pos.get('y', 0), z=pos.get('z', 0), estimated=pos.get('estimated', False),
                    revision=pos.get('revision', '0'))
    actor = {key: actions.get(key, False) for key in ('moving', 'motionUnconfirmed', 'castPending', 'autoAttacking',
             'autoAttackRequested', 'autoAttackUnconfirmed', 'traveling', 'practicing')}
    actor.update(interactionOwner=actions.get('interactionOwner', ''), position=position)
    return dict(sessionId=custody['sessionId'], selfGuid=str(self_guid), visitId=world.get('visitRevision', '0'),
                self=body, actions=actor, mailboxes=boxes, mail=custody), custody


def mailbox_target(rpc, world, actions, self_guid, *, bridge=None):
    """Read the accepted mailbox choice; no implicit native target fallback."""
    try:
        mail = rpc.call('buddy-mail.state', {})
        frame, custody = _mailbox_frame(mail, world, actions, self_guid)
        identity = dict(sessionId=frame['sessionId'], characterGuid=frame['selfGuid'])
        if not _decimal(identity['characterGuid'], 2**64-1):
            raise Error('current_mailbox_body_required')
        source, revision, generation, limit = _accepted(rpc, identity)
        wrapped = ('local policy=(function()\n'+source+'\nend)()\nreturn {decide=function(input) '
            'if type(policy.mailbox_target)~="function" then return {available=false} end '
            'return {available=true,decision=policy.mailbox_target(input)} end}\n')
        bridge = bridge or runtime.Bridge(os.environ.get('BUDDY_LUAPOLICY_BINARY',
            os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY', ROOT/'data/bin/luapolicy')), ROOT/'modules',
            source_bytes=max(limit, len(wrapped.encode())), execution_ms=1000)
        result = bridge.evaluate(wrapped, frame)
        if not isinstance(result, dict) or result.get('available') is not True:
            raise Error('mailbox_target_export_unavailable')
        decision = result.get('decision')
        if not isinstance(decision, dict):
            raise Error('bounded_mailbox_target_decision_required')
        if decision.get('kind') == 'wait':
            reason = decision.get('reason')
            if set(decision) != {'kind', 'reason'} or not isinstance(reason, str) or not 1 <= len(reason) <= 128:
                raise Error('bounded_mailbox_target_wait_required')
        elif decision.get('kind') == 'list':
            if (set(decision) != {'kind', 'sessionId', 'selfGuid', 'visitId', 'mailboxGuid'}
                    or any(decision[key] != frame[key] for key in ('sessionId', 'selfGuid', 'visitId'))
                    or not _decimal(decision.get('visitId'), 2**64-1)
                    or not _decimal(decision.get('mailboxGuid'), 2**64-1)
                    or not isinstance(frame['mailboxes'], list) or len(frame['mailboxes']) > 32
                    or sum(isinstance(row, dict) and row.get('guid') == decision['mailboxGuid'] for row in frame['mailboxes']) != 1):
                raise Error('lua_must_select_one_exact_observed_mailbox')
        else:
            raise Error('mailbox_target_list_or_wait_required')
        selection = dict(decision, policySHA256=revision,
                         _proof=dict(revision=revision, generation=generation, frame=frame, mailCustody=custody))
        if decision['kind'] == 'list' and not mailbox_current(rpc, selection, self_guid):
            raise Error('mailbox_source_body_visit_or_pose_changed')
        return selection
    except Exception as error:
        raise Error('mailbox_policy_unavailable: ' + str(error)) from error


def mailbox_current(rpc, selection, self_guid):
    """Bounded asynchronous preflight; List has no atomic source/visit token."""
    try:
        proof, expected = selection['_proof'], selection['_proof']['frame']
        if str(self_guid) != expected['selfGuid']:
            return False
        mail = rpc.call('buddy-mail.state', {})
        world = rpc.call('buddy-world.snapshot', {})
        actions = rpc.call('buddy-actions.status', {})
        frame, custody = _mailbox_frame(mail, world, actions, self_guid)
        if (json.dumps(frame, sort_keys=True, allow_nan=False) != json.dumps(expected, sort_keys=True, allow_nan=False)
                or custody != proof['mailCustody']):
            return False
        identity = dict(sessionId=frame['sessionId'], characterGuid=frame['selfGuid'])
        _, revision, generation, _ = _accepted(rpc, identity)
        attached = rpc.call('session.attach', identity)
        policy = attached.get('policy', {})
        return (revision == proof['revision'] == selection['policySHA256'] and generation == proof['generation']
                and attached.get('sessionId') == identity['sessionId'] and str(attached.get('characterGuid')) == identity['characterGuid']
                and policy.get('revision') == revision and str(policy.get('generation')) == generation)
    except Exception:
        return False
