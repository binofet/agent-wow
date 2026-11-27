import copy
import hashlib
import json
import unittest

import direct_tools
from gm_lookup_context import project_lookup_feedback
import test_gm_command_evidence as evidence


def item_line(entry, name):
    return f"{entry} - |cffffffff|Hitem:{entry}:0:0:0:0:0:0:0:0:0|h[{name}]|h|r "


class LookupContextTests(unittest.TestCase):
    def captured(self, extra):
        # Exercise the ordinary command writer and Chat collector against the existing simulated session.
        rig = evidence.GMCommandEvidenceTests()
        rig.setUp()
        self.addCleanup(rig.doCleanups)
        rig.rpc.feedback_enabled = False
        rows = [dict(text=item_line(18000+i, f"Runecloth Swatch {i}")) for i in range(33)] + extra
        def deliver():
            if len(rig.rpc.messages) == 10:
                rig.rpc.messages.extend(dict(row, sequence=str(i)) for i, row in enumerate(rows, 11))
        rig.on_wait = deliver
        args = dict(command=".lookup item Runecloth")
        result = rig.execute(args["command"])
        self.assertEqual(rig.rpc.commands,[args["command"]])
        original = direct_tools.packed(result)
        ref = dict(transcriptMessage=6, toolCallId="lookup-current", resultBytes=len(original),
            resultSHA256=hashlib.sha256(original).hexdigest(), contextPath=["conversationArchive","6","content"])
        return args, result, ref

    def test_late_exact_item_in_large_native_reply_and_complete_bounded_readback(self):
        args, result, ref = self.captured([dict(text=item_line(14047, "Runecloth"))])
        original = copy.deepcopy(result)
        shown = project_lookup_feedback(result,args,ref)
        self.assertEqual(result,original)
        feedback = shown["result"]["feedback"]
        lookup = feedback["itemLookup"]
        self.assertEqual(lookup["exactItems"],[dict(itemId=14047,name="Runecloth",sequence="44")])
        self.assertEqual((lookup["capturedMessageCount"],lookup["capturedSystemMessageCount"],
            lookup["capturedMatchingItemRows"],lookup["distinctExactItemCount"]),(34,34,34,1))
        self.assertEqual(feedback["messages"][-1],result["result"]["feedback"]["messages"][-1])
        self.assertEqual(feedback["systemMessages"][-1],result["result"]["feedback"]["systemMessages"][-1])
        self.assertLess(len(direct_tools.packed(feedback))*2,len(direct_tools.packed(original["result"]["feedback"])))
        self.assertEqual({k:v for k,v in shown["result"].items() if k!="feedback"},
                         {k:v for k,v in result["result"].items() if k!="feedback"})
        self.assertEqual(feedback["correlation"],"temporal_only")
        self.assertFalse(feedback["commandSuccessKnown"])
        archive = {"conversationArchive":{"6":{"content":direct_tools.packed(result).decode()}}}
        ptr = lookup["fullReceipt"]
        parts, offset = [], 0
        while True:
            page = direct_tools.context_page(archive,ptr["contextPath"],offset,900)
            parts.append(page["value"])
            if not page["truncated"]:
                break
            offset = page["nextOffset"]
        self.assertGreater(len(parts),1)
        full = "".join(parts).encode()
        self.assertEqual((full,len(full),hashlib.sha256(full).hexdigest()),
                         (direct_tools.packed(original),ptr["resultBytes"],ptr["resultSHA256"]))
        self.assertEqual(json.loads(full)["result"]["feedback"],original["result"]["feedback"])

    def test_mixed_chat_multiple_exact_ids_and_latest_witness(self):
        args, result, ref = self.captured([
            dict(text=item_line(14047,"RUNEcloth")),
            dict(text=item_line(19,"Runecloth"),channel="WHISPER",language=7,senderGuid="3"),
            dict(text="34 results shown; others may follow."),
            dict(text=item_line(22,"Runecloth").replace("22 -","24 -")),
            dict(text=item_line(18003,"Unrelated goods")),
            dict(text=item_line(32,"Runecloth"), pendingReceipt={"outcomeUnknown":True}),
            dict(text=item_line(27,"Runecloth"),receiverGuid="71"),
            dict(text=item_line(14047,"Runecloth"),receiverGuid="87"),
            dict(text=item_line(14048,"Runecloth")),
        ])
        f = project_lookup_feedback(result,args,ref)["result"]["feedback"]
        self.assertEqual(f["itemLookup"]["exactItems"],[
            dict(itemId=14048,name="Runecloth",sequence="52"), dict(itemId=14047,name="Runecloth",sequence="51")])
        self.assertEqual(f["itemLookup"]["distinctExactItemCount"],2)
        raw = result["result"]["feedback"]
        for row in raw["messages"][-8:-2]:
            self.assertIn(row,f["messages"])
            if row in raw["systemMessages"]:
                self.assertIn(row,f["systemMessages"])
        self.assertNotIn(raw["messages"][-8],f["systemMessages"])

    def test_no_exact_stays_observational_and_rejects_crossed_windows(self):
        args, result, ref = self.captured([])
        f = project_lookup_feedback(result,args,ref)["result"]["feedback"]
        self.assertEqual(f["itemLookup"]["exactItems"],[])
        self.assertEqual(f["itemLookup"]["distinctExactItemCount"],0)
        self.assertEqual(f["readMore"],result["result"]["feedback"]["readMore"])
        for change in ("duplicate","foreign-subset","stale","lost","wrong-command","unknown","no-archive"):
            with self.subTest(change=change):
                failed, location = copy.deepcopy(result), ref
                r = failed["result"]
                feedback = r["feedback"]
                if change == "duplicate":
                    feedback["systemMessages"][2]["sequence"] = feedback["systemMessages"][1]["sequence"]
                    feedback["messages"][2]["sequence"] = feedback["messages"][1]["sequence"]
                if change == "foreign-subset":
                    feedback["systemMessages"] = [dict(row,senderGuid="7") for row in feedback["systemMessages"]]
                if change == "stale":
                    feedback["afterSequence"] = feedback["nextSequence"]
                if change == "lost":
                    feedback["cursorLost"] = True
                if change == "wrong-command":
                    r["command"] = ".additem 14047 5"
                if change == "unknown":
                    r["commandOutcomeUnknown"] = True
                if change == "no-archive":
                    location = {}
                self.assertIsNone(project_lookup_feedback(failed,args,location))


if __name__ == "__main__":
    unittest.main()
