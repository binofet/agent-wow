#!/usr/bin/env python3
"""Focused offline checks for the pinned taxi reference generator."""

from collections import Counter
import hashlib
import json
from pathlib import Path
import struct
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import generate


DIRECTORY = Path(__file__).resolve().parent
REFERENCE_SHA256 = "506a4ff09b45419eff756cf2df4cee0757837f4f0c3f53447d1a1acea4f2506a"


def dbc(rows, width, strings=b"\0"):
    return (struct.pack("<4sIIII", b"WDBC", len(rows), width, width * 4, len(strings))
            + b"".join(struct.pack(f"<{width}I", *row) for row in rows) + strings)


def floating(value):
    return struct.unpack("<I", struct.pack("<f", value))[0]


def node(identifier=1, map_id=0):
    row = [0] * 24
    row[0], row[1] = identifier, map_id
    row[2:5] = [floating(1.25), floating(-2.5), floating(0)]
    row[7] = 1  # Locale slot 2, independent of the empty enUS slot.
    return row


class DecoderTests(unittest.TestCase):
    def test_nodes_preserve_french_label_coordinates_and_unknown_map(self):
        rows = [node(2, 999), node(1)]
        result = generate.decode_nodes(dbc(rows, 24, "\0Hurlevent, Elwynn\0".encode()), {0})
        self.assertEqual([entry["id"] for entry in result], [1, 2])
        self.assertEqual(result[0], dict(id=1, mapId=0, x=1.25, y=-2.5, z=0,
                                         name="Hurlevent, Elwynn", mapReferenceKnown=True))
        self.assertFalse(result[1]["mapReferenceKnown"])

    def test_every_node_locale_string_is_validated(self):
        for name in ("offset_outside", "offset_at_end", "unterminated", "utf8", "unused_locale"):
            with self.subTest(name=name):
                row, strings = node(), b"\0Nom\0"
                if name == "offset_outside":
                    row[7] = 99
                elif name == "offset_at_end":
                    row[7] = len(strings)
                elif name == "unterminated":
                    strings = b"\0Nom"
                elif name == "utf8":
                    strings = b"\0\xff\0"
                elif name == "unused_locale":
                    row[5] = 99
                with self.assertRaisesRegex(ValueError, "string|UTF-8"):
                    generate.decode_nodes(dbc([row], 24, strings), {0})

    def test_complete_headers_and_exact_file_size_are_required(self):
        good = dbc([node()], 24, b"\0Nom\0")
        fixtures = {"short_header": good[:19], "truncated": good[:-1], "trailing": good + b"x"}
        for name, offset, value in (("magic", 0, 0), ("field_count", 8, 23), ("record_size", 12, 92),
                                    ("count", 4, 0xffffffff), ("string_size", 16, 0xffffffff)):
            bad = bytearray(good)
            struct.pack_into("<I", bad, offset, value)
            fixtures[name] = bytes(bad)
        fixtures["empty"] = dbc([], 24)
        for name, data in fixtures.items():
            with self.subTest(name=name), self.assertRaises(ValueError):
                generate.decode_nodes(data, {0})

    def test_nonfinite_coordinates_and_nonpositive_or_duplicate_ids_are_rejected(self):
        for name, word in (("nan", 0x7fc00000), ("positive_infinity", 0x7f800000), ("negative_infinity", 0xff800000)):
            with self.subTest(name=name):
                row = node()
                row[2] = word
                with self.assertRaisesRegex(ValueError, "nonfinite"):
                    generate.decode_nodes(dbc([row], 24, b"\0Nom\0"), {0})
        for rows in ([node(0)], [node(), node()]):
            with self.subTest(rows=rows), self.assertRaisesRegex(ValueError, "zero or duplicate"):
                generate.decode_nodes(dbc(rows, 24, b"\0Nom\0"), {0})

    def test_map_zero_is_real_but_duplicate_map_ids_are_rejected(self):
        row = [0] * 66
        self.assertEqual(generate.decode_map_ids(dbc([row], 66)), {0})
        with self.assertRaisesRegex(ValueError, "duplicate"):
            generate.decode_map_ids(dbc([row, row], 66))

    def test_paths_preserve_duplicate_pairs_zero_prices_and_endpoint_sentinels(self):
        result = generate.decode_paths(dbc([[793, 1, 2, 0], [733, 1, 2, 0], [6, 0, 0xffffffff, 12000]], 4), {1, 2})
        self.assertEqual(result, [
            dict(id=6, source=0, destination=0xffffffff, baseCopper=12000, endpointsKnown=False),
            dict(id=733, source=1, destination=2, baseCopper=0, endpointsKnown=True),
            dict(id=793, source=1, destination=2, baseCopper=0, endpointsKnown=True),
        ])
        for rows in ([[0, 1, 2, 0]], [[6, 1, 2, 0], [6, 1, 2, 1]]):
            with self.subTest(rows=rows), self.assertRaisesRegex(ValueError, "zero or duplicate"):
                generate.decode_paths(dbc(rows, 4), {1, 2})


