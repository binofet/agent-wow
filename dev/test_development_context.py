"""Synthetic editing-task briefing and real DirectBrain API-copy coverage."""
import copy
import json
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import direct_context
import direct_tools
from test_direct_brain import PublicRPC, chat, converse, spoken


def fixture(workspace):
    return dict(character=dict(guid="4", name="Mira", **{"class": "mage"}),
        development=dict(enabled=True, directDevelopment=True, workspace=str(workspace),
            rpcUrl=PublicRPC.url, sessionId=PublicRPC.core, selfGuid="4", ownerGuid="3",
            policyRevision="a"*64, policyCommands=dict(show=["existing", "show"])),
        current_request=dict(source="party_agent", senderGuid="87", verifiedPeer=True,
            developmentWhisper=True, controls_allowed=False, messages=[dict(text="Edit your own avoidance rule.")]),
        controls=dict(sessionId=PublicRPC.core, selfGuid="4", castPending=True,
            position=dict(known=True, mapKnown=True, mapId=0, visitId="9"),
            luaPolicy=dict(revision="a"*64, pending=[dict(id="old", outcome="UNKNOWN")])),
        observations=dict(visitRevision="9", self=dict(guid="4", present=True, mapKnown=True,
            mapId=0, positionKnown=True, x=0, positionEstimated=True, positionAgeMs="101"),
            owner=dict(guid="3", present=False, aliveKnown=False),
            nearbyCreatures=[dict(guid=str(1000+i), entry=12000+i, present=True, mapKnown=True,
                mapId=0, positionKnown=True, x=i, positionAgeMs="200", rawFacts="captured-fact-"*90,
                activeCast=dict(spellId=21070, known=True)) for i in range(180)]),
        abilities={str(i):dict(known=True, detail="ability-fact-"*30) for i in range(120)},
        spell_toolkit=dict(known=True, revision="19", spells=[dict(spellId=i, mechanics="mechanics-"*200) for i in range(120)]),
        inventory=dict(known=True, revision="20", items=[dict(entry=i, fact="item-"*300) for i in range(80)],
            operation=dict(status="OUTCOME_UNCONFIRMED", itemGuid="9007199254740993", originalArgs=dict(count=1))),
        party=dict(known=True, inGroup=True, leaderGuid="3", members=[dict(guid="3", online=True),dict(guid="4", online=True)]),
        agentContext=dict(activeNotes=[dict(text="Preserve my learned note", sourceRequestId="previous")]),
        missions=dict(known=True, goals=[dict(goalId="long-term", objective="Reach level 20")]),
        currentUnresolvedCustody=[dict(id="original-custody", outcome="UNKNOWN")])


