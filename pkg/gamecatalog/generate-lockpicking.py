#!/usr/bin/env python3
"""Generate lockpicking.json from the pinned WoW 3.3.5a Lock.dbc."""

import hashlib
import json
from pathlib import Path
import struct
import sys


SOURCE = "https://raw.githubusercontent.com/DreamCoreRev/EonsDBC/AzgathCore/DBFilesClient/dbc/Lock.dbc"
SOURCE_SHA256 = "c49bd7ed970583a3660d5d16d6917e91e46aafc9e9ed7926281f4889d45904d0"
BUILD = 12340


def generate(data):
    if hashlib.sha256(data).hexdigest() != SOURCE_SHA256:
        raise SystemExit("The Lock.dbc SHA-256 does not match the pinned client data.")
    header = struct.Struct("<4sIIII")
    row = struct.Struct("<33I")
    magic, count, fields, record_size, strings = header.unpack_from(data)
    if (magic, fields, record_size) != (b"WDBC", 33, row.size) or len(data) != header.size + count * record_size + strings:
        raise SystemExit("Invalid build-12340 Lock.dbc layout.")
    requirements = {}
    for index in range(count):
        entry, *values = row.unpack_from(data, header.size + index * record_size)
        for lock_type, lock_index, skill in zip(values[:8], values[8:16], values[16:24]):
            # AzerothCore tests the first matching skill case. Type 2 is a
            # skill case, and its lock type 1 is Pick Lock.
            if lock_type == 2 and lock_index == 1:
                requirements[entry] = skill
                break
    return dict(build=BUILD, source=SOURCE, sha256=SOURCE_SHA256, requirements=dict(sorted(requirements.items())))


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit(f"Usage: python3 {sys.argv[0]} /path/to/Lock.dbc > lockpicking.json")
    print(json.dumps(generate(Path(sys.argv[1]).read_bytes()), indent=2))
