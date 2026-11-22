#!/usr/bin/env python3
"""Generate offline taxi reference facts from three exact pinned public DBCs.

The upstream repository claims build 12340. This catalog does not establish
installed client/realm data, a live offered route, a quoted fare or walkability.
"""

import argparse
from collections import Counter
import hashlib
import json
import math
from pathlib import Path
import struct


REPOSITORY = "https://github.com/DreamCoreRev/EonsDBC"
COMMIT = "c98c8e4972086195d1306b7830d3e8452026b68d"
RAW_ROOT = f"https://raw.githubusercontent.com/DreamCoreRev/EonsDBC/{COMMIT}/DBFilesClient/dbc"
NODES_SHA256 = "63a096b629298cdd254687658947c3b3ac8b40b483de012a8a5194aa0467cb1a"
PATHS_SHA256 = "1af5ba71fca5684a432b9d3f449aaeb21e4c091532e4b5795335be200d88dd5d"
MAP_SHA256 = "261bd7c2c9dbdba1f2966ca21106673ed722c2e3681d227f1eed261b6ce870d5"
MAP_SOURCE = "https://github.com/wowgaming/client-data/releases/download/v20.0/Data.zip"
MAX_INPUT_BYTES = 1 << 20


def records(data, fields, name):
    """Validate the complete WDBC envelope before decoding any record."""
    if len(data) < 20:
        raise ValueError(f"{name}: incomplete WDBC header")
    magic, count, width, size, string_bytes = struct.unpack_from("<4sIIII", data)
    if magic != b"WDBC" or width != fields or size != fields * 4:
        raise ValueError(f"{name}: unexpected WDBC field layout")
    end = 20 + count * size
    if count == 0 or end + string_bytes != len(data):
        raise ValueError(f"{name}: file length does not match nonempty record/string counts")
    rows = [struct.unpack_from(f"<{fields}I", data, 20 + index * size) for index in range(count)]
    return rows, data[end:]


def unique_ids(rows, name, allow_zero=False):
    seen = set()
    for row in rows:
        if (not allow_zero and row[0] == 0) or row[0] in seen:
            raise ValueError(f"{name}: zero or duplicate record ID {row[0]}")
        seen.add(row[0])
    return seen


def dbc_string(block, offset, name, record_id):
    if offset >= len(block):
        raise ValueError(f"{name}: ID {record_id} string offset outside string block")
    end = block.find(b"\0", offset)
    if end < 0:
        raise ValueError(f"{name}: ID {record_id} unterminated string")
    try:
        return block[offset:end].decode("utf-8")
    except UnicodeDecodeError as error:
        raise ValueError(f"{name}: ID {record_id} invalid UTF-8 string") from error


def decode_map_ids(data):
    rows, _ = records(data, 66, "Map.dbc")
    # Map 0 is a real map ID; the positive-ID constraint applies to taxi rows.
    return unique_ids(rows, "Map.dbc", allow_zero=True)


def decode_nodes(data, map_ids):
    rows, strings = records(data, 24, "TaxiNodes.dbc")
    unique_ids(rows, "TaxiNodes.dbc")
    result = []
    for row in sorted(rows, key=lambda value: value[0]):
        coordinates = [struct.unpack("<f", struct.pack("<I", word))[0] for word in row[2:5]]
        if not all(math.isfinite(value) for value in coordinates):
            raise ValueError(f"TaxiNodes.dbc: ID {row[0]} nonfinite coordinates")
        # Validate every locale offset, including the empty enUS slots. Only
        # slot 2 is populated in this pinned file; its labels are French.
        names = [dbc_string(strings, offset, "TaxiNodes.dbc", row[0]) for offset in row[5:21]]
        result.append(dict(id=row[0], mapId=row[1], x=coordinates[0], y=coordinates[1], z=coordinates[2],
                           name=names[2], mapReferenceKnown=row[1] in map_ids))
    return result


def decode_paths(data, node_ids):
    rows, _ = records(data, 4, "TaxiPath.dbc")
    unique_ids(rows, "TaxiPath.dbc")
    # Endpoints may contain upstream 0/UINT32_MAX sentinels. Retain their raw
    # IDs and knownness; duplicate endpoint pairs also retain each path ID.
    return [dict(id=row[0], source=row[1], destination=row[2], baseCopper=row[3],
                 endpointsKnown=row[1] in node_ids and row[2] in node_ids)
            for row in sorted(rows, key=lambda value: value[0])]


def generate(nodes_data, paths_data, map_data):
    # Pin every consumed file before invoking a decoder. WDBC layouts alone
    # cannot establish a client build or identify trustworthy reference bytes.
    mismatches = [name for name, data, checksum in (
        ("TaxiNodes.dbc", nodes_data, NODES_SHA256),
        ("TaxiPath.dbc", paths_data, PATHS_SHA256),
        ("Map.dbc", map_data, MAP_SHA256),
    ) if hashlib.sha256(data).hexdigest() != checksum]
    if mismatches:
        raise ValueError("SHA-256 checksum mismatch: " + ", ".join(mismatches))
    nodes = decode_nodes(nodes_data, decode_map_ids(map_data))
    paths = decode_paths(paths_data, {node["id"] for node in nodes})
    pairs = Counter((path["source"], path["destination"]) for path in paths)
    return dict(
        format=1, claimedBuild=12340, nameLocale="frFR",
        provenance=dict(repository=REPOSITORY, commit=COMMIT, nodesSHA256=NODES_SHA256,
                        pathsSHA256=PATHS_SHA256, mapSHA256=MAP_SHA256,
                        nodeURL=RAW_ROOT + "/TaxiNodes.dbc", pathURL=RAW_ROOT + "/TaxiPath.dbc", mapSource=MAP_SOURCE),
        nodes=nodes, paths=paths,
        quality=dict(nodes=len(nodes), paths=len(paths), unnamedNodes=sum(not node["name"] for node in nodes),
                     unmappedNodes=sum(not node["mapReferenceKnown"] for node in nodes),
                     invalidEndpointPaths=sum(not path["endpointsKnown"] for path in paths),
                     duplicateEndpointPairs=sum(count >= 2 for count in pairs.values()),
                     zeroPricePaths=sum(path["baseCopper"] == 0 for path in paths)),
    )


def encoded(catalog):
    return (json.dumps(catalog, ensure_ascii=False, indent=2, allow_nan=False) + "\n").encode("utf-8")


def read_bounded(filename):
    with filename.open("rb") as source:
        data = source.read(MAX_INPUT_BYTES + 1)
    if len(data) > MAX_INPUT_BYTES:
        raise ValueError(f"{filename.name}: exceeds reference input size limit")
    return data


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--nodes", type=Path, required=True)
    parser.add_argument("--paths", type=Path, required=True)
    parser.add_argument("--map", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        result = encoded(generate(read_bounded(args.nodes), read_bounded(args.paths), read_bounded(args.map)))
    except (OSError, ValueError) as error:
        parser.error(str(error))
    # All pin, layout and fact validation finishes before touching output.
    args.output.write_bytes(result)


if __name__ == "__main__":
    main()