class PinAndCLITests(unittest.TestCase):
    def test_all_three_pins_are_checked_before_any_decoder(self):
        with mock.patch.object(generate, "decode_map_ids") as maps, mock.patch.object(generate, "decode_nodes") as nodes, mock.patch.object(generate, "decode_paths") as paths:
            with self.assertRaisesRegex(ValueError, "TaxiNodes.dbc, TaxiPath.dbc, Map.dbc"):
                generate.generate(b"wrong node bytes", b"wrong path bytes", b"wrong map bytes")
            maps.assert_not_called()
            nodes.assert_not_called()
            paths.assert_not_called()

    def test_cli_rejects_unpinned_synthetic_data_without_touching_output(self):
        # Lower-level synthetic decoders never grant a production CLI bypass.
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            inputs = (directory / "TaxiNodes.dbc", directory / "TaxiPath.dbc", directory / "Map.dbc")
            inputs[0].write_bytes(dbc([node()], 24, b"\0Nom\0"))
            inputs[1].write_bytes(dbc([[6, 1, 1, 0]], 4))
            inputs[2].write_bytes(dbc([[0] * 66], 66))
            output = directory / "reference.json"
            output.write_bytes(b"previous complete catalog\n")
            process = subprocess.run([sys.executable, "-B", str(DIRECTORY / "generate.py"),
                                      "--nodes", str(inputs[0]), "--paths", str(inputs[1]), "--map", str(inputs[2]),
                                      "--output", str(output)], capture_output=True, text=True, timeout=5)
            self.assertEqual(process.returncode, 2, process.stderr)
            self.assertIn("SHA-256 checksum mismatch: TaxiNodes.dbc, TaxiPath.dbc, Map.dbc", process.stderr)
            self.assertEqual(output.read_bytes(), b"previous complete catalog\n")


class GeneratedReferenceTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.raw = (DIRECTORY / "reference.json").read_bytes()
        cls.catalog = json.loads(cls.raw)

    def test_exact_generated_artifact_and_deterministic_encoding(self):
        self.assertEqual(hashlib.sha256(self.raw).hexdigest(), REFERENCE_SHA256)
        self.assertEqual(generate.encoded(self.catalog), self.raw)
        self.assertEqual(set(self.catalog), {"format", "claimedBuild", "nameLocale", "provenance", "nodes", "paths", "quality"})
        self.assertEqual((self.catalog["format"], self.catalog["claimedBuild"], self.catalog["nameLocale"]), (1, 12340, "frFR"))
        self.assertEqual(self.catalog["provenance"], dict(
            repository=generate.REPOSITORY, commit=generate.COMMIT,
            nodesSHA256=generate.NODES_SHA256, pathsSHA256=generate.PATHS_SHA256, mapSHA256=generate.MAP_SHA256,
            nodeURL=generate.RAW_ROOT + "/TaxiNodes.dbc", pathURL=generate.RAW_ROOT + "/TaxiPath.dbc", mapSource=generate.MAP_SOURCE))

    def test_all_raw_rows_and_quality_limits_are_retained(self):
        nodes, paths = self.catalog["nodes"], self.catalog["paths"]
        self.assertEqual(self.catalog["quality"], dict(nodes=364, paths=915, unnamedNodes=6, unmappedNodes=6,
                                                      invalidEndpointPaths=10, duplicateEndpointPairs=4, zeroPricePaths=145))
        self.assertEqual(len(nodes), 364)
        self.assertEqual(len(paths), 915)
        self.assertEqual([row["id"] for row in nodes], sorted({row["id"] for row in nodes}))
        self.assertEqual([row["id"] for row in paths], sorted({row["id"] for row in paths}))
        self.assertEqual({row["id"] for row in nodes if not row["mapReferenceKnown"]}, {81, 90, 161, 165, 178, 204})
        self.assertEqual({row["id"] for row in nodes if not row["name"]}, {216, 217, 220, 238, 353, 357})
        self.assertEqual({row["id"] for row in paths if not row["endpointsKnown"]}, {1247, 1248, 1252, 1813, 1814, 1815, 1816, 1817, 1818, 1819})
        self.assertEqual([row["id"] for row in paths if (row["source"], row["destination"]) == (175, 178)], [733, 793])
        self.assertEqual(sum(row["baseCopper"] == 0 for row in paths), 145)
        pairs = Counter((row["source"], row["destination"]) for row in paths)
        self.assertEqual(sum(count >= 2 for count in pairs.values()), 4)

    def test_explicit_fields_do_not_turn_unknown_facts_into_zero_defaults(self):
        node_ids = {row["id"] for row in self.catalog["nodes"]}
        for row in self.catalog["nodes"]:
            self.assertEqual(set(row), {"id", "mapId", "x", "y", "z", "name", "mapReferenceKnown"})
        for row in self.catalog["paths"]:
            self.assertEqual(set(row), {"id", "source", "destination", "baseCopper", "endpointsKnown"})
            self.assertEqual(row["endpointsKnown"], row["source"] in node_ids and row["destination"] in node_ids)
        node_two = next(row for row in self.catalog["nodes"] if row["id"] == 2)
        self.assertEqual(node_two["name"], "Hurlevent, Elwynn")


if __name__ == "__main__":
    unittest.main()
