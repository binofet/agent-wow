"""Bounded fact transport to the shared starter objective policy."""
import importlib.machinery
import importlib.util
import os
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SOURCE = ROOT / "behaviors/starter-objectives.lua"


def evaluate(facts):
    loader = importlib.machinery.SourceFileLoader("starter_objective_runtime", str(ROOT / "dev/task-runtime"))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    runtime = importlib.util.module_from_spec(spec)
    loader.exec_module(runtime)
    binary = os.environ.get("BUDDY_LUAPOLICY_BINARY", os.environ.get("BUDDY_LUAPOLICY_TEST_BINARY", ROOT / "data/bin/luapolicy"))
    try:
        return runtime.Bridge(binary, ROOT / "modules").evaluate(SOURCE.read_text(), facts)
    except (runtime.Error, OSError, UnicodeError):
        return {"status": "pending"}
