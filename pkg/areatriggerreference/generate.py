#!/usr/bin/env python3
"""Join two exact public AC reference dumps offline; never inspect a realm DB."""
import argparse
import csv
import hashlib
import json
from pathlib import Path

COMMIT = 'b8c2e189651d262ab94d6f012b1a9d38591a091b'
GEOMETRY_SHA = '6f9e067e66812e485b80d194e316e44989396e666a77d81a6f0845b5ca987e10'
TELEPORTS_SHA = '94acced79fe57b7338376102d51dcb8d47aafa0c3a551f0289db8d636a178fcc'
SOURCE_ROOT = f'https://raw.githubusercontent.com/azerothcore/azerothcore-wotlk/{COMMIT}/data/sql/base/db_world/'


def rows(path, expected, width):
    raw = path.read_bytes()
    if hashlib.sha256(raw).hexdigest() != expected:
        raise ValueError('pinned reference hash mismatch: ' + str(path))
    result = {}
    for line in raw.decode('utf-8').splitlines():
        if not line.startswith('('): continue
        fields = next(csv.reader([line.rstrip(',;')[1:-1]], quotechar="'", escapechar='\\', skipinitialspace=True))
        if len(fields) != width or int(fields[0]) <= 0 or int(fields[0]) in result:
            raise ValueError('invalid or duplicate reference row')
        result[int(fields[0])] = fields
    return result


def generate(geometry, teleports):
    volumes = rows(geometry, GEOMETRY_SHA, 10)
    destinations = rows(teleports, TELEPORTS_SHA, 7)
    out = []
    for identifier, dest in sorted(destinations.items()):
        volume = volumes[identifier]
        source_map, target_map = int(volume[1]), int(dest[2])
        if source_map == target_map: continue
        row = dict(id=identifier, sourceMap=source_map, destinationMap=target_map)
        row.update(zip(('x', 'y', 'z', 'radius', 'length', 'width', 'height', 'orientation'), map(float, volume[2:])))
        out.append(row)
    return dict(format=1, commit=COMMIT, geometrySHA256=GEOMETRY_SHA, teleportsSHA256=TELEPORTS_SHA, triggers=out)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--geometry', type=Path, required=True)
    parser.add_argument('--teleports', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    document = generate(args.geometry, args.teleports)
    args.output.write_text(json.dumps(document, sort_keys=True, separators=(',', ':'), allow_nan=False)+'\n')
