#!/usr/bin/env python3
"""Prepare or create a bounded Horde roster through normal public APIs only."""

import argparse
from collections import Counter
from copy import deepcopy
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import socket
import stat
import string
import subprocess
import sys
import tempfile
import time


ROOT = Path(__file__).resolve().parents[3]
CLASSES = {"warrior": 5, "paladin": 5, "hunter": 4, "rogue": 4, "priest": 5,
           "shaman": 5, "mage": 4, "warlock": 4, "druid": 4}
RACES = {"warrior": ["orc", "tauren", "troll", "undead"], "paladin": ["blood-elf"],
         "hunter": ["orc", "troll", "tauren", "blood-elf"],
         "rogue": ["undead", "orc", "troll", "blood-elf"],
         "priest": ["undead", "troll", "blood-elf"], "shaman": ["orc", "tauren", "troll"],
         "mage": ["undead", "troll", "blood-elf"], "warlock": ["orc", "undead", "blood-elf"],
         "druid": ["tauren"]}
PROFESSIONS = [("tailoring", "enchanting"), ("mining", "jewelcrafting"),
               ("herbalism", "inscription"), ("mining", "blacksmithing"),
               ("mining", "engineering"), ("skinning", "leatherworking"),
               ("herbalism", "alchemy")]


class Error(Exception):
    pass


def private_directory(path):
    path = Path(path)
    for parent in [path, *path.parents]:
        if parent.is_symlink():
            raise Error("Private directory path must not contain symlinks")
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = path.stat()
    if not path.is_dir() or info.st_uid != os.geteuid() or info.st_mode & 0o077:
        raise Error("Expected a private owned directory with mode0700")


def read_private_text(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd) as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077:
            raise Error("Expected an owned private regular file")
        raw = source.read((2 << 20) + 1)
    if len(raw) > 2 << 20:
        raise Error("Private JSON exceeds its size limit")
    return raw


def read_private(path):
    return json.loads(read_private_text(path))


def write_private(path, value):
    path = Path(path)
    private_directory(path.parent)
    if path.is_symlink():
        raise Error("Refusing a symlinked private file")
    if path.exists():
        read_private_text(path)
    with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, prefix=".roster-", delete=False) as out:
        temporary = Path(out.name)
        try:
            os.fchmod(out.fileno(), 0o600)
            json.dump(value, out, indent=2)
            out.write("\n")
            out.flush()
            os.fsync(out.fileno())
            os.replace(temporary, path)
            parent = os.open(path.parent, os.O_RDONLY)
            try:
                os.fsync(parent)
            finally:
                os.close(parent)
        finally:
            temporary.unlink(missing_ok=True)


