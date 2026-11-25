"""Compact recipient facts from fixed public client reads; never select or mutate.

resolve raises RecipientError when class/level or identity is unavailable. A
remote observed player is not an owned client: its skills/session are unknown.
No catalog, arbitrary RPC URL, game command, actor source or prompt is read.
"""
import re


class RecipientError(ValueError):
    pass


def _guid(value):
    text = str(value)
    if re.fullmatch(r'[1-9][0-9]{0,19}', text) is None or int(text) >= 2**64:
        raise RecipientError('recipient_guid_required')
    return text


def _authority(tools, remote):
    if remote:
        if tools.self_guid != '87' or tools.workspace is None:
            raise RecipientError('recipient_gm_workspace_required')
        authorise = getattr(tools, '_authorise_gm_observation', None) or tools._authorise_gm
    else:
        authorise = tools._authorise_owner if tools.owner_allowed() is True else tools._authorise
    try:
        authorise()
    except Exception as error:
        raise RecipientError('recipient_authority_unavailable') from error


def _call(rpc, method):
    try:
        value = rpc.call(method, {})
    except Exception as error:
        raise RecipientError('recipient_public_read_unavailable:' + method) from error
    if not isinstance(value, dict):
        raise RecipientError('recipient_public_read_invalid:' + method)
    return value


def _body(status, guid, expected_sid=None):
    sid = status.get('sessionId')
    if (not isinstance(sid, str) or re.fullmatch(r'[A-Za-z0-9._-]{1,128}', sid) is None
            or str(status.get('selfGuid')) != guid or expected_sid is not None and sid != expected_sid):
        raise RecipientError('recipient_body_changed')
    return sid


def _world(rpc, guid, expected_sid=None):
    before = _call(rpc, 'buddy-actions.status')
    sid = _body(before, guid, expected_sid)
    world = _call(rpc, 'buddy-world.snapshot')
    _body(_call(rpc, 'buddy-actions.status'), guid, sid)
    own = world.get('self')
    if (not isinstance(own, dict) or str(own.get('guid')) != guid or own.get('present') is not True
            or any(key in world and str(world[key]) != value for key, value in
                   (('sessionId', sid), ('selfGuid', guid), ('characterGuid', guid)))):
        raise RecipientError('recipient_world_body_changed')
    return sid, world


def _facts(row):
    if row.get('classKnown') is not True or row.get('levelKnown') is not True:
        raise RecipientError('recipient_class_or_level_unknown')
    cls, level = row.get('classId'), row.get('level')
    if type(cls) is not int or not 1 <= cls <= 32 or type(level) is not int or not 1 <= level < 2**32:
        raise RecipientError('recipient_class_or_level_invalid')
    out = dict(classId=cls, level=level)
    for flag, field, minimum, maximum in (('raceKnown', 'raceId', 1, 32), ('powerKnown', 'powerType', 0, 255)):
        if row.get(flag) is True:
            # Proto3 omits a known mana powerType=0; unknown stays omitted.
            value = row.get(field, 0)
            if type(value) is not int or not minimum <= value <= maximum:
                raise RecipientError('recipient_' + field + '_invalid')
            out[field] = value
    return out


def _skills(world):
    rows = world.get('selfSkills')
    revision = world.get('selfSkillsRevision')
    known_revision = (type(revision) is int and 0 < revision < 2**64 or
                      isinstance(revision, str) and re.fullmatch(r'[1-9][0-9]{0,19}', revision) is not None
                      and int(revision) < 2**64)
    if world.get('selfSkillsKnown') is not True or not known_revision or not isinstance(rows, list) or len(rows) != 128:
        return None, 'complete_own_128_slot_baseline_unavailable'
    slots, values = set(), {}
    for row in rows:
        if not isinstance(row, dict):
            return None, 'invalid_own_skill_baseline'
        slot, skill, value, maximum = (row.get(key, 0) for key in ('slot', 'skillId', 'value', 'maxValue'))
        if (any(type(v) is not int for v in (slot, skill, value, maximum)) or not 0 <= slot < 128
                or slot in slots or not 0 <= skill <= 65535 or not 0 <= value <= maximum <= 65535):
            return None, 'invalid_own_skill_baseline'
        slots.add(slot)
        if skill == 0:
            if value or maximum:
                return None, 'invalid_own_skill_baseline'
        elif skill in values:
            return None, 'invalid_own_skill_baseline'
        else:
            values[skill] = value
    return values, None


def _binding(gm, root, guid, caller_url):
    rows = gm.bindings(root)
    matching = [row for row in rows if str(row.get('guid')) == guid]
    if not matching:
        return None
    if any(not isinstance(row.get('rpc_url'), str) or not row['rpc_url']
           or not isinstance(row.get('name'), str) or not 0 < len(row['name']) <= 128 for row in matching):
        raise RecipientError('recipient_binding_ambiguous')
    identities = {(row['rpc_url'], row['name']) for row in matching}
    if len(identities) != 1:
        raise RecipientError('recipient_binding_ambiguous')
    url, name = next(iter(identities))
    if url == caller_url or any(str(row.get('guid')) != guid and row.get('rpc_url') == url for row in rows):
        raise RecipientError('recipient_binding_ambiguous')
    return url, name


