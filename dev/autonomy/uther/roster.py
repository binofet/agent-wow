#!/usr/bin/env python3
"""Create-only Alliance roster. Account bootstrap/auth and runtime admission are external."""
import argparse
import copy
from contextlib import contextmanager
import fcntl
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import tempfile
import time


ROOT = Path(__file__).resolve().parents[3]
PROTECTED = [
    {"name": "Arlen", "guid": "1", "class": "Priest", "rpcPort": 8086,
     "desiredProfessions": ["Herbalism", "Alchemy"]},
    {"name": "Mira", "guid": "4", "class": "Mage", "rpcPort": 8087,
     "desiredProfessions": ["Tailoring", "Enchanting"]},
    {"name": "Rook", "guid": "5", "class": "Rogue", "rpcPort": 8088,
     "desiredProfessions": ["Skinning", "Leatherworking"]},
    {"name": "Aveline", "guid": "7", "class": "Mage", "rpcPort": 8192},
]
SECONDARY = ["Cooking", "Fishing", "First Aid"]
PRIMARY = {2: ["Mining", "Engineering"], 3: ["Herbalism", "Inscription"],
           4: ["Mining", "Jewelcrafting"], 5: ["Mining", "Blacksmithing"]}
RACES = {"Human": 1, "Dwarf": 3, "Night Elf": 4, "Gnome": 7, "Draenei": 11}
CLASSES = {"Warrior": 1, "Paladin": 2, "Hunter": 3, "Rogue": 4, "Priest": 5,
           "Shaman": 7, "Mage": 8, "Warlock": 9, "Druid": 11}
LEGAL = {"Human": {1, 2, 4, 5, 8, 9}, "Dwarf": {1, 2, 3, 4, 5},
         "Night Elf": {1, 3, 4, 5, 11}, "Gnome": {1, 4, 8, 9},
         "Draenei": {1, 2, 3, 5, 7, 8}}
SEEDS = [
    ("Nixwick", "Gnome", "Warlock"), ("Sylvara", "Night Elf", "Druid"),
    ("Brannic", "Dwarf", "Hunter"), ("Celdric", "Human", "Paladin"),
    ("Velaara", "Draenei", "Shaman"), ("Bromgar", "Dwarf", "Warrior"),
    ("Tesswyn", "Human", "Rogue"), ("Elowen", "Night Elf", "Priest"),
    ("Pipwizzle", "Gnome", "Mage"), ("Merrix", "Gnome", "Warlock"),
    ("Luneth", "Night Elf", "Druid"), ("Thordrin", "Dwarf", "Hunter"),
    ("Aurelia", "Human", "Paladin"), ("Zelaani", "Draenei", "Shaman"),
    ("Garrick", "Human", "Warrior"), ("Nimblet", "Gnome", "Rogue"),
    ("Daelira", "Draenei", "Priest"), ("Corwin", "Human", "Mage"),
    ("Vespera", "Human", "Warlock"), ("Thalindra", "Night Elf", "Druid"),
    ("Fenrisa", "Night Elf", "Hunter"), ("Torvald", "Dwarf", "Paladin"),
    ("Orelai", "Draenei", "Shaman"), ("Bryndis", "Dwarf", "Warrior"),
    ("Shalwen", "Night Elf", "Rogue"), ("Hildrun", "Dwarf", "Priest"),
    ("Yrelina", "Draenei", "Mage"), ("Fizzara", "Gnome", "Warlock"),
    ("Elarune", "Night Elf", "Druid"), ("Durnic", "Dwarf", "Hunter"),
    ("Saelora", "Draenei", "Paladin"), ("Tavraani", "Draenei", "Shaman"),
    ("Alaric", "Human", "Warrior"), ("Brixley", "Gnome", "Rogue"),
    ("Serelis", "Human", "Priest"), ("Wendel", "Gnome", "Mage"),
]
NAME_REJECTIONS = {0x32, *range(0x58, 0x68)}


class Error(Exception):
    pass


def valid_name(value):
    return isinstance(value, str) and re.fullmatch(r"[A-Za-z]{2,12}", value) is not None


def names_for(name):
    if not valid_name(name):
        raise Error("Names must contain 2..12 ASCII letters")
    name = name.capitalize()
    return [name] + [name[:11] + suffix for suffix in "aeio"]