def make_plan():
    order = ["mage", "paladin", "druid", "hunter", "warrior", "priest", "shaman", "rogue", "warlock"]
    counts = Counter(order)
    while len(order) < 40:
        for kind, maximum in CLASSES.items():
            if counts[kind] < maximum:
                order.append(kind)
                counts[kind] += 1
    seen, rows = Counter(), []
    for slot, kind in enumerate(order, 1):
        n = seen[kind]
        seen[kind] += 1
        role = "ranged-damage"
        if kind == "warrior":
            role = "tank" if n < 2 else "melee-damage"
        elif kind == "paladin":
            role = "tank" if n < 2 else "healer" if n < 4 else "melee-damage"
        elif kind == "druid":
            role = "tank" if n < 2 else "healer" if n == 2 else "ranged-damage"
        elif kind == "priest":
            role = "healer" if n < 3 else "ranged-damage"
        elif kind == "shaman":
            role = "healer" if n < 2 else "ranged-damage" if n < 4 else "melee-damage"
        elif kind == "rogue":
            role = "melee-damage"
        encoded = string.ascii_lowercase[(slot - 1) // 26] + string.ascii_lowercase[(slot - 1) % 26]
        rows.append({"slot": slot, "account": f"WCHORDE{slot:02d}",
                     "name": "Gravemint" if slot == 1 else "Vorth" + encoded,
                     "race": RACES[kind][n % len(RACES[kind])], "class": kind,
                     "gender": "female" if slot % 2 else "male",
                     "profile": "horde-thrall" if slot == 1 else f"horde-{slot:02d}",
                     "rpcPort": 8191 if slot == 1 else 8300 + slot,
                     "intendedRole": role, "primaryProfessionsPlanned": list(PROFESSIONS[(slot - 1) % len(PROFESSIONS)]),
                     "secondaryProfessionsPlanned": ["cooking", "fishing", "first-aid"],
                     "professionLearningConfirmed": False, "levelStageCap": 60, "runtimeLaunchAllowed": False})
    return rows


PLAN = make_plan()
PLAN_HASH = hashlib.sha256(json.dumps(PLAN, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def new_state():
    rows = []
    for planned in PLAN:
        anchor = planned["slot"] == 1
        rows.append({"plan": deepcopy(planned), "accountPhase": "existing-reference" if anchor else "planned",
                     "authPhase": "protected-live-account" if anchor else "planned",
                     "characterPhase": "confirmed-existing" if anchor else "planned",
                     "guid": "6" if anchor else None, "receipts": [], "profilesWritten": anchor})
    return {"schemaVersion": 1, "cohort": "horde-40", "planHash": PLAN_HASH, "rows": rows,
            "deathKnightsDeferred": {"countedInCurrent40": False, "creationAllowed": False,
                                     "requires": "Actual normal account prerequisite eligibility; no grant or local filter bypass"}}


def validate_state(value):
    if (not isinstance(value, dict) or type(value.get("schemaVersion")) is not int
            or value["schemaVersion"] != 1 or value.get("cohort") != "horde-40"
            or value.get("planHash") != PLAN_HASH or not isinstance(value.get("rows"), list)
            or len(value["rows"]) != 40
            or value.get("deathKnightsDeferred") != new_state()["deathKnightsDeferred"]):
        raise Error("Roster state or immutable plan identity does not match")
    guids = set()
    receipt_kinds = ("account-bootstrap", "normal-auth-login", "normal-character-list",
                     "normal-character-create", "post-create-normal-character-list")
    for expected, row in zip(PLAN, value["rows"]):
        if (not isinstance(row, dict) or row.get("plan") != expected
                or json.dumps(row["plan"], sort_keys=True) != json.dumps(expected, sort_keys=True)):
            raise Error("Roster row changed from the bounded immutable plan")
        guid = row.get("guid")
        if guid is not None and (not isinstance(guid, str) or len(guid) > 15 or not re.fullmatch(r"[1-9][0-9]*", guid)
                                 or not 0 < int(guid) < 2**48 or guid in guids):
            raise Error("Roster GUID is invalid")
        if guid:
            guids.add(guid)
        if (type(row.get("profilesWritten")) is not bool or not isinstance(row.get("receipts"), list)
                or len(row["receipts"]) > 50):
            raise Error("Roster progress fields are invalid")
        for receipt in row["receipts"]:
            if (not isinstance(receipt, dict) or set(receipt) != {"at", "kind", "value"}
                    or type(receipt["at"]) not in (int, float) or not 0 < receipt["at"] < 1e12
                    or receipt["kind"] not in receipt_kinds or not isinstance(receipt["value"], dict)):
                raise Error("Roster receipt is invalid")
        if expected["slot"] == 1:
            if row != new_state()["rows"][0]:
                raise Error("Protected Gravemint anchor changed")
            continue
        if (row.get("accountPhase") not in ("planned", "intent", "created", "unknown", "collision")
                or row.get("authPhase") not in ("planned", "intent", "confirmed", "unknown")
                or row.get("characterPhase") not in ("planned", "intent", "confirmed", "unknown", "rejected", "conflict")):
            raise Error("Roster phase is invalid")
        if (row["accountPhase"] != "created" and
                (row["authPhase"] != "planned" or row["characterPhase"] != "planned")
                or row["characterPhase"] != "planned" and row["authPhase"] != "confirmed"):
            raise Error("Roster phases do not describe a possible operation sequence")
        if row["accountPhase"] == "created" and not any(
                receipt["kind"] == "account-bootstrap" and receipt["value"] == {
                    "account": expected["account"], "classification": "created"}
                for receipt in row["receipts"]):
            raise Error("Created account lacks its exact successful bootstrap receipt")
        confirmed = row["characterPhase"] == "confirmed"
        if bool(guid) != confirmed or row["profilesWritten"] and not confirmed:
            raise Error("Only an enumerated confirmed character can have a GUID or profile")
        if confirmed:
            if (not character_matches(expected, row.get("actualCharacter"))
                    or row["actualCharacter"]["guid"] != guid
                    or not isinstance(row.get("actualRealm"), dict) or type(row["actualRealm"].get("id")) is not int
                    or row["actualRealm"]["id"] != 1):
                raise Error("Confirmed character is missing matching server enumeration evidence")
        elif "actualCharacter" in row or "actualRealm" in row:
            raise Error("Unconfirmed row must not contain confirmed character evidence")
    return value


def character_matches(plan, character):
    if not isinstance(character, dict):
        return False
    normalize = lambda text: str(text).lower().replace(" ", "-")
    guid = character.get("guid")
    return (character.get("name") == plan["name"] and normalize(character.get("race")) == plan["race"]
            and normalize(character.get("class")) == plan["class"]
            and normalize(character.get("gender")) == plan["gender"]
            and isinstance(guid, str) and len(guid) <= 15 and re.fullmatch(r"[1-9][0-9]*", guid) is not None
            and 0 < int(guid) < 2**48)


def classify_account_receipt(username, raw):
    text = re.sub(r"\x1b\[[0-9;?]*[A-Za-z]", "", raw.decode(errors="replace"))
    if re.search(r"Account created:\s*" + re.escape(username) + r"(?:\s|$)", text):
        return "created"
    if re.search(r"Account[^\r\n]*\b" + re.escape(username) + r"\b[^\r\n]*already exist", text, re.IGNORECASE):
        return "collision"
    return "unknown"


def summary(state):
    confirmed = [r for r in state["rows"] if r["characterPhase"] in ("confirmed", "confirmed-existing")]
    return {"plannedCharacters": 40, "actualConfirmedCharacters": len(confirmed),
            "newAccountsCreated": sum(r["accountPhase"] == "created" for r in state["rows"]),
            "existingAccountsReferenced": 1, "newCharacterGuids": [r["guid"] for r in confirmed if r["plan"]["slot"] != 1],
            "classCountsPlanned": dict(Counter(r["class"] for r in PLAN)),
            "classCountsConfirmed": dict(Counter(r["plan"]["class"] for r in confirmed)),
            "roleIntentions": dict(Counter(r["intendedRole"] for r in PLAN)),
            "primaryProfessionIntentions": dict(Counter(p for r in PLAN for p in r["primaryProfessionsPlanned"])),
            "professionsTrainedByTool": 0, "runtimesStartedByTool": 0,
            "liveApplyEnabled": True, "liveApplyRequiresAcknowledgement": True, "levelStageCap": 60,
            "unresolvedSlots": [r["plan"]["slot"] for r in state["rows"] if r["accountPhase"] in ("intent", "unknown", "collision")
                                or r["authPhase"] in ("intent", "unknown") or r["characterPhase"] in ("intent", "unknown", "conflict", "rejected")]}


class RealmAdapter:
    def __init__(self, root):
        self.root = Path(root).absolute()
        self.binary = self.root / "bin/agent-wow"

    def paths(self, row):
        slot = row["plan"]["slot"]
        if type(slot) is not int or not 2 <= slot <= 40 or row["plan"] != PLAN[slot - 1]:
            raise Error("Protected existing account is outside the creation adapter")
        config = self.root / "config/autonomy/thrall/roster" / f"slot-{slot:02d}"
        data = self.root / "data/autonomy/thrall/roster-clients" / f"slot-{slot:02d}"
        return config, data

    def guard(self, row):
        config, _ = self.paths(row)
        pid_path = self.root / "config/buddies" / row["plan"]["profile"] / "buddy.pid"
        if pid_path.exists() or pid_path.is_symlink():
            record = read_private(pid_path)
            pid = record.get("pid") if isinstance(record, dict) else None
            if type(pid) is not int or pid <= 1:
                raise Error("Unrecognized profile process record; account operations blocked")
            try:
                process = (Path("/proc") / str(pid) / "stat").read_text().rsplit(") ", 1)[1].split()[0]
            except FileNotFoundError:
                process = None
            if process not in (None, "Z", "X"):
                raise Error("Profile has a running process; do not authenticate or enumerate its account")
        # Also reject an untracked normal body using this exact config path.
        for entry in Path("/proc").iterdir():
            if not entry.name.isdecimal():
                continue
            try:
                argv = (entry / "cmdline").read_bytes().split(b"\0")
            except (FileNotFoundError, PermissionError, ProcessLookupError):
                continue
            if str(config / "client.yaml").encode() in argv and b"play" in argv:
                raise Error("An untracked body uses this roster account; operations blocked")

    def prepare(self, row):
        self.guard(row)
        config, data = self.paths(row)
        private_directory(config)
        private_directory(data)
        auth = config / "auth.json"
        if auth.exists() or auth.is_symlink():
            creds = read_private(auth)
            if (not isinstance(creds, dict) or set(creds) != {"username", "password"}
                    or creds["username"] != row["plan"]["account"]
                    or not isinstance(creds["password"], str) or not re.fullmatch(r"[A-Za-z0-9]{16}", creds["password"])):
                raise Error("Private credential identity mismatch")
        else:
            if row["accountPhase"] != "planned":
                raise Error("Credentials are missing after account intent; do not replace them")
            password = "".join(secrets.choice(string.ascii_letters + string.digits) for _ in range(16))
            write_private(auth, {"username": row["plan"]["account"], "password": password})
        yaml = config / "client.yaml"
        body = (f"config_dir: {json.dumps(str(config))}\ndata_dir: {json.dumps(str(data))}\nmodule_dir: {json.dumps(str(config / 'modules'))}\n"
                f"log_level: info\nauthserver:\n  host: 127.0.0.1\n  port: 3724\n"
                f"worldrpc:\n  host: 127.0.0.1\n  port: {row['plan']['rpcPort']}\n")
        if yaml.exists() or yaml.is_symlink():
            if read_private_text(yaml) != body:
                raise Error("Existing profile configuration differs")
        else:
            fd = os.open(yaml, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
            with os.fdopen(fd, "w") as out:
                out.write(body)
                out.flush()
                os.fsync(out.fileno())

    def create_account(self, row):
        if row["accountPhase"] != "intent":
            raise Error("Account command requires a durable creation intent")
        self.guard(row)
        config, _ = self.paths(row)
        creds = read_private(config / "auth.json")
        if (not isinstance(creds, dict) or set(creds) != {"username", "password"}
                or creds["username"] != row["plan"]["account"]
                or not isinstance(creds["password"], str)
                or not re.fullmatch(r"[A-Za-z0-9]{16}", creds["password"])):
            raise Error("Private account command identity is invalid")
        username, password = creds["username"], creds["password"]
        ids = subprocess.run(["docker", "ps", "--filter", "label=com.docker.compose.project=warcraft-buddy-realm",
                              "--filter", "label=com.docker.compose.service=worldserver", "--format", "{{.ID}}"],
                             capture_output=True, text=True, check=True, timeout=8).stdout.split()
        if len(ids) != 1 or not re.fullmatch(r"[0-9a-f]{12,64}", ids[0]):
            raise Error("Expected exactly one known worldserver console")
        # Same documented account-create interface and Docker attach transport
        # submitted to approval review. Never persist or print its raw TTY:
        # it may echo this password or a concurrent operator's credentials.
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
            connection.settimeout(5)
            connection.connect("/var/run/docker.sock")
            connection.sendall((f"POST /containers/{ids[0]}/attach?stream=1&stdin=1&stdout=1&stderr=1 HTTP/1.1\r\n"
                                "Host: localhost\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Length: 0\r\n\r\n").encode())
            head = b""
            while b"\r\n\r\n" not in head and len(head) < 16384:
                part = connection.recv(4096)
                if not part:
                    raise Error("Console attach closed before command")
                head += part
            if b"\r\n\r\n" not in head or head.split(b"\r\n", 1)[0].split()[:2] != [b"HTTP/1.1", b"101"]:
                raise Error("Console attach did not upgrade")
            # Credentials are generated/stored privately by prepare(), not
            # passed in shell arguments or displayed in receipts.
            connection.sendall(f"account create {username} {password}\n".encode())
            connection.settimeout(.25)
            raw, deadline = b"", time.monotonic() + 4
            while time.monotonic() < deadline and len(raw) < 65536:
                try:
                    part = connection.recv(min(4096, 65536 - len(raw)))
                    if not part:
                        break
                    raw += part
                except socket.timeout:
                    pass
        return classify_account_receipt(username, raw)

    def cli(self, row, args):
        self.guard(row)
        config, _ = self.paths(row)
        environment = {key: value for key, value in os.environ.items() if not key.startswith("AGENT_WOW_")}
        result = subprocess.run([str(self.binary), "--config", str(config / "client.yaml"), *args],
                                env=environment, capture_output=True, text=True, timeout=30)
        # No command accepts credentials in argv or stdin; auth reads its own
        # private file. Keep bounded output only from the ordinary client CLI.
        return {"exit": result.returncode, "stdout": result.stdout[:16384], "stderr": result.stderr[:4096]}

    def login(self, row):
        return self.cli(row, ["auth", "login", "--timeout", "15s"])

    def auth_status(self, row):
        return self.cli(row, ["auth", "status", "--timeout", "15s"])

    def characters(self, row):
        result = self.cli(row, ["char", "list", "--json", "--timeout", "15s"])
        if result["exit"]:
            raise Error("Normal saved-session character listing failed")
        return json.loads(result["stdout"])

    def create_character(self, row):
        p = row["plan"]
        return self.cli(row, ["char", "create", "--name", p["name"], "--race", p["race"],
                              "--class", p["class"], "--gender", p["gender"], "--timeout", "15s"])

    def profile(self, row):
        config, data = self.paths(row)
        write_private(config / "profile.json", {"profile": row["plan"]["profile"], "characterGuid": row["guid"],
                                               "characterName": row["plan"]["name"], "rpcPort": row["plan"]["rpcPort"],
                                               "config": str(config / "client.yaml"), "configDir": str(config), "dataDir": str(data),
                                               "independent": True, "moduleImagesInstalled": False,
                                               "runtimeLaunchAllowed": False, "levelStageCap": 60,
                                               "plannedPrimaryProfessions": row["plan"]["primaryProfessionsPlanned"],
                                               "plannedSecondaryProfessions": row["plan"]["secondaryProfessionsPlanned"]})


class Roster:
    def __init__(self, state, adapter, save):
        self.state, self.adapter, self.save = validate_state(state), adapter, save

    def checkpoint(self, row, **fields):
        row.update(fields)
        validate_state(self.state)
        self.save(self.state)

    def receipt(self, row, kind, value):
        row["receipts"].append({"at": time.time(), "kind": kind, "value": value})
        validate_state(self.state)
        self.save(self.state)

    def reconcile(self, row, listing):
        if row["accountPhase"] != "created" or row["authPhase"] != "confirmed":
            raise Error("Character reconciliation requires the owned account and saved session")
        if (not isinstance(listing, dict) or not isinstance(listing.get("realm"), dict)
                or type(listing["realm"].get("id")) is not int or listing["realm"]["id"] != 1
                or not isinstance(listing.get("characters"), list)):
            raise Error("Character receipt must identify expected realm1")
        characters = listing["characters"]
        p = row["plan"]
        if not characters:
            return False
        if len(characters) != 1:
            self.checkpoint(row, characterPhase="conflict")
            raise Error("Unexpected additional character on dedicated roster account")
        c = characters[0]
        if not character_matches(p, c):
            self.checkpoint(row, characterPhase="conflict")
            raise Error("Dedicated account character identity does not match immutable plan")
        if row["guid"] is not None and row["guid"] != c["guid"]:
            raise Error("Confirmed GUID must not change during reconciliation")
        existing_guids = {r["guid"] for r in self.state["rows"] if r is not row and r.get("guid")}
        if c["guid"] in existing_guids:
            raise Error("Character GUID duplicates another roster row")
        self.checkpoint(row, characterPhase="confirmed", guid=c["guid"], actualCharacter=c, actualRealm=listing["realm"])
        self.adapter.profile(row)
        self.checkpoint(row, profilesWritten=True)
        return True

    def step(self, row):
        validate_state(self.state)
        if not any(candidate is row for candidate in self.state["rows"]):
            raise Error("Operation row is outside this immutable roster state")
        if row["plan"]["slot"] == 1:
            return
        if row["characterPhase"] == "confirmed":
            if not row.get("profilesWritten"):
                self.adapter.profile(row)
                self.checkpoint(row, profilesWritten=True)
            return
        self.adapter.guard(row)
        if row["accountPhase"] in ("intent", "unknown", "collision"):
            raise Error("Account creation requires manual receipt reconciliation; no retry/adoption/login")
        self.adapter.prepare(row)
        if row["accountPhase"] == "planned":
            self.checkpoint(row, accountPhase="intent")
            try:
                phase = self.adapter.create_account(row)
            except Exception:
                self.checkpoint(row, accountPhase="unknown")
                raise Error("Account outcome unknown; command was not retried") from None
            if phase not in ("created", "collision"):
                phase = "unknown"
            receipt = {"at": time.time(), "kind": "account-bootstrap",
                       "value": {"account": row["plan"]["account"], "classification": phase}}
            self.checkpoint(row, accountPhase=phase, receipts=[*row["receipts"], receipt])
            if phase != "created":
                raise Error("No successful new-account receipt; no authentication attempted")
        if row["authPhase"] == "planned":
            self.checkpoint(row, authPhase="intent")
            result = self.adapter.login(row)
            self.receipt(row, "normal-auth-login", {"exit": result["exit"]})
            self.checkpoint(row, authPhase="confirmed" if result["exit"] == 0 else "unknown")
            if result["exit"]:
                raise Error("New-account authentication unconfirmed; no repeated login")
        else:
            result = self.adapter.auth_status(row)
            if result["exit"]:
                raise Error("Saved session unconfirmed; no fresh login or character mutation")
            self.checkpoint(row, authPhase="confirmed")
        listing = self.adapter.characters(row)
        self.receipt(row, "normal-character-list", listing)
        if self.reconcile(row, listing):
            return
        if row["characterPhase"] != "planned":
            raise Error("Prior character attempt remains absent; no automatic retry")
        self.checkpoint(row, characterPhase="intent")
        try:
            result = self.adapter.create_character(row)
            self.receipt(row, "normal-character-create", {"exit": result["exit"]})
        except Exception:
            result = None
        self.checkpoint(row, characterPhase="unknown")
        listing = self.adapter.characters(row)
        self.receipt(row, "post-create-normal-character-list", listing)
        if not self.reconcile(row, listing):
            self.checkpoint(row, characterPhase="rejected" if result is not None and result["exit"] else "unknown")
            raise Error("Creation did not produce an exact server-enumerated character; no retry")

    def apply(self, batch):
        if type(batch) is not int or not 1 <= batch <= 5:
            raise Error("Creation batch must be1..5")
        pending = [r for r in self.state["rows"] if r["plan"]["slot"] != 1 and
                   (r["characterPhase"] != "confirmed" or not r.get("profilesWritten"))]
        for row in pending[:batch]:
            self.step(row)
        return summary(self.state)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("plan", "status", "apply"))
    parser.add_argument("--root", type=Path, default=ROOT)
    parser.add_argument("--batch-size", type=int, default=1)
    parser.add_argument("--acknowledge-account-creation", action="store_true",
                        help="Explicitly select the reviewed normal-account creation operation; no runtime launch")
    args = parser.parse_args()
    root = args.root.absolute()
    state_path = root / "data/autonomy/thrall/roster/state.json"
    lock = None
    try:
        private_directory(state_path.parent)
        lock = os.open(state_path.with_suffix(".lock"), os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
        info = os.fstat(lock)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077:
            raise Error("Expected an owned private regular lock file")
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        try:
            value = validate_state(read_private(state_path))
        except FileNotFoundError:
            if args.command != "plan":
                raise Error("Prepare the immutable roster plan first")
            value = new_state()
            write_private(state_path, value)
        if args.command == "apply":
            if not args.acknowledge_account_creation:
                raise Error("Apply requires --acknowledge-account-creation; no realm operation ran")
            if not sys.platform.startswith("linux"):
                raise Error("Apply requires the known Linux realm host; no realm operation ran")
            if type(args.batch_size) is not int or not 1 <= args.batch_size <= 5:
                raise Error("Creation batch must be1..5")
            if value["rows"][1]["characterPhase"] != "confirmed" and args.batch_size != 1:
                raise Error("First Horde pilot must be a single fully verified character")
            result = Roster(value, RealmAdapter(root), lambda state: write_private(state_path, state)).apply(args.batch_size)
        else:
            result = summary(value)
        print(json.dumps(result, sort_keys=True))
        return 0
    except (Error, OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"horde-roster: {error}", file=sys.stderr)
        return 1
    finally:
        if lock is not None:
            os.close(lock)


if __name__ == "__main__":
    sys.exit(main())
