#!/usr/bin/env python3
"""One bounded Deathknell starter step through an already running own client."""

import argparse
import fcntl
import json
import math
import os
from pathlib import Path
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request


ROOT = Path(__file__).resolve().parents[3]
QUEST, MORDO, SARVIS = 363, 1568, 1569
AUTO_ACCEPT = 0x80000


class Error(Exception):
    pass


def number(value, default=-1):
    if type(value) is int:
        return value
    if isinstance(value, str) and value.isdecimal() and len(value) <= 20:
        return int(value)
    return default


def position(value, known="positionKnown"):
    return (isinstance(value, dict) and value.get(known) is True
            and value.get("mapKnown") is True and number(value.get("mapId", 0)) == 0
            and all(type(value.get(k)) in (int, float) and math.isfinite(value[k])
                    and abs(value[k]) < 40000 for k in ("x", "y", "z")))


def distance(a, b):
    return math.sqrt(sum((a[k] - b[k]) ** 2 for k in ("x", "y", "z")))


class RPC:
    def __init__(self, url):
        parsed = urllib.parse.urlsplit(url)
        if (parsed.scheme != "http" or parsed.hostname != "127.0.0.1"
                or parsed.path != "/rpc" or parsed.username or parsed.password
                or parsed.query or parsed.fragment or parsed.port != 8191):
            raise Error("Use Gravemint's loopback RPC at http://127.0.0.1:8191/rpc")
        self.url, self.sequence = url, 0
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def call(self, method, params):
        self.sequence += 1
        body = json.dumps({"jsonrpc": "2.0", "id": self.sequence,
                           "method": method, "params": params}).encode()
        request = urllib.request.Request(self.url, data=body,
                                         headers={"Content-Type": "application/json"})
        try:
            with self.opener.open(request, timeout=12) as response:
                raw = response.read((2 << 20) + 1)
            if len(raw) > 2 << 20:
                raise Error("RPC response exceeds its size limit")
            result = json.loads(raw)
        except (OSError, ValueError, urllib.error.URLError) as error:
            raise Error(f"{method}: response unavailable; reconcile observations before retry") from error
        if result.get("id") != self.sequence or result.get("jsonrpc") != "2.0":
            raise Error("RPC response does not match the request")
        if "error" in result:
            raise Error(f"{method}: {str(result['error'].get('message', 'RPC failed'))[:500]}")
        if not isinstance(result.get("result"), dict):
            raise Error(f"{method}: missing result")
        return result["result"]