def plan(first_name="Nixwick"):
    seeds = [(first_name, *SEEDS[0][1:]), *SEEDS[1:]]
    rows = []
    for index, (name, race, klass) in enumerate(seeds, 2):
        name = names_for(name)[0]
        rows.append({"account": f"WCALLIANCE{index:02}", "index": index,
                     "name": name, "names": names_for(name), "race": race,
                     "raceId": RACES[race], "class": klass, "classId": CLASSES[klass],
                     "gender": "Male" if index % 2 == 0 else "Female",
                     "profile": "alliance-" + name.lower(), "rpcPort": 8400 + index,
                     "configDir": f"config/autonomy/uther-roster/account{index:02}",
                     "dataDir": f"data/autonomy/uther-roster/account{index:02}",
                     "desiredProfessions": list(PRIMARY.get(index, [])),
                     "desiredSecondaryProfessions": list(SECONDARY)})
    candidates = [n.casefold() for row in rows for n in row["names"]]
    if len(candidates) != len(set(candidates)) or any(
            n in {row["name"].casefold() for row in PROTECTED} for n in candidates):
        raise Error("Planned names or fallback names collide")
    protected = [dict(row, desiredSecondaryProfessions=list(SECONDARY)) for row in copy.deepcopy(PROTECTED)]
    return {"target": 40, "protected": protected, "newAccounts": rows,
            "deathKnight": "deferred_until_actual_eligibility",
            "professionIntent": "Planning only; no profession learning claimed without actual trainer receipts",
            "progressionIntent": {"softCap": 60, "enforcement": "coach_admission_policy_only_not_server_cap",
                                  "futureStages": [70, 80], "futureAdmission": "explicit_user_change_required"},
            "runtimeAdmission": "external; no runtimes launched by this tool"}


def initial(first_name="Nixwick", realm_id=1):
    if type(realm_id) is not int or not 1 <= realm_id <= 255:
        raise Error("Realm ID must be 1..255")
    return {"version": 1, "realmId": realm_id, "plan": plan(first_name),
            "attempts": [], "verified": {}}


def validate(data):
    try:
        expected = initial(data["plan"]["newAccounts"][0]["name"], data["realmId"])
        if data["version"] != 1 or data["plan"] != expected["plan"]:
            raise Error("Roster plan is invalid or belongs to another format")
        accounts = {row["account"]: row for row in data["plan"]["newAccounts"]}
        attempts, verified = data["attempts"], data["verified"]
        if not isinstance(attempts, list) or len(attempts) > 180 or not isinstance(verified, dict):
            raise Error("Roster evidence is invalid")
        seen = set()
        histories = {}
        for seq, row in enumerate(attempts, 1):
            account = row["account"]
            key = (account, row["name"])
            if (row["sequence"] != seq or account not in accounts
                    or row["name"] not in accounts[account]["names"] or key in seen
                    or row["state"] not in ("pending", "name_rejected", "blocked", "verified")):
                raise Error("Roster reservation history is invalid")
            seen.add(key)
            if row["state"] == "name_rejected" and row.get("serverCode") not in NAME_REJECTIONS:
                raise Error("Name retry lacks a definite server name rejection")
            history = histories.setdefault(account, [])
            if history and history[-1]["state"] != "name_rejected":
                raise Error("Earlier creation is unresolved, blocked or verified; later reservation is forbidden")
            if row["name"] != accounts[account]["names"][len(history)]:
                raise Error("Creation names do not follow the bounded reservation order")
            history.append(row)
        guids, names = set(), set()
        for account, row in verified.items():
            if account not in accounts:
                raise Error("Verified account is outside the owned roster")
            check_character(row["character"], accounts[account])
            ch = row["character"]
            if ch["guid"] in guids or ch["name"].casefold() in names or row["realmId"] != data["realmId"]:
                raise Error("Verified roster contains duplicate or mismatched identities")
            guids.add(ch["guid"])
            names.add(ch["name"].casefold())
            history = histories.get(account, [])
            if (row.get("proof") != "normal_character_list"
                    or row.get("creation") != ("reserved" if history else "observed_existing")
                    or history and (history[-1]["state"] != "verified"
                                    or history[-1]["name"].casefold() != ch["name"].casefold())):
                raise Error("Verified character evidence and reservation history disagree")
        if any(history[-1]["state"] == "verified" and account not in verified
               for account, history in histories.items()):
            raise Error("Verified reservation lacks its actual character-list evidence")
    except (KeyError, IndexError, TypeError, ValueError):
        raise Error("Roster ledger structure is invalid") from None
    return data


def private_directory(path):
    for part in (path, *path.parents):
        try:
            info = part.lstat()
        except FileNotFoundError:
            continue
        if stat.S_ISLNK(info.st_mode) or not stat.S_ISDIR(info.st_mode):
            raise Error("Private directory must not contain symlinks")
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = path.stat()
    if info.st_uid != os.geteuid() or info.st_mode & 0o077:
        raise Error("Roster/account directory must be owned and mode0700")


