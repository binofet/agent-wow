"""Generate ordinary, explicitly accepted item starters from the pinned public templates."""
import argparse
import gzip
import hashlib
import json
from pathlib import Path
import runpy


HERE = Path(__file__).resolve().parent
sql_rows = runpy.run_path(str(HERE.parents[1] / "dev/build-game-catalog"))["sql_rows"]
# This additional table supplies the server-side AUTO_ACCEPT special flag.
ADDON_SHA256 = "ca1081203af9dc5ab9b948d94652a66674d116ce387f629595ec38f89c4af5d2"


def build(source):
    catalog = json.loads((HERE / "catalog.json").read_text())
    with gzip.open(HERE / "item-requirements.json.gz", "rt") as ref:
        items = json.load(ref)
    shas = {name: catalog["sources"][name]["sha256"] for name in ("item_template", "quest_template")}
    shas["quest_template_addon"] = ADDON_SHA256
    if items["sourceCommit"] != catalog["sourceCommit"] or items["sourceSHA256"] != shas["item_template"]:
        raise ValueError("item and quest catalogs use different source revisions")
    for table in ("quest_template", "quest_template_addon"):
        if hashlib.sha256((source / (table + ".sql")).read_bytes()).hexdigest() != shas[table]:
            raise ValueError("source checksum mismatch: " + table)

    # HandleQuestgiverQueryQuestOpcode can add AUTO_ACCEPT quests during a
    # query and returns a reward/request-items form for auto-complete quests.
    # Both lie outside this RPC's query → explicit acceptance contract.
    special = {r["id"]: r["specialflags"] for r in sql_rows(source / "quest_template_addon.sql")}
    manual = {}
    for quest in sql_rows(source / "quest_template.sql"):
        quest_id, flags = quest["id"], quest["flags"]
        special_flags = special.get(quest_id, 0)
        if quest["questtype"] == 0 or flags & (0x80000 | 0x10000) or special_flags & 4:
            continue
        # Repeatable or monthly special flags, or a daily/weekly quest flag.
        repeatable = bool(special_flags & (0x01 | 0x10) or flags & (0x1000 | 0x8000))
        manual[quest_id] = quest["minlevel"], repeatable
    fields = [items["columns"].index(k) for k in ("itemId", "startQuest", "requiredLevel", "stackable")]
    starters = {}
    for row in items["rows"]:
        entry, quest, level, stack = (row[i] for i in fields)
        if quest and quest in manual:
            min_level, repeatable = manual[quest]
            if stack < 1 or min(level, min_level) < 0:
                raise ValueError("invalid starter requirements: " + str(entry))
            starters[entry] = dict(questId=quest, minLevel=max(level, min_level), maxStack=stack)
            if repeatable:
                starters[entry]["repeatable"] = True
    return dict(build=catalog["build"], sourceCommit=catalog["sourceCommit"], sourceSHA256=shas, items=starters)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path, help="directory containing the pinned public quest SQL files")
    parser.add_argument("--output", type=Path, default=HERE / "quest-starters.json")
    args = parser.parse_args()
    result = build(args.source)
    # Keep each generated starter on one independently reviewable line.
    rows = result.pop("items")
    header = json.dumps(result, indent=2)[:-2]
    lines = [f'    "{entry}": ' + json.dumps(rule) for entry, rule in sorted(rows.items())]
    args.output.write_text(header + ',\n  "items": {\n' + ",\n".join(lines) + "\n  }\n}\n")
