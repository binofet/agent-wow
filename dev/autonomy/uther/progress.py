#!/usr/bin/env python3
"""Bounded, resumable self-only Northshire mage coach; no login or credentials."""
import argparse
import copy
import fcntl
import ipaddress
import json
import math
import os
from pathlib import Path
import stat
import tempfile
import time
from urllib import error, parse, request


class Error(Exception):
    pass


def number(value):
    if type(value) is int and value >= 0:
        return value
    if isinstance(value, str) and value.isascii() and value.isdecimal():
        return int(value)
    raise Error("Invalid nonnegative protocol integer")


def url_checked(value):
    parsed = parse.urlsplit(value)
    try:
        local = parsed.hostname == "localhost" or ipaddress.ip_address(parsed.hostname).is_loopback
        port = parsed.port
    except (ValueError, TypeError):
        raise Error("RPC URL must be an explicit local HTTP /rpc endpoint") from None
    if (not local or parsed.scheme != "http" or parsed.path != "/rpc" or not port
            or parsed.username or parsed.password or parsed.query or parsed.fragment):
        raise Error("RPC URL must be an explicit local HTTP /rpc endpoint")
    return value


class RPC:
    def __init__(self, url, deadline=None):
        self.url, self.deadline, self.sequence = url_checked(url), deadline, 0
        self.opener = request.build_opener(request.ProxyHandler({}))

    def call(self, method, params):
        remaining = 40 if self.deadline is None else min(40, self.deadline - time.monotonic())
        if remaining <= 0:
            raise Error("Bounded run deadline reached")
        self.sequence += 1
        payload = json.dumps({"jsonrpc": "2.0", "id": self.sequence,
                              "method": method, "params": params}).encode()
        req = request.Request(self.url, data=payload, headers={"Content-Type": "application/json"})
        try:
            with self.opener.open(req, timeout=remaining) as response:
                body = response.read(524289)
            if len(body) > 524288:
                raise Error("RPC response exceeds coach limit")
            result = json.loads(body)
        except (OSError, ValueError, error.URLError) as failure:
            raise Error("RPC response unavailable: " + str(failure)[:240]) from None
        if not isinstance(result, dict) or result.get("id") != self.sequence:
            raise Error("RPC response identity is unconfirmed")
        if result.get("error"):
            fault = result["error"]
            detail = fault.get("data", {}) if isinstance(fault, dict) else {}
            message = detail.get("grpc_message") if isinstance(detail, dict) else None
            fallback = fault.get("message", "unknown") if isinstance(fault, dict) else fault
            raise Error("RPC rejected request: " + str(message or fallback)[:240])
        if not isinstance(result.get("result"), dict):
            raise Error("RPC result is not an object")
        return result["result"]


