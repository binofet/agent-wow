package behavior

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestItemDisposalHostProjectionPreservesUnknownVersusObservedZero(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	path, err := filepath.Abs(filepath.Join("..", "..", "dev", "item_disposal.py"))
	if err != nil {
		t.Fatal(err)
	}
	// The real host projector must reject malformed JSON shapes before Lua
	// converts both empty objects and empty arrays to indistinguishable tables.
	const script = `
import copy
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("checked_item_disposal", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
base_fields = ("itemId", "itemClass", "subclass", "inventoryType", "quality", "flags", "flags2", "sellPrice",
               "requiredSkill", "requiredSkillRank", "requiredSpell", "requiredHonorRank", "requiredCityRank",
               "requiredReputationFaction", "requiredReputationRank")
raw_fields = ("containerSlots", "bonding", "pageText", "startQuest", "lockId", "maxDurability", "bagFamily", "duration")

class SpyBridge:
    def __init__(self, result=None):
        self.result = result if result is not None else {"status": "eligible", "reason": "Lua result"}
        self.calls = []
    def evaluate(self, source, value):
        self.calls.append((source, copy.deepcopy(value)))
        return copy.deepcopy(self.result)

def item():
    return {"itemId": 7070, "available": True, "itemClass": 15, "sellPrice": 4, "rawTemplateFacts": {}}

original = item()
before = copy.deepcopy(original)
projected = module.project(original)
assert type(projected) is dict
assert set(projected) == {"available", "hasSpell", "rawTemplateFacts", *base_fields}
assert set(projected["rawTemplateFacts"]) == set(raw_fields)
assert all(type(v) is int and v == 0 for v in projected["rawTemplateFacts"].values())
assert projected["rawTemplateFacts"] is not original["rawTemplateFacts"]
assert original == before
spy = SpyBridge()
assert module.classify(original, bridge=spy) == spy.result
assert len(spy.calls) == 1 and spy.calls[0][1] == {"mode": "vendor_sale", "item": projected}

invalid = [None, [], "item"]
old = item(); del old["rawTemplateFacts"]; invalid.append(old)
for raw in (None, False, 0, "unknown", [], [1]):
    value = item(); value["rawTemplateFacts"] = raw; invalid.append(value)
for key, bad in (("available", 1), ("available", False), ("pending", 0), ("pending", True),
                 ("itemId", 0), ("itemId", 2**31), ("itemId", "7070"), ("flags", False),
                 ("quality", 0.0), ("sellPrice", -1), ("sellPrice", 2**32), ("hasSpell", 0)):
    value = item(); value[key] = bad; invalid.append(value)
for field in raw_fields:
    for bad in (False, -1, 0.5, "0", 2**32):
        value = item(); value["rawTemplateFacts"][field] = bad; invalid.append(value)
for value in invalid:
    before = copy.deepcopy(value)
    spy = SpyBridge()
    assert module.project(value) is None, value
    assert module.classify(value, bridge=spy)["status"] == "unknown", value
    assert spy.calls == [], "malformed fact reached Lua"
    assert value == before, "projector mutated input"

# Display fields and unused metadata cannot consume the evaluator frame budget.
large = item()
large["name"] = "display" * 20000
large["unusedMetadata"] = {"rows": ["unused" * 20 for _ in range(5000)]}
large["rawTemplateFacts"]["unusedDisplay"] = "ignored" * 20000
before = copy.deepcopy(large)
spy = SpyBridge({"status": "keep", "reason": "policy keeps this item"})
assert module.classify(large, "misc_destroy", bridge=spy) == spy.result
frame = spy.calls[0][1]
assert frame["mode"] == "misc_destroy" and frame["item"] == projected
assert len(json.dumps(frame, separators=(",", ":")).encode()) < 2048
assert large == before

for result in (None, [], "eligible", {"status": "sell", "reason": "wrong status"},
               {"status": True, "reason": "wrong type"}, {"status": "eligible"},
               {"status": "eligible", "reason": ""}, {"status": "eligible", "reason": 1},
               {"status": "eligible", "reason": "x" * 129},
               {"status": "eligible", "reason": "extra action", "action": "sell"}):
    spy = SpyBridge()
    spy.result = result
    assert module.classify(item(), bridge=spy)["status"] == "unknown", result
    assert len(spy.calls) == 1
for status in ("eligible", "keep", "unknown"):
    spy = SpyBridge({"status": status, "reason": "unchanged Lua decision"})
    assert module.classify(item(), bridge=spy) == spy.result
class FailingBridge:
    def evaluate(self, source, value):
        raise module.runtime.Error("evaluator rejected this frame")
assert module.classify(item(), bridge=FailingBridge())["status"] == "unknown"

with tempfile.TemporaryDirectory() as directory:
    root = Path(directory)
    source = root / "item-disposal.lua"
    source.write_text('return {decide=function(input) return {status="keep",reason="first"} end}')
    spy = SpyBridge({"status": "keep", "reason": "first"})
    assert module.classify(item(), bridge=spy, policy=source) == spy.result
    first_frame = copy.deepcopy(spy.calls[-1][1])
    source.write_text('return {decide=function(input) return {status="eligible",reason="edited"} end}')
    spy.result = {"status": "eligible", "reason": "edited"}
    assert module.classify(item(), bridge=spy, policy=source) == spy.result
    assert spy.calls[0][0] != spy.calls[1][0] and spy.calls[1][0] == source.read_text()
    assert first_frame == spy.calls[1][1], "policy edit changed observed facts"
    spy = SpyBridge()
    assert module.classify(item(), bridge=spy, policy=root / "missing.lua")["status"] == "unknown"
    assert spy.calls == []
    with patch.dict(os.environ, {"BUDDY_LUAPOLICY_BINARY": str(root / "missing-evaluator"),
                                 "BUDDY_LUAPOLICY_TEST_BINARY": str(root / "missing-evaluator")}):
        assert module.classify(item(), policy=source)["status"] == "unknown"
`
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, python, "-B", "-c", script, path).CombinedOutput()
	if err != nil {
		t.Fatalf("host projection boundary failed: %v\n%s", err, output)
	}
}