class Coach:
    def __init__(self, rpc, stored=None, save=lambda value: None, pause=time.sleep):
        self.rpc, self.save, self.pause = rpc, save, pause
        self.data = stored or {"characterGuid": "6", "character": "Gravemint"}
        if (not isinstance(self.data, dict) or self.data.get("characterGuid") != "6"
                or self.data.get("character") != "Gravemint"):
            raise Error("Saved coach identity does not match Gravemint GUID6")

    def checkpoint(self, phase, **values):
        self.data.update(values, phase=phase, recordedAt=time.time())
        self.save(self.data)
        return self.data

    def observe(self, inspection=False):
        world = self.rpc.call("buddy-world.snapshot", {})
        own = world.get("self", {})
        if (number(own.get("guid")) != 6 or own.get("present") is not True
                or own.get("classKnown") is not True or own.get("classId") != 8
                or own.get("levelKnown") is not True or not 1 <= number(own.get("level")) <= 5
                or not position(own)):
            raise Error("Expected observed level1..5 mage Gravemint GUID6 on map0")
        if (own.get("aliveKnown") is not True or own.get("alive") is not True
                or own.get("healthKnown") is not True or number(own.get("health")) <= 0
                or own.get("unitFlagsKnown") is not True
                or number(own.get("unitFlags", 0)) & 0x80000):
            raise Error("Gravemint must be observed alive and outside combat")
        actions = self.rpc.call("buddy-actions.status", {})
        if (not position(actions.get("position"), "known")
                or any(actions.get(k) for k in ("moving", "motionUnconfirmed", "castPending",
                                                "autoAttacking", "autoAttackUnconfirmed", "following",
                                                "healing", "assisting", "practicing", "interactionOwner"))):
            raise Error("Gravemint's own controller must be confirmed idle")
        quests = self.rpc.call("buddy-quests.discover", {})
        entries = quests.get("questLog", [])
        if (quests.get("questLogKnown") is not True or len(entries) != 25
                or any(not isinstance(q, dict) for q in entries)
                or sorted(number(q.get("slot", 0)) for q in entries) != list(range(25))
                or not isinstance(quests.get("sessionId"), str) or not quests["sessionId"]):
            raise Error("A complete current own quest log and session are required")
        session = quests["sessionId"]
        if self.data.get("sessionId") and self.data["sessionId"] != session and not inspection:
            # A new body requires a read-only reconciliation run before actions.
            self.checkpoint("new_session_requires_inspection", pendingSessionId=session)
            raise Error("New client session observed; rerun inspect before another step")
        if inspection:
            self.data.pop("pendingSessionId", None)
        self.data.update(sessionId=session, selfObservation=own,
                         controllerPosition=actions["position"],
                         questLogRevision=quests.get("questLogRevision", "0"), questLog=entries)
        return quests, actions

    def has_starter(self, quests):
        return next((q for q in quests["questLog"] if number(q.get("questId")) == QUEST), None)

    def pending_route(self, quests):
        quest = self.has_starter(quests)
        visible = [n["creature"] for n in quests.get("nearby", [])
                   if number(n.get("creature", {}).get("entry")) == SARVIS]
        receipt = quests.get("lastAcceptance", {})
        return self.checkpoint("awaiting_verified_deathknell_route", starterQuestInOwnLog=True,
                               starterQuestComplete=quest.get("complete", False),
                               sarvisObserved=bool(visible), sarvisObservations=visible,
                               lastAcceptance=receipt,
                               acceptanceReceiptConflict=receipt.get("status") == "SERVER_REJECTED",
                               nextRequirement="Verified client-asset route up crypt stairs to observed Sarvis; no Horde kill/loot/XP receipt yet")

    def run(self, step=False):
        quests, actions = self.observe(inspection=not step)
        if self.has_starter(quests):
            return self.pending_route(quests)
        if not step:
            return self.checkpoint("starter_offer_not_requested", starterQuestInOwnLog=False)
        if self.data.get("acceptAttempted"):
            return self.checkpoint("acceptance_requires_reconciliation")
        if self.data.get("offerAttempted"):
            return self.checkpoint("offer_requires_reconciliation")
        candidates = [n.get("creature", {}) for n in quests.get("nearby", [])
                      if number(n.get("creature", {}).get("entry")) == MORDO]
        if len(candidates) != 1:
            return self.checkpoint("mordo_not_uniquely_observed")
        npc = candidates[0]
        p = actions["position"]
        if (npc.get("present") is not True or npc.get("aliveKnown") is not True
                or npc.get("alive") is not True or not position(npc)
                or number(npc.get("guid")) >> 48 != 0xf130
                or number(npc.get("guid")) >> 24 & 0xffffff != MORDO
                or number(npc.get("npcFlags", 0)) & 2 == 0
                or number(npc.get("movementFlags", 0)) & 0x080000ff
                or distance(p, npc) > 8 or abs(p["z"] - npc["z"]) > 1.5):
            return self.checkpoint("mordo_outside_existing_safe_approach")
        self.checkpoint("requesting_observed_starter_offer", offerAttempted=True)
        result = self.rpc.call("buddy-quests.offer", {"sessionId": quests["sessionId"], "npcGuid": npc["guid"]})
        offer = result.get("state", {}).get("offer", {})
        q = next((q for q in offer.get("quests", []) if number(q.get("questId")) == QUEST
                  and q.get("available") is True), None)
        if (not q or offer.get("npcGuid") != npc["guid"]
                or number(offer.get("revision")) <= 0 or number(offer.get("expiresInMs")) <= 0):
            return self.checkpoint("starter_offer_not_confirmed")
        self.checkpoint("starter_offer_observed", offer=offer)
        # Hello/details can auto-accept. Let the authoritative own log catch up
        # before deciding whether any explicit accept packet is necessary.
        for _ in range(30):
            self.pause(.05)
            quests, _ = self.observe()
            if self.has_starter(quests):
                return self.pending_route(quests)
        if number(q.get("flags", 0)) & AUTO_ACCEPT:
            return self.checkpoint("autoaccept_log_confirmation_unavailable")
        params = {"sessionId": quests["sessionId"], "npcGuid": npc["guid"],
                  "questId": QUEST, "offerRevision": offer["revision"]}
        # Persist before sending: an ambiguous transport cannot authorize a retry.
        self.checkpoint("accepting_observed_starter", acceptAttempted=True)
        self.rpc.call("buddy-quests.accept", params)
        for _ in range(30):
            self.pause(.05)
            quests, _ = self.observe()
            if self.has_starter(quests):
                return self.pending_route(quests)
        return self.checkpoint("acceptance_requires_reconciliation")


def private_save(path, data):
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, prefix=".thrall-", delete=False) as out:
        temporary = Path(out.name)
        try:
            os.fchmod(out.fileno(), 0o600)
            json.dump(data, out, indent=2)
            out.write("\n")
            out.flush()
            os.fsync(out.fileno())
            os.replace(temporary, path)
        finally:
            temporary.unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("inspect", "step"))
    parser.add_argument("--rpc-url", default="http://127.0.0.1:8191/rpc")
    parser.add_argument("--state", type=Path, default=ROOT / "data/autonomy/thrall/coach-status.json")
    args = parser.parse_args()
    path = args.state.absolute()
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    lock = os.open(path.with_suffix(".lock"), os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        try:
            fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
            with os.fdopen(fd) as source:
                stored = json.load(source)
        except FileNotFoundError:
            stored = None
        coach = Coach(RPC(args.rpc_url), stored, lambda data: private_save(path, data))
        status = coach.run(step=args.command == "step")
        print(json.dumps({k: status[k] for k in ("character", "characterGuid", "phase",
                         "starterQuestInOwnLog", "sarvisObserved", "nextRequirement") if k in status}))
        return 0
    except (Error, OSError, ValueError) as error:
        print(f"thrall: {error}", file=sys.stderr)
        return 1
    finally:
        os.close(lock)


if __name__ == "__main__":
    sys.exit(main())