class Store:
    def __init__(self, folder):
        self.folder = Path(folder).absolute()
        self.folder.mkdir(mode=0o700, parents=True, exist_ok=True)
        for path in (self.folder, *self.folder.parents):
            info = path.lstat()
            if stat.S_ISLNK(info.st_mode) or not stat.S_ISDIR(info.st_mode):
                raise Error("Coach state directory must not contain symlinks")
        info = self.folder.stat()
        if info.st_uid != os.geteuid() or info.st_mode & 0o077:
            raise Error("Coach state directory must be private and owned (mode0700)")
        self.path = self.folder / "progress.json"

    def private_open(self, path, flags):
        fd = os.open(path, flags | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077:
            os.close(fd)
            raise Error("Coach state files must be private and owned (mode0600)")
        return fd

    def load(self):
        try:
            with os.fdopen(self.private_open(self.path, os.O_RDONLY), "r") as source:
                raw = source.read(4194305)
        except FileNotFoundError:
            return None
        if len(raw) > 4194304:
            raise Error("Coach state exceeds its size limit")
        try:
            value = json.loads(raw)
        except ValueError:
            raise Error("Coach state is invalid JSON") from None
        if not isinstance(value, dict):
            raise Error("Coach state is not an object")
        return value

    def save(self, value):
        if self.path.exists() or self.path.is_symlink():
            os.close(self.private_open(self.path, os.O_RDONLY))
        fd, name = tempfile.mkstemp(prefix=".progress-", dir=self.folder)
        try:
            with os.fdopen(fd, "w") as output:
                os.fchmod(output.fileno(), 0o600)
                json.dump(value, output, separators=(",", ":"), allow_nan=False)
                output.write("\n")
                output.flush()
                os.fsync(output.fileno())
            os.replace(name, self.path)
            directory = os.open(self.folder, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
        finally:
            if os.path.exists(name):
                os.unlink(name)


def position(value, known="positionKnown"):
    return (isinstance(value, dict) and value.get(known) is True and value.get("mapKnown") is True
            and number(value.get("mapId", 0)) == 0
            and all(type(value.get(axis)) in (int, float) and math.isfinite(value[axis])
                    for axis in ("x", "y", "z")))


def northshire(value):
    return (-9599 <= value["x"] <= -8400 and -700 <= value["y"] <= 200 and 40 <= value["z"] <= 180)


def giver_entry(guid):
    guid = number(guid)
    return (guid >> 24) & 0xffffff if guid >> 48 == 0xf130 and guid & 0xffffff else 0


def active_log(quests):
    rows = quests.get("questLog")
    if quests.get("questLogKnown") is not True or not isinstance(rows, list) or len(rows) != 25:
        raise Error("Own complete25-slot quest log is unknown")
    slots, active = set(), {}
    for row in rows:
        if not isinstance(row, dict):
            raise Error("Own quest log contains an invalid slot")
        slot, qid = number(row.get("slot", 0)), number(row.get("questId", 0))
        if slot >= 25 or slot in slots or qid > 2**31 - 1 or qid and qid in active:
            raise Error("Own quest log has duplicate or invalid slots/quests")
        slots.add(slot)
        if qid:
            active[qid] = row
    return active


class Coach:
    def __init__(self, rpc, *, guid=7, name="Aveline", url="http://127.0.0.1:8192/rpc",
                 stored=None, save=lambda value: None, now=time.time):
        if type(guid) is not int or not 0 < guid < 2**48 or not isinstance(name, str) or not name.isalpha() or not 2 <= len(name) <= 12:
            raise Error("Expected GUID/name is invalid")
        self.rpc, self.save, self.now = rpc, save, now
        identity = {"guid": str(guid), "expectedName": name, "classId": 8, "url": url_checked(url)}
        self.data = copy.deepcopy(stored) if stored else {
            "version": 1, "identity": identity, "reservations": [], "pending": None,
            "evidence": {"rewards": {}, "acceptances": {}, "levels": []}, "status": {}}
        if self.data.get("version") != 1 or self.data.get("identity") != identity:
            raise Error("Coach state belongs to a different profile")
        history = self.data.get("reservations")
        if (not isinstance(history, list) or len(history) > 128
                or any(not isinstance(row, dict) or row.get("sequence") != index + 1
                       or not isinstance(row.get("method"), str) or not isinstance(row.get("params"), dict)
                       for index, row in enumerate(history))):
            raise Error("Coach reservation history is invalid")
        pending = self.data.get("pending")
        if pending is not None and (type(pending) is not int or not 1 <= pending <= len(history)):
            raise Error("Coach pending reservation is invalid")
        evidence = self.data.get("evidence", {})
        if (not isinstance(evidence, dict) or not isinstance(evidence.get("rewards"), dict)
                or not isinstance(evidence.get("acceptances"), dict) or not isinstance(evidence.get("levels"), list)):
            raise Error("Coach evidence is invalid")
        for qid, row in evidence["rewards"].items():
            if qid not in ("783", "5261", "33") or not isinstance(row, dict) or not self.reward_valid(row.get("receipt", {}), int(qid)):
                raise Error("Saved reward is missing actual server evidence")
        self.guid = guid
        self.status = {"identity": identity, "phase": "starting", "observationsCurrent": False,
                       "nameVerification": "profile_label_only_rpc_exposes_guid_and_class", "xpEvidence": "level_only"}

    def phase(self, value, detail=None):
        self.status["phase"] = value
        if detail is not None:
            self.status["detail"] = detail
        else:
            self.status.pop("detail", None)

    def read(self):
        quests = self.rpc.call("buddy-quests.discover", {})
        world = self.rpc.call("buddy-world.snapshot", {})
        actions = self.rpc.call("buddy-actions.status", {})
        loot = self.rpc.call("buddy-loot.state", {})
        final = self.rpc.call("buddy-quests.discover", {})
        session = final.get("sessionId")
        if not isinstance(session, str) or not 1 <= len(session) <= 128 or session != quests.get("sessionId"):
            raise Error("Quest body changed while observing")
        player = world.get("self", {})
        if not isinstance(player, dict) or number(player.get("guid", 0)) != self.guid:
            raise Error("RPC character GUID does not match expected profile")
        if player.get("classKnown") is not True or number(player.get("classId", 0)) != 8:
            raise Error("RPC character is not an observed mage")
        if player.get("levelKnown") is not True or not 1 <= number(player.get("level", 0)) <= 5:
            raise Error("Own observed level is outside the1..5 starter scope")
        active = active_log(final)
        number(final.get("questLogRevision", 0))
        rows = final.get("nearby", [])
        if not isinstance(rows, list) or len(rows) > 64:
            raise Error("Questgiver discovery is outside its bound")
        effective = actions.get("position", {})
        givers = {}
        for row in rows:
            c = row.get("creature", {}) if isinstance(row, dict) else {}
            entry = number(c.get("entry", 0))
            if (entry not in (196, 197, 823) or giver_entry(c.get("guid", 0)) != entry
                    or any(c.get(key) is not True for key in ("present", "aliveKnown", "alive"))
                    or not position(c) or not northshire(c) or number(c.get("npcFlags", 0)) & 2 == 0
                    or number(c.get("movementFlags", 0)) & 0x080000ff
                    or number(c.get("unitFlags", 0)) & 0x80000):
                continue
            trajectory = c.get("trajectory", {})
            if trajectory.get("known") is True and (trajectory.get("stopped") is not True or trajectory.get("transportRelative") is True):
                continue
            if position(effective, "known"):
                distance = math.sqrt(sum((c[axis] - effective[axis])**2 for axis in ("x", "y", "z")))
                candidate = {"guid": str(number(c["guid"])), "entry": entry, "distance": distance}
                if entry in givers and givers[entry]["guid"] != candidate["guid"]:
                    raise Error("Multiple observed copies of a starter questgiver")
                givers[entry] = candidate
        return {"world": world, "player": player, "quests": final, "actions": actions,
                "loot": loot, "session": session, "active": active, "givers": givers}

    @staticmethod
    def reward_valid(receipt, qid, npc=None, revision=None):
        return (isinstance(receipt, dict) and number(receipt.get("questId", 0)) == qid
                and receipt.get("status") in ("REWARD_CONFIRMED", 2)
                and receipt.get("serverRewardObserved") is True and receipt.get("logRemovalConfirmed") is True
                and giver_entry(receipt.get("npcGuid", 0)) == {783: 197, 5261: 196, 33: 196}.get(qid)
                and (npc is None or str(number(receipt.get("npcGuid", 0))) == str(npc))
                and (revision is None or number(receipt.get("offerRevision", 0)) == number(revision)))

    def observe(self, context):
        quests, actions = context["quests"], context["actions"]
        receipt = quests.get("lastReward", {})
        qid = number(receipt.get("questId", 0))
        if qid in (783, 5261, 33) and qid not in context["active"] and self.reward_valid(receipt, qid):
            self.data["evidence"]["rewards"][str(qid)] = {"sessionId": context["session"], "receipt": copy.deepcopy(receipt)}
        acceptance = quests.get("lastAcceptance", {})
        accepted = number(acceptance.get("questId", 0))
        if accepted in context["active"] and acceptance.get("status") in ("CONFIRMED_IN_LOG", 2):
            self.data["evidence"]["acceptances"][str(accepted)] = copy.deepcopy(acceptance)
        level = number(context["player"]["level"])
        levels = self.data["evidence"]["levels"]
        if not levels or levels[-1]["level"] != level:
            levels.append({"level": level, "observedAt": self.now()})
        self.data["evidence"]["combat"] = {key: copy.deepcopy(actions[key]) for key in (
            "practiceKills", "practicePulls", "fireballDamageEvents", "fireballDamageTotal",
            "practicePhase", "reason", "navAvailable", "navReason") if key in actions}
        self.data["evidence"]["loot"] = copy.deepcopy(context["loot"])
        self.status.update(observationsCurrent=True, questSession=context["session"], level=level,
                           activeQuestIds=sorted(context["active"]),
                           confirmedRewardQuestIds=sorted(int(key) for key in self.data["evidence"]["rewards"]),
                           practicePhase=actions.get("practicePhase", ""), navReason=actions.get("navReason", ""),
                           position=copy.deepcopy(actions.get("position", {})))

    def reconcile(self, context):
        pending = self.data.get("pending")
        if pending is None:
            return True
        row = self.data["reservations"][pending - 1]
        method, params = row["method"], row["params"]
        qid = number(params.get("questId", 0))
        quests = context["quests"]
        same = row.get("session") == context["session"]
        resolved = False
        if method in ("buddy-quests.accept", "buddy-quests.details") and qid in context["active"]:
            resolved = True
        elif method == "buddy-quests.claim_reward":
            returned = row.get("response", {}).get("state", {})
            receipt = returned.get("lastReward", {})
            if (qid not in context["active"] and returned.get("sessionId") == row.get("session")
                    and self.reward_valid(receipt, qid, params["npcGuid"], params["offerRevision"])):
                self.data["evidence"]["rewards"][str(qid)] = {
                    "sessionId": row["session"], "receipt": copy.deepcopy(receipt)}
            proof = self.data["evidence"]["rewards"].get(str(qid), {})
            resolved = (proof.get("sessionId") == row.get("session") and qid not in context["active"]
                        and self.reward_valid(proof.get("receipt", {}), qid,
                                              params["npcGuid"], params["offerRevision"]))
        elif method in ("buddy-quests.offer", "buddy-quests.details"):
            offer = quests.get("offer", {})
            resolved = (same and self.offer_valid(offer, params["npcGuid"])
                        and (method.endswith(".offer") or offer.get("source") == "quest_details"
                             and any(number(q.get("questId", 0)) == qid for q in offer.get("quests", []))))
        elif method == "buddy-quests.turn_in":
            offer = quests.get("turnInOffer", {})
            resolved = (same and number(offer.get("questId", 0)) == qid
                        and str(number(offer.get("npcGuid", 0))) == params["npcGuid"]
                        and number(offer.get("revision", 0)) > 0)
        elif method == "buddy-actions.practice":
            resolved = context["actions"].get("practicing", False) is params["enabled"]
            if params["enabled"]:
                resolved |= (row.get("stage") == "xp" and number(context["player"]["level"]) >= 2
                             or row.get("stage") == "wolves" and self.complete(context["active"].get(33)))
        elif method == "buddy-loot.enable":
            resolved = context["loot"].get("enabled", False) is params["enabled"]
        elif method == "buddy-actions.travel_quest":
            npc = context["givers"].get(giver_entry(params["npcGuid"]))
            resolved = (same and npc is not None and npc["guid"] == params["npcGuid"] and npc["distance"] <= 4
                        and not context["actions"].get("moving") and not context["actions"].get("motionUnconfirmed"))
            response, before = row.get("response", {}), row.get("fromPosition", {})
            current = context["actions"].get("position", {})
            returned = response.get("position", {})
            partial = (same and npc is not None and npc["guid"] == params["npcGuid"]
                and response.get("reason") == "quest_travel_lease_changed_or_expiring"
                and response.get("navAvailable") is True and response.get("moving") is not True
                and response.get("motionUnconfirmed") is not True and not row.get("error")
                and not context["actions"].get("moving") and not context["actions"].get("motionUnconfirmed")
                and not context["actions"].get("interactionOwner")
                and position(before, "known") and position(current, "known") and position(returned, "known")
                and sum((current[axis] - before[axis])**2 for axis in ("x", "y", "z")) > 0.25
                and sum((current[axis] - returned[axis])**2 for axis in ("x", "y", "z")) <= 1
                and npc["distance"] < row.get("fromDistance", 0) - 0.5)
            if partial:
                row["safePartialTravelObserved"] = True
                resolved = True
        if not resolved and method in ("buddy-quests.offer", "buddy-quests.details", "buddy-quests.turn_in"):
            response = row.get("response", {})
            returned = response.get("state", {})
            old_offer = returned.get("turnInOffer" if method.endswith(".turn_in") else "offer", {})
            historical = (response.get("serverResponseReceived") is True
                and returned.get("sessionId") == row.get("session")
                and str(number(old_offer.get("npcGuid", 0))) == params["npcGuid"]
                and number(old_offer.get("revision", 0)) > 0)
            if method.endswith(".details"):
                historical = (historical and old_offer.get("source") == "quest_details"
                    and any(number(q.get("questId", 0)) == qid for q in old_offer.get("quests", [])))
            elif method.endswith(".turn_in"):
                historical = historical and number(old_offer.get("questId", 0)) == qid
            if historical:
                row["receivedQueryReconciled"] = True
                resolved = True
        if resolved:
            row["outcome"] = "reconciled_from_received_response" if row.get("receivedQueryReconciled") else "reconciled_from_own_state"
            row["reconciledAt"] = self.now()
            self.data["pending"] = None
            return True
        detail = row.get("error") or row.get("response", {}).get("reason") or "No matching own-state effect or server response"
        phase = "blocked_unconfirmed_request"
        if method == "buddy-actions.travel_quest":
            phase = "blocked_navigation"
        elif method == "buddy-actions.practice":
            phase = "blocked_practice"
        self.phase(phase, {"method": method, "questId": qid, "reason": detail, "sequence": pending})
        return False

    @staticmethod
    def complete(row):
        return bool(row) and (row.get("complete") is True or number(row.get("state", 0)) & 1 != 0)

    @staticmethod
    def offer_valid(offer, npc):
        return (isinstance(offer, dict) and str(number(offer.get("npcGuid", 0))) == str(npc)
                and number(offer.get("revision", 0)) > 0 and number(offer.get("expiresInMs", 0)) > 0)

    def operation(self, context, method, params, stage):
        return {"method": method, "params": params, "stage": stage,
                "session": context["session"], "level": number(context["player"]["level"]),
                "logRevision": str(number(context["quests"].get("questLogRevision", 0)))}

    def safety(self, context):
        player, actions = context["player"], context["actions"]
        if any(player.get(key) is not True for key in ("present", "aliveKnown", "alive", "unitFlagsKnown")):
            self.phase("blocked_dead_or_unknown_character", "No death recovery is implemented")
            return False
        effective = actions.get("position", {})
        if not position(player) or not position(effective, "known") or not northshire(effective):
            self.phase("blocked_outside_known_northshire_position")
            return False
        if (player.get("movementFlagsKnown") is not True or number(player.get("movementFlags", 0)) & ~0x1ff
                or number(player.get("extraMovementFlags", 0))
                or number(player.get("unitFlags", 0)) & 0x0bd40005):
            self.phase("blocked_unsupported_character_state")
            return False
        if actions.get("motionUnconfirmed") or actions.get("autoAttackUnconfirmed"):
            self.phase("blocked_unconfirmed_action_stop")
            return False
        if any(actions.get(key) for key in ("following", "healing", "assisting", "traveling")):
            self.phase("blocked_other_action_controller_active")
            return False
        if context["quests"].get("actionInFlight") or actions.get("interactionOwner"):
            self.phase("waiting_for_existing_interaction")
            return False
        return True

    def travel(self, context, npc, stage):
        # Quest interaction itself accepts4yd. Short approach has additional
        # height/corner limits, so use the complete navmesh route outside4yd.
        if npc["distance"] <= 4:
            return None
        return dict(self.operation(context, "buddy-actions.travel_quest", {"npcGuid": npc["guid"]}, stage),
                    fromPosition={key: context["actions"]["position"][key] for key in
                                  ("known", "mapKnown", "x", "y", "z")}, fromDistance=npc["distance"])

    def queried(self, context, entry, stage):
        npc = context["givers"].get(entry)
        if npc is None:
            return False
        for row in self.data["reservations"]:
            if (row["method"] == "buddy-quests.offer" and row.get("outcome") in
                    ("reconciled_from_own_state", "reconciled_from_received_response")
                    and row.get("stage") == stage and row.get("session") == context["session"]
                    and row.get("level") == number(context["player"]["level"])
                    and row.get("logRevision") == str(number(context["quests"].get("questLogRevision", 0)))
                    and row["params"].get("npcGuid") == npc["guid"]):
                offer = row.get("response", {}).get("state", {}).get("offer", {})
                wanted = {783} if stage == "starter" else {33, 5261}
                # A known positive query may be refreshed after expiry. Only
                # known empty/non-goal offers suppress another query at the
                # same observed level/log revision.
                if not any(number(q.get("questId", 0)) in wanted and q.get("available") is True
                           and q.get("repeatable") is not True for q in offer.get("quests", [])):
                    return True
        return False

    def offered(self, context, entries, wanted, stage):
        quests, active = context["quests"], context["active"]
        offer = quests.get("offer", {})
        for entry in entries:
            npc = context["givers"].get(entry)
            if npc is None:
                continue
            if self.offer_valid(offer, npc["guid"]):
                rows = offer.get("quests", [])
                if not isinstance(rows, list) or len(rows) > 32:
                    raise Error("Actual quest offer is invalid")
                for q in rows:
                    qid = number(q.get("questId", 0))
                    if (qid not in wanted or qid in active or {783: 823, 5261: 823, 33: 196}[qid] != entry
                            or q.get("available") is not True or q.get("repeatable") is True or q.get("acceptRequested") is True):
                        continue
                    route = self.travel(context, npc, stage)
                    if route:
                        return route
                    method = "buddy-quests.accept" if offer.get("source") == "quest_details" else "buddy-quests.details"
                    return self.operation(context, method, {"sessionId": context["session"], "npcGuid": npc["guid"],
                        "offerRevision": str(number(offer["revision"])), "questId": qid}, stage)
            if not self.queried(context, entry, stage):
                route = self.travel(context, npc, stage)
                return route or self.operation(context, "buddy-quests.offer",
                    {"sessionId": context["session"], "npcGuid": npc["guid"]}, stage)
        return None

    def reward(self, context, qid, entry):
        npc = context["givers"].get(entry)
        if npc is None:
            self.phase("blocked_missing_observed_reward_giver", {"questId": qid, "entry": entry})
            return None
        route = self.travel(context, npc, "reward:" + str(qid))
        if route:
            return route
        offer = context["quests"].get("turnInOffer", {})
        if (self.offer_valid(offer, npc["guid"]) and number(offer.get("questId", 0)) == qid
                and offer.get("source") == "reward_offer" and offer.get("canComplete") is True
                and offer.get("rewardRequested") is not True):
            choices = offer.get("choices", [])
            if not isinstance(choices, list) or len(choices) > 6:
                raise Error("Reward choice list is invalid")
            valid, indices = [], set()
            for item in choices:
                idx, item_id, count = number(item.get("index", 0)), number(item.get("itemId", 0)), number(item.get("count", 0))
                if idx >= 6 or idx in indices or not 0 < item_id < 2**32 or not 0 < count < 2**32:
                    raise Error("Reward choice is invalid")
                indices.add(idx)
                valid.append({"choiceIndex": idx, "expectedItemId": item_id})
            preferred = [item for item in valid if qid == 33 and item["expectedItemId"] == 80]
            if len(valid) > 1 and len(preferred) != 1:
                self.phase("blocked_reward_choice", {"questId": qid, "choices": valid})
                return None
            choice = preferred[0] if preferred else valid[0] if valid else {"choiceIndex": 0, "expectedItemId": 0}
            return self.operation(context, "buddy-quests.claim_reward", dict(choice,
                sessionId=context["session"], npcGuid=npc["guid"], questId=qid,
                offerRevision=str(number(offer["revision"])), noChoice=not valid), "reward:" + str(qid))
        if (number(offer.get("questId", 0)) == qid and offer.get("source") == "requirements"
                and offer.get("canComplete") is not True):
            self.phase("blocked_server_quest_requirements", {"questId": qid, "offer": copy.deepcopy(offer)})
            return None
        return self.operation(context, "buddy-quests.turn_in", {"sessionId": context["session"],
            "npcGuid": npc["guid"], "questId": qid,
            "questLogRevision": str(number(context["quests"].get("questLogRevision", 0)))}, "reward:" + str(qid))

    def practice_failed(self, context):
        actions = context["actions"]
        reason, phase = actions.get("reason", ""), actions.get("practicePhase", "")
        if (not actions.get("practicing") and (phase not in ("", "disabled", "level_two_observed", "quest_33_completion_observed")
                or reason.startswith("practice_") and reason not in ("practice_disabled", "practice_level_two_observed", "practice_quest_33_completion_observed"))):
            suffix = phase or reason.removeprefix("practice_")
            if not suffix or any(not (char.isascii() and (char.isalnum() or char == "_")) for char in suffix):
                suffix = "unknown_failure"
            self.phase("blocked_practice_" + suffix, {"reason": reason, "practicePhase": phase,
                                                     "navReason": actions.get("navReason", "")})
            return True
        return False

    def practice(self, context, stage):
        actions = context["actions"]
        if actions.get("practicing") is True:
            self.phase("waiting_for_wolf_practice" if stage == "wolves" else "waiting_for_level_two",
                       {"practicePhase": actions.get("practicePhase", ""), "level": number(context["player"]["level"])})
            return None
        if self.practice_failed(context):
            return None
        if actions.get("knownFireball") is not True:
            self.phase("blocked_training_required_fireball", "Trainer movement is not implemented")
            return None
        return self.operation(context, "buddy-actions.practice", {"enabled": True}, stage)

    def plan(self, context):
        if "33" in self.data["evidence"]["rewards"]:
            self.phase("complete", "Own quest33 reward and log removal observed; no further progression scheduled")
            return None
        if not self.safety(context):
            return None
        active, actions, loot = context["active"], context["actions"], context["loot"]
        for qid in (783, 5261, 33):
            row = active.get(qid)
            if row and (row.get("failed") is True or number(row.get("state", 0)) & 2):
                self.phase("blocked_failed_own_quest", {"questId": qid})
                return None
        in_combat = number(context["player"].get("unitFlags", 0)) & 0x80000 != 0
        if in_combat or actions.get("castPending") or actions.get("moving") and not actions.get("practicing"):
            self.phase("waiting_for_current_combat_or_movement", {"practicePhase": actions.get("practicePhase", "")})
            return None
        if context["loot"].get("phase", "idle") not in ("idle", "disabled"):
            self.phase("waiting_for_loot_receipt_and_release", {"phase": context["loot"].get("phase")})
            return None
        if 33 in active:
            if self.complete(active[33]):
                if actions.get("practicing"):
                    return self.operation(context, "buddy-actions.practice", {"enabled": False}, "finish_wolves")
                if loot.get("enabled"):
                    return self.operation(context, "buddy-loot.enable", {"enabled": False}, "finish_wolves")
                return self.reward(context, 33, 196)
            if self.practice_failed(context):
                return None
            if not loot.get("enabled"):
                return self.operation(context, "buddy-loot.enable", {"enabled": True}, "wolves")
            return self.practice(context, "wolves")
        if actions.get("practicing"):
            self.phase("waiting_for_level_two", {"practicePhase": actions.get("practicePhase", "")})
            return None
        if loot.get("enabled"):
            return self.operation(context, "buddy-loot.enable", {"enabled": False}, "no_wolf_quest")
        if 783 in active:
            return self.reward(context, 783, 197)
        if 5261 in active:
            return self.reward(context, 5261, 196)
        if 7 not in active and "783" not in self.data["evidence"]["rewards"] and "5261" not in self.data["evidence"]["rewards"]:
            plan = self.offered(context, (823, 197, 196), {783}, "starter")
            if plan:
                return plan
            self.phase("blocked_starter_quest_not_offered", "Need actual quest783 offer or own prerequisite evidence")
            return None
        plan = self.offered(context, (196, 823), {33, 5261}, "wolves_offer")
        if plan:
            return plan
        if number(context["player"]["level"]) == 1 and self.queried(context, 196, "wolves_offer"):
            return self.practice(context, "xp")
        self.phase("blocked_wolf_quest_not_offered", {"level": number(context["player"]["level"]),
            "observedGiverEntries": sorted(context["givers"]), "unsupportedQuestIds": sorted(set(active) - {783, 5261, 33})})
        return None

    def execute(self, plan, context):
        fresh = self.read()
        self.observe(fresh)
        current = self.plan(fresh)
        if self.intent(current) != self.intent(plan):
            self.phase("waiting_for_fresh_plan_after_state_change")
            return
        plan = current
        if len(self.data["reservations"]) >= 128:
            self.phase("blocked_reservation_capacity")
            return
        row = dict(copy.deepcopy(plan), sequence=len(self.data["reservations"]) + 1,
                   reservedAt=self.now(), outcome="reserved_before_request")
        self.data["reservations"].append(row)
        self.data["pending"] = row["sequence"]
        self.save(self.data)
        try:
            if plan["method"] == "buddy-actions.travel_quest":
                lease = self.rpc.call("buddy-actions.acquire_interaction", {"owner": "quests", "leaseMs": 30000})
                token, state = lease.get("token"), lease.get("state", {})
                if not isinstance(token, str) or not 1 <= len(token) <= 128 or state.get("interactionOwner") != "quests":
                    raise Error("Travel lease ownership is unconfirmed")
                try:
                    if state.get("moving") or state.get("motionUnconfirmed"):
                        raise Error("Travel lease did not confirm a stopped position")
                    guarded = self.read()
                    # The owned lease is expected while this operation revalidates.
                    if guarded["actions"].get("interactionOwner") != "quests":
                        raise Error("Travel lease ownership changed before travel")
                    guarded["actions"] = dict(guarded["actions"], interactionOwner="")
                    self.observe(guarded)
                    latest = self.plan(guarded)
                    if self.intent(latest) != self.intent(plan):
                        raise Error("Own quest, body or observed giver changed before travel")
                    row["fromPosition"], row["fromDistance"] = latest["fromPosition"], latest["fromDistance"]
                    self.save(self.data)
                    result = self.rpc.call(plan["method"], dict(plan["params"], token=token))
                finally:
                    self.rpc.call("buddy-actions.release_interaction", {"owner": "quests", "token": token})
            else:
                result = self.rpc.call(plan["method"], plan["params"])
            row["response"] = self.receipt_response(result)
            row["outcome"] = "response_received_reconciliation_required"
            self.phase("requested_action_reconciling", {"method": plan["method"], "sequence": row["sequence"]})
        except Error as failure:
            row["error"] = str(failure)
            row["outcome"] = "unconfirmed_no_retry"
            self.phase("blocked_unconfirmed_request", {"method": plan["method"], "reason": str(failure), "sequence": row["sequence"]})
        self.save(self.data)

    @staticmethod
    def intent(plan):
        return None if plan is None else {key: value for key, value in plan.items() if key not in ("fromPosition", "fromDistance")}

    @staticmethod
    def receipt_response(result):
        if not isinstance(result, dict):
            raise Error("Mutation returned no result object")
        if "state" not in result:
            return copy.deepcopy(result)
        state = result.get("state", {})
        return {key: copy.deepcopy(result[key]) for key in ("transportWritten", "serverResponseReceived") if key in result} | {
            "state": {key: copy.deepcopy(state[key]) for key in ("sessionId", "offer", "turnInOffer", "lastAcceptance", "lastReward",
                     "questLogKnown", "questLogRevision", "questLog") if key in state}}

    def step(self):
        self.status["observationsCurrent"] = False
        try:
            context = self.read()
            self.observe(context)
            if self.reconcile(context):
                plan = self.plan(context)
                if plan:
                    self.execute(plan, context)
        except Error as failure:
            self.status["observationsCurrent"] = False
            self.phase("blocked_observation", str(failure))
        self.status["observedAt"] = self.now()
        self.status["confirmedRewardQuestIds"] = sorted(int(key) for key in self.data["evidence"]["rewards"])
        self.data["status"] = copy.deepcopy(self.status)
        self.save(self.data)
        return copy.deepcopy(self.status)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", nargs="?", choices=("step", "run", "status"), default="step")
    parser.add_argument("--expected-guid", type=int, default=7)
    parser.add_argument("--expected-name", default="Aveline", help="Profile label; RPC verifies GUID and mage class, not the name")
    parser.add_argument("--url", default="http://127.0.0.1:8192/rpc")
    parser.add_argument("--state-dir", type=Path, default=Path(__file__).resolve().parents[3] / "data/autonomy/uther/coach")
    parser.add_argument("--max-steps", type=int, default=30)
    parser.add_argument("--max-seconds", type=float, default=300)
    parser.add_argument("--interval", type=float, default=2)
    args = parser.parse_args(argv)
    if (not 1 <= args.max_steps <= 300 or not math.isfinite(args.max_seconds) or not 1 <= args.max_seconds <= 600
            or not math.isfinite(args.interval) or not 0 <= args.interval <= 10):
        parser.error("Use1..300steps,1..600seconds, and0..10seconds between steps")
    store = Store(args.state_dir)
    if args.command == "status":
        coach = Coach(RPC(args.url), guid=args.expected_guid, name=args.expected_name,
                      url=args.url, stored=store.load())
        print(json.dumps(coach.data.get("status", {}), indent=2))
        return 0
    with os.fdopen(store.private_open(store.folder / "lock", os.O_RDWR | os.O_CREAT), "r+") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Error("Another bounded coach run holds this state directory") from None
        deadline = time.monotonic() + args.max_seconds
        coach = Coach(RPC(args.url, deadline), guid=args.expected_guid, name=args.expected_name,
                      url=args.url, stored=store.load(), save=store.save)
        count = 1 if args.command == "step" else args.max_steps
        status = coach.status
        for index in range(count):
            status = coach.step()
            if status["phase"] == "complete" or status["phase"].startswith("blocked_") or time.monotonic() >= deadline:
                break
            if index + 1 < count:
                time.sleep(min(args.interval, max(0, deadline - time.monotonic())))
        print(json.dumps(status, indent=2))
        return 0 if status["phase"] == "complete" else 2 if status["phase"].startswith("blocked_") else 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (Error, OSError) as failure:
        print(json.dumps({"phase": "blocked_coach", "detail": str(failure)}))
        raise SystemExit(2)