class DevelopmentContextTests(unittest.TestCase):
    def test_compact_sections_keep_identity_goals_and_complete_unresolved_custody(self):
        state=fixture(Path("/synthetic/owned")); original=copy.deepcopy(state)
        brief,metrics=direct_context.ordinary_context(state)
        legacy=copy.deepcopy(state);legacy["current_request"]["developmentWhisper"]=False
        old,_=direct_context.ordinary_context(legacy)
        self.assertLess(metrics["contextBytes"],len(direct_context.encode(old))//5)
        self.assertEqual(brief["contextProjection"]["mode"],"verified_development_whisper")
        for key in ("character","current_request","controls","development","party","agentContext","missions","currentUnresolvedCustody"):
            self.assertEqual(brief[key],state[key])
        self.assertEqual(brief["observations"]["self"],state["observations"]["self"])
        self.assertEqual(brief["observations"]["owner"],state["observations"]["owner"])
        self.assertEqual(brief["observations"]["nearbyCreatures"]["contextPath"],["observations","nearbyCreatures"])
        self.assertNotIn("rows",brief["observations"]["nearbyCreatures"])
        self.assertIn(dict(path=["inventory","operation"],value=state["inventory"]["operation"]),
            brief["contextProjection"]["unresolvedCustody"])
        self.assertLess(len(brief["contextProjection"]["omittedPaths"]),10)
        self.assertEqual(state,original)

    def test_unresolved_bulk_record_is_never_clipped_or_relabelled(self):
        state=fixture(Path("/synthetic/owned"))
        unknown=dict(status="OUTCOME_UNCONFIRMED", requestId="exact-original", originalArgs=dict(text="x"*40000))
        state["inventory"]["operation"]=unknown
        brief,_=direct_context.ordinary_context(state)
        self.assertIn(dict(path=["inventory","operation"],value=unknown),brief["contextProjection"]["unresolvedCustody"])
        self.assertGreater(len(direct_context.encode(brief)),direct_context.CONTEXT_BYTES)
        self.assertFalse(brief["observations"]["owner"]["present"])
        self.assertEqual(brief["observations"]["self"]["mapId"],0)

    def test_only_verified_enabled_bound_peer_development_whispers_change(self):
        state=fixture(Path("/synthetic/owned"))
        variants=[("current_request","source","owner"),("current_request","source","combat_review"),
            ("current_request","verifiedPeer",False),("current_request","verifiedPeer",1),
            ("current_request","developmentWhisper",False),("development","enabled",False),
            ("development","directDevelopment",False),("development","selfGuid","8"),
            ("development","sessionId",""),("controls","sessionId","other-body")]
        for section,key,value in variants:
            with self.subTest(section=section,key=key,value=value):
                candidate=copy.deepcopy(state);candidate[section][key]=value
                actual=direct_context.ordinary_context(candidate)
                with patch.object(direct_context,"_verified_development_whisper",return_value=False):
                    unchanged=direct_context.ordinary_context(candidate)
                self.assertEqual(actual,unchanged)
        small=dict(character=dict(guid="4"),current_request=dict(source="owner"))
        self.assertEqual(direct_context.ordinary_context(small)[0],small)

    def test_real_brain_wire_preserves_schemas_users_and_full_context_reads(self):
        with tempfile.TemporaryDirectory() as folder, patch.object(socket.socket,"connect",side_effect=AssertionError("network forbidden")):
            root=Path(folder);workspace=root/"workspace";workspace.mkdir()
            subprocess.run(["git","init","--quiet",str(workspace)],check=True)
            (root/"state").mkdir();(root/"other").mkdir()
            state=fixture(workspace)
            request="Please inspect and edit your own avoidance function; preserve pending operations."
            state["current_request"]["messages"]=[dict(text=request)]
            original=copy.deepcopy(state);history=[dict(speaker="Lotharen",text=request)]
            rpc=PublicRPC();captured=[];instances=[]
            real_init=direct_tools.DirectTools.__init__
            def init(tools,*args,**kwargs):
                real_init(tools,*args,**kwargs);instances.append(tools)
            responses=[chat(("spell","context_read",dict(path=["spell_toolkit","spells","119","mechanics"])),
                ("creature","context_read",dict(path=["observations","nearbyCreatures","179","rawFacts"])),
                ("custody","context_read",dict(path=["inventory","operation"]))),spoken()]
            def api(payload,*_):
                captured.append(copy.deepcopy(payload));return responses.pop(0)
            with patch.object(converse,"ROOT",workspace),patch.object(converse,"RPC",return_value=rpc), \
                 patch.object(direct_tools.DirectTools,"__init__",init):
                brain=converse.DirectBrain(root/"state",developer_workspace=workspace,character_name="Mira",character_class="mage",
                    model="synthetic",provider="local",base_url="http://127.0.0.1:1/v1")
                self.assertTrue(brain.development_enabled(state))
                with patch.object(brain.client,"_http",side_effect=api):brain.generate(history,state)
                unprojected=[]
                with patch.object(direct_context,"_verified_development_whisper",return_value=False), \
                     patch.object(brain.client,"_http",side_effect=lambda payload,*_:unprojected.append(copy.deepcopy(payload)) or spoken()):
                    other=converse.DirectBrain(root/"other",developer_workspace=workspace,character_name="Mira",character_class="mage",
                        model="synthetic",provider="local",base_url="http://127.0.0.1:1/v1")
                    with patch.object(other.client,"_http",side_effect=lambda payload,*_:unprojected.append(copy.deepcopy(payload)) or spoken()):
                        other.generate(history,state)
            self.assertEqual(captured[0]["tools"],unprojected[0]["tools"])
            system=captured[0]["messages"][0]["content"]
            brief=json.loads(system.split("Current observations and control state JSON:\n",1)[1])
            self.assertEqual(brief["contextProjection"]["mode"],"verified_development_whisper")
            self.assertEqual([r for r in captured[0]["messages"] if r["role"]=="user"],[dict(role="user",content=request)])
            results={r["tool_call_id"]:json.loads(r["content"])["result"] for r in captured[1]["messages"] if r["role"]=="tool"}
            self.assertEqual(results["spell"]["value"],state["spell_toolkit"]["spells"][119]["mechanics"])
            self.assertEqual(results["creature"]["value"],state["observations"]["nearbyCreatures"][179]["rawFacts"])
            self.assertEqual(instances[0].context["inventory"]["operation"],state["inventory"]["operation"])
            self.assertEqual(instances[0].context["conversationHistory"],history)
            self.assertEqual(state,original);self.assertFalse(rpc.commands or rpc.replies)
            self.metrics=dict(fullStateBytes=len(direct_context.encode(state)),legacySystemBytes=len(unprojected[0]["messages"][0]["content"].encode()),
                developmentSystemBytes=len(system.encode()),briefBytes=len(direct_context.encode(brief)),toolSchemaBytes=len(direct_context.encode(captured[0]["tools"])))
            print("DEVELOPMENT_BRIEF_METRICS",json.dumps(self.metrics,sort_keys=True))


if __name__ == "__main__":unittest.main()