def private_file(path, flags):
    fd = os.open(path, flags | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
    info = os.fstat(fd)
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077:
        os.close(fd)
        raise Error("Private file must be owned and mode0600")
    return fd


class Store:
    def __init__(self, directory):
        self.directory = Path(directory).absolute()
        private_directory(self.directory)
        self.path = self.directory / "roster.json"

    def lock(self):
        fd = private_file(self.directory / "roster.lock", os.O_RDWR | os.O_CREAT)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            os.close(fd)
            raise Error("Another roster operation holds the lock") from None
        return os.fdopen(fd, "r+")

    def load(self):
        try:
            with os.fdopen(private_file(self.path, os.O_RDONLY)) as source:
                raw = source.read(1048577)
        except FileNotFoundError:
            raise Error("Roster is not initialized; use init") from None
        if len(raw) > 1048576:
            raise Error("Roster ledger exceeds its size limit")
        try:
            return validate(json.loads(raw))
        except ValueError:
            raise Error("Roster ledger is invalid JSON") from None

    def save(self, data):
        validate(data)
        if self.path.exists() or self.path.is_symlink():
            os.close(private_file(self.path, os.O_RDONLY))
        fd, name = tempfile.mkstemp(prefix=".roster-", dir=self.directory)
        try:
            with os.fdopen(fd, "w") as output:
                os.fchmod(output.fileno(), 0o600)
                json.dump(data, output, allow_nan=False, separators=(",", ":"))
                output.write("\n")
                output.flush()
                os.fsync(output.fileno())
            os.replace(name, self.path)
            directory = os.open(self.directory, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
        finally:
            if os.path.exists(name):
                os.unlink(name)


def check_character(ch, slot):
    if (not isinstance(ch, dict) or not isinstance(ch.get("guid"), str)
            or not re.fullmatch(r"[1-9][0-9]{0,14}", ch["guid"])
            or not 7 < int(ch["guid"]) < 2**48
            or not valid_name(ch.get("name"))
            or ch["name"].casefold() not in {name.casefold() for name in slot["names"]}
            or ch.get("race") != slot["race"] or ch.get("class") != slot["class"]
            or ch.get("gender") != slot["gender"] or type(ch.get("level")) is not int
            or not 1 <= ch["level"] <= 80):
        raise Error("Character GUID/name/race/class/gender differs from the owned reservation")


def character_list(raw, realm_id):
    try:
        result = json.loads(raw)
        realm = result["realm"]
        rows = result["characters"]
        if (type(realm["id"]) is not int or realm["id"] != realm_id
                or not isinstance(rows, list) or len(rows) > 10):
            raise Error("Character list realm or count is unconfirmed")
        guids, names = set(), set()
        for row in rows:
            guid, name = row["guid"], row["name"]
            if (not isinstance(guid, str) or not re.fullmatch(r"[1-9][0-9]{0,14}", guid)
                    or not 0 < int(guid) < 2**48 or not valid_name(name)
                    or guid in guids or name.casefold() in names):
                raise Error("Character list contains duplicate or invalid identities")
            guids.add(guid)
            names.add(name.casefold())
    except (ValueError, KeyError, TypeError):
        raise Error("Character list is invalid; raw output was withheld") from None
    return rows


class CLI:
    """Saved-key checks and normal char enumeration/creation only; never auth login."""
    def __init__(self, root, slot, inactive_account, realm_id, run=subprocess.run):
        self.root, self.slot, self.realm_id, self.run = Path(root).absolute(), slot, realm_id, run
        if inactive_account != slot["account"]:
            raise Error("Supply the exact owned --inactive-account; a character-list connection can displace a live client")
        self.config = self.root / slot["configDir"]
        self.data = self.root / slot["dataDir"]
        for directory in (self.config, self.data):
            if not directory.is_dir():
                raise Error("Account bootstrap/configuration and first normal authentication are external prerequisites")
            private_directory(directory)
        # Do not read credentials or the session database; the normal client owns them.
        for filename in ("auth.json", "realm.json", "buddy.yaml"):
            os.close(private_file(self.config / filename, os.O_RDONLY))
        binary = self.root / "bin/agent-wow"
        if binary.is_symlink() or not binary.is_file() or not os.access(binary, os.X_OK):
            raise Error("An explicit built project client is required")
        self.command = [str(binary), "--config", str(self.config / "buddy.yaml")]
        self.environment = {k: v for k, v in os.environ.items() if not k.startswith("AGENT_WOW_")}
        self.environment.update(AGENT_WOW_CONFIG_DIR=str(self.config), AGENT_WOW_DATA_DIR=str(self.data),
                                AGENT_WOW_AUTH_FILE_PATH=str(self.config / "auth.json"),
                                AGENT_WOW_REALM_FILE_PATH=str(self.config / "realm.json"),
                                AGENT_WOW_MODULE_DIR=str(self.config / "modules"), AGENT_WOW_LOG_LEVEL="error")
        self.idle_guard()

    @contextmanager
    def lock(self):
        # Share the maintained launcher's lock so it cannot start between our lists/create.
        directory = self.root / "config/buddies" / self.slot["profile"]
        private_directory(directory)
        with os.fdopen(private_file(directory / "buddy.lock", os.O_RDWR | os.O_CREAT), "r+") as lock:
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                raise Error("Owned profile launcher is busy; no account connection attempted") from None
            self.idle_guard()
            yield

    def idle_guard(self):
        for path in (self.root / "config/buddies" / self.slot["profile"] / "buddy.pid",
                     self.config / "buddy.pid"):
            if path.exists() or path.is_symlink():
                raise Error("Owned profile has a PID record; leave it untouched and obtain owner-confirmed clean stop")

    def invoke(self, args):
        self.idle_guard()
        try:
            result = self.run(self.command + args, cwd=self.root, env=self.environment,
                              stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                              timeout=40, check=False)
        except (OSError, subprocess.TimeoutExpired):
            raise Error("Normal client command failed or timed out; raw output was withheld") from None
        if len(result.stdout) + len(result.stderr) > 1048576:
            raise Error("Normal client output exceeds roster limit")
        return result

    def verify_account(self):
        result = self.invoke(["auth", "status", "--timeout", "10s"])
        users = re.findall(rb"(?m)^Username: ([A-Za-z0-9]+)\r?$", result.stdout)
        if result.returncode != 0 or users != [self.slot["account"].encode()]:
            raise Error("Saved-session account identity is unconfirmed; no login or character mutation attempted")

    def list(self):
        self.verify_account()
        result = self.invoke(["char", "list", "--json", "--timeout", "10s"])
        if result.returncode != 0:
            raise Error("Normal character list failed; no auth refresh attempted")
        return character_list(result.stdout, self.realm_id)

    def create(self, name):
        self.verify_account()
        result = self.invoke(["char", "create", "--name", name, "--race", str(self.slot["raceId"]),
                              "--class", str(self.slot["classId"]), "--gender", self.slot["gender"].lower(),
                              "--timeout", "10s"])
        if result.returncode == 0:
            return {"reply": "success"}
        # Only the normal client's explicit final create-result format is retry authority.
        codes = re.findall(rb"(?m)^(?:Error: )?character create: [^\r\n]* \(0x([0-9a-f]{2})\)\r?$",
                           result.stderr)
        if len(codes) == 1:
            return {"reply": "server_rejection", "serverCode": int(codes[0], 16)}
        return {"reply": "unknown"}


class Roster:
    def __init__(self, data, save, now=time.time):
        self.data, self.save, self.now = validate(copy.deepcopy(data)), save, now

    def slot(self, account):
        for row in self.data["plan"]["newAccounts"]:
            if row["account"] == account:
                return row
        raise Error("Account is outside the new owned roster; protected accounts are never connected")

    def attempts(self, account):
        return [row for row in self.data["attempts"] if row["account"] == account]

    def observe(self, account, rows):
        slot = self.slot(account)
        if len(rows) > 1:
            raise Error("Expected one character per owned account; no creation attempted")
        if not rows:
            if account in self.data["verified"]:
                raise Error("Previously verified character is absent; no recreation attempted")
            return False
        ch = rows[0]
        check_character(ch, slot)
        old = self.data["verified"].get(account)
        if old and (old["character"]["guid"] != ch["guid"] or old["character"]["name"] != ch["name"]):
            raise Error("Previously verified character identity changed")
        for other, record in self.data["verified"].items():
            if other != account and (record["character"]["guid"] == ch["guid"]
                                     or record["character"]["name"].casefold() == ch["name"].casefold()):
                raise Error("Character identity is already owned by another roster account")
        attempts = self.attempts(account)
        if attempts and ch["name"].casefold() != attempts[-1]["name"].casefold():
            raise Error("Listed character does not match the latest reserved creation")
        if attempts and attempts[-1]["state"] in ("name_rejected", "blocked"):
            raise Error("Server rejection conflicts with the character list; owner investigation required")
        if attempts:
            attempts[-1]["state"] = "verified"
        self.data["verified"][account] = {
            "character": {key: ch[key] for key in ("guid", "name", "race", "class", "gender", "level")},
            "realmId": self.data["realmId"], "observedAt": self.now(),
            "proof": "normal_character_list", "creation": "reserved" if attempts else "observed_existing",
            "professions": "unlearned_until_actual_trainer_receipt", "runtime": "not_admitted"}
        self.save(self.data)
        return True

    def reconcile(self, account, cli):
        self.slot(account)
        if self.observe(account, cli.list()):
            return {"account": account, "state": "verified", **self.data["verified"][account]}
        attempts = self.attempts(account)
        return {"account": account, "state": attempts[-1]["state"] if attempts else "empty",
                "retryAllowed": bool(attempts and attempts[-1]["state"] == "name_rejected")}

    def create_next(self, account, cli):
        slot = self.slot(account)
        # Always reconcile before another mutation, including a previous process crash.
        observation = self.reconcile(account, cli)
        if observation["state"] == "verified":
            return observation
        attempts = self.attempts(account)
        if attempts and attempts[-1]["state"] != "name_rejected":
            raise Error("Creation is unresolved or blocked; absence in a later list is not retry authority")
        next_account = next((row["account"] for row in self.data["plan"]["newAccounts"]
                             if row["account"] not in self.data["verified"]), None)
        if account != next_account:
            raise Error("create-next must process the first unverified roster account")
        candidates = [name for name in slot["names"] if name not in {row["name"] for row in attempts}]
        if not candidates:
            raise Error("Bounded name alternatives exhausted; owner review required")
        reservation = {"sequence": len(self.data["attempts"]) + 1, "account": account,
                       "name": candidates[0], "state": "pending", "reservedAt": self.now()}
        self.data["attempts"].append(reservation)
        self.save(self.data)  # Durable before ANY create request, including account verification.
        try:
            reply = cli.create(reservation["name"])
        except Error:
            raise Error("Reserved create outcome is unconfirmed; reconcile only, never blindly retry") from None
        reservation.update(reply)
        if reply.get("reply") == "server_rejection":
            reservation["state"] = "name_rejected" if reply.get("serverCode") in NAME_REJECTIONS else "blocked"
        self.save(self.data)
        return self.reconcile(account, cli)


def summary(data):
    return {"target": 40, "protectedAnchors": 4, "newVerified": len(data["verified"]),
            "accountedIncludingProtected": 4 + len(data["verified"]),
            "protectedObservation": "external_owner_manifest; never connected by roster tool",
            "newPlanned": 36, "runtimeLaunchesByTool": 0,
            "nextAccount": next((row["account"] for row in data["plan"]["newAccounts"]
                                 if row["account"] not in data["verified"]), None),
            "attempts": copy.deepcopy(data["attempts"]), "verified": copy.deepcopy(data["verified"])}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=ROOT)
    parser.add_argument("--state-dir", type=Path)
    sub = parser.add_subparsers(dest="command", required=True)
    for command in ("plan", "init"):
        child = sub.add_parser(command)
        child.add_argument("--first-name", default="Nixwick")
        child.add_argument("--realm-id", type=int, default=1)
    sub.add_parser("status")
    for command in ("create-next", "reconcile"):
        child = sub.add_parser(command)
        child.add_argument("--inactive-account", required=True,
                           help="Owner asserts this exact owned account has no active world client; no client is stopped")
    args = parser.parse_args(argv)
    try:
        if args.command == "plan":
            output = initial(args.first_name, args.realm_id)
        else:
            store = Store(args.state_dir or args.root / "data/autonomy/uther/roster")
            with store.lock():
                if args.command == "init":
                    if store.path.exists() or store.path.is_symlink():
                        raise Error("Roster already exists; left unchanged")
                    data = initial(args.first_name, args.realm_id)
                    store.save(data)
                    output = summary(data)
                else:
                    data = store.load()
                    if args.command == "status":
                        output = summary(data)
                    else:
                        roster = Roster(data, store.save)
                        slot = roster.slot(args.inactive_account)
                        cli = CLI(args.root, slot, args.inactive_account, data["realmId"])
                        with cli.lock():
                            output = getattr(roster, args.command.replace("-", "_"))(args.inactive_account, cli)
        print(json.dumps(output, indent=2, allow_nan=False))
        return 0
    except (Error, OSError) as failure:
        # External client output and credentials are never interpolated here.
        print("Roster blocked: " + str(failure), file=__import__("sys").stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