def resolve(tools, recipient_guid: str) -> dict:
    """Resolve only requested identity; unavailable is an error, not GM fallback."""
    if not isinstance(recipient_guid, str):
        raise RecipientError('recipient_guid_required')
    guid = _guid(recipient_guid)
    caller = _guid(tools.self_guid)
    remote = guid != caller
    _authority(tools, remote)
    if not remote:
        sid, world = _world(tools.rpc, guid, tools.session_id)
        facts = _facts(world['self'])
        skills, reason = _skills(world)
        provenance = dict(scope='own_client',recipientSessionId=sid,recipientSessionKnown=True,
                          source='buddy-world.snapshot.self',skillsKnown=skills is not None)
    else:
        # Fence the issuing GM as well as the requested target's own body.
        _body(_call(tools.rpc, 'buddy-actions.status'), caller, tools.session_id)
        gm = tools._helper('gm_tasks')
        binding = _binding(gm, tools.root, guid, tools.rpc.url)
        if binding is not None:
            try:
                rpc = gm.task_rpc(binding[0])
            except Exception as error:
                raise RecipientError('recipient_binding_unavailable') from error
            sid, world = _world(rpc, guid)
            facts = _facts(world['self'])
            skills, reason = _skills(world)
            if _binding(gm, tools.root, guid, tools.rpc.url) != binding:
                raise RecipientError('recipient_binding_changed')
            provenance = dict(scope='configured_own_client',recipientSessionId=sid,recipientSessionKnown=True,
                              source='buddy-world.snapshot.self',nameSource='configured_actor_identity',skillsKnown=skills is not None)
            facts['name'] = binding[1]
        else:
            world = _call(tools.rpc, 'buddy-world.snapshot')
            own = world.get('self')
            if (not isinstance(own, dict) or str(own.get('guid')) != caller or own.get('present') is not True
                    or any(key in world and str(world[key]) != value for key, value in
                           (('sessionId', tools.session_id), ('selfGuid', caller), ('characterGuid', caller)))):
                raise RecipientError('recipient_observer_body_changed')
            nearby = world.get('nearbyPlayers', [])
            if not isinstance(nearby, list):
                raise RecipientError('recipient_public_player_list_invalid')
            candidates = [('owner', world.get('owner'))] + [('nearbyPlayers', row) for row in nearby]
            matching = [(source, row) for source, row in candidates if isinstance(row, dict)
                        and str(row.get('guid')) == guid and row.get('present') is True]
            witnesses = [dict(observerGuid=caller, observerSessionId=tools.session_id,
                              source=source) for source, _ in matching]
            # Local absence is range-limited, not contrary evidence. Only if the
            # caller lacks a present witness, consult fixed configured clients.
            if not matching:
                peer_rows = gm.bindings(tools.root)
                peer_guids = sorted({_guid(row.get('guid')) for row in peer_rows
                                     if isinstance(row, dict) and str(row.get('guid')) not in (caller, guid)})
                for peer_guid in peer_guids:
                    peer_binding = _binding(gm, tools.root, peer_guid, tools.rpc.url)
                    if peer_binding is None:
                        raise RecipientError('recipient_observer_binding_changed')
                    try:
                        peer_rpc = gm.task_rpc(peer_binding[0])
                    except Exception as error:
                        raise RecipientError('recipient_observer_unavailable') from error
                    peer_sid, peer_world = _world(peer_rpc, peer_guid)
                    if _binding(gm, tools.root, peer_guid, tools.rpc.url) != peer_binding:
                        raise RecipientError('recipient_observer_binding_changed')
                    peer_nearby = peer_world.get('nearbyPlayers', [])
                    if not isinstance(peer_nearby, list):
                        raise RecipientError('recipient_public_player_list_invalid')
                    peer_candidates = [('owner', peer_world.get('owner'))] + [('nearbyPlayers', row) for row in peer_nearby]
                    for source, row in peer_candidates:
                        if isinstance(row, dict) and str(row.get('guid')) == guid and row.get('present') is True:
                            matching.append((source, row))
                            witnesses.append(dict(observerGuid=peer_guid, observerSessionId=peer_sid, source=source))
            if not matching:
                raise RecipientError('recipient_not_locally_observed')
            observed = [_facts(row) for _, row in matching]
            facts = dict(observed[0])
            for other in observed[1:]:
                if any(key in facts and facts[key] != value for key, value in other.items()):
                    raise RecipientError('recipient_observations_conflict')
                facts.update(other)
            skills, reason = None, 'remote_own_skill_baseline_not_observed'
            provenance = dict(scope='observer_local_player',observerGuid=caller,observerSessionId=tools.session_id,
                              recipientSessionKnown=False,sources=sorted({source for source, _ in matching}),
                              witnesses=witnesses,skillsKnown=False)
        _body(_call(tools.rpc, 'buddy-actions.status'), caller, tools.session_id)
    _authority(tools, remote)
    if skills is not None:
        facts['skills'] = skills
        provenance['skillsRevision'] = world['selfSkillsRevision']
    else:
        provenance['skillsUnavailableReason'] = reason
    return dict(guid=guid, **facts, provenance=provenance)
