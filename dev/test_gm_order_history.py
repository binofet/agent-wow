"""Exercise long owner conversations through the normal local journal and simulated native GM writer."""
import copy
import json
from pathlib import Path
import tempfile
import unittest

import direct_model
import direct_tools
import test_direct_model as model
import test_gm_command_evidence as commands
import test_gm_lookup_context as lookup


class OwnerOrderHistoryTests(unittest.TestCase):
    def test_105_distinct_owner_orders_restore_original_receipts_without_replay(self):
        query = lookup.LookupContextTests()
        try:
            lookup_args, broad, _ = query.captured([dict(text=lookup.item_line(14047,"Runecloth"))])
        finally:
            query.doCleanups()
        rig = commands.GMCommandEvidenceTests()
        rig.setUp()
        self.addCleanup(rig.doCleanups)
        functions = [direct_tools.context_tool_schema(),dict(type="function",function=dict(name="gm_execute",
            parameters=dict(type="object",properties=dict(command=dict(type="string")),required=["command"])))]
        history, archive, issued, peaks = {}, {}, [], []
        originals = {}
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp)/"owner.json"
            current = ""
            def enter(number, text, replies):
                nonlocal current
                current = f"owner-{number}"
                rig.tools.request_id = current
                def dispatch(name,args):
                    if name=="context_read":
                        return dict(ok=True,result=direct_tools.context_page({"conversationArchive":archive},**args))
                    command = args["command"]
                    if command==lookup_args["command"]: return copy.deepcopy(broad)
                    issued.append((current,command))
                    return rig.execute(command,operation=current)
                def index(past):
                    history.clear()
                    history.update(past)
                    archive.update(past)
                def retain(ref, original):
                    archive[str(ref["transcriptMessage"])] = dict(content=original,reference=ref)
                # Deterministic fixture-only template overhead. Native production keeps its own exact tokenizer.
                client = model.LocalPromptBudgetTests.Transport(replies,local_prompt_token_limit=40960,
                    local_token_counter=lambda p:32768+len(direct_tools.packed(p))//4)
                response=client.generate("Bound current GM instructions",[dict(role="user",content=text)],None,
                    functions,dispatch,natural=True,transcript_path=path,conversation_key="same-bound-character",
                    request_id=current,read_only_tools={"context_read"},archive_read=retain,archive_history=index)
                peaks.append(max(32768+len(direct_tools.packed(p))//4 for p in client.sent))
                return client,response
            for i in range(105):
                # Reuse model call IDs across different genuine owner requests as real providers can do.
                args = dict(command=f".additem Joel {12000+i} 2")
                text = f"Please get two of crafting material {12000+i} for Joel."
                replies = [model.chat(("lookup","gm_execute",lookup_args))] if i in (0,52,104) else []
                replies.extend([model.chat(("grant","gm_execute",args)),model.ActiveContextTests.text()])
                client,_ = enter(i,text,replies)
                self.assertEqual(client.sent[0]["messages"][-1],dict(role="user",content=text))
                if i in (0,52):
                    row = next(row for row in reversed(client.sent[-1]["messages"]) if row.get("tool_call_id")=="grant")
                    originals[i] = row["content"]
            self.assertEqual(len(issued),105)
            self.assertEqual(len(rig.rpc.commands),105)
            self.assertGreaterEqual(len(history["turns"]),100)
            self.assertLessEqual(max(peaks),40960)
            self.assertLessEqual(len(client.sent[0]["messages"]),12)
            saved=json.loads(path.read_text())
            for i,original in originals.items():
                request=saved["requests"][f"owner-{i}"]
                turn=history["turns"][str(request["firstMessage"])]
                position=turn["issuedCommands"][-1]["message"]
                self.assertEqual((turn["requestId"],turn["lastMessage"]),(f"owner-{i}",request["endMessage"]-1))
                self.assertEqual(history["messages"][str(position)]["content"],original)
                self.assertEqual(saved["messages"][position]["content"],original)
            # Same input/request ID is already entered; different input on it cannot replace it either.
            for text,reason in [(text,'conversation_request_already_entered'),('A different order','conversation_request_identity_changed')]:
                with self.assertRaisesRegex(direct_model.ModelError,reason): enter(104,text,[])
            self.assertEqual(len(rig.rpc.commands),105)

    def test_newer_unknown_identical_order_supersedes_the_location_of_a_prior_written_one(self):
        text=dict(role="user",content="Give Joel two more.")
        args=dict(command=".additem Joel 1234 2")
        def turn(value):
            return [text,model.chat(("grant","gm_execute",args))["choices"][0]["message"],
                dict(role="tool",tool_call_id="grant",content=json.dumps(value)),dict(role="assistant",content="I checked the result.")]
        correct=model.LocalPromptBudgetTests.gm_selection_receipt(args,"older-issued")
        lost=dict(ok=False,outcomeUnknown=True,replayAllowed=False,operationId="later-unknown")
        history=turn(correct)+turn(lost)+[dict(role="user",content="Are you present?"),
            model.chat(("read","observe",{}))["choices"][0]["message"],
            dict(role="tool",tool_call_id="read",content='{"ok":true}'),dict(role="assistant",content="Yes.")]
        requests={"first":{"firstMessage":0,"endMessage":4},"unknown":{"firstMessage":4,"endMessage":8},
            "presence":{"firstMessage":8,"endMessage":12}}
        keep, archive, note=direct_model._gm_history_window(history,len(history),requests,{"observe"},{},[text],100000)
        self.assertNotIn("lastIdenticalOwnerTextTurn",note)
        self.assertIn(6,keep)
        self.assertIn("0",archive["turns"])
        self.assertEqual(archive["messages"]["2"]["content"],history[2]["content"])

    def test_legacy_digest_splits_an_unanswered_whisper_from_a_real_multimessage_order(self):
        from hashlib import sha256
        a,b,c=(dict(role="user",content=value) for value in ("Earlier unserved cue","The new order","Keep this condition"))
        later=dict(role="user",content="A later user request")
        current=dict(role="user",content="A fresh check")
        history=[a,b,c,dict(role="assistant",content="Reply to the newer pair"),later,dict(role="assistant",content="Yes.")]
        requests={key:dict(inputSHA256=sha256(direct_model._packed(rows,10000)).hexdigest())
            for key,rows in (("old",[a]),("pair",[b,c]),("later",[later]))}
        keep,archived,_=direct_model._gm_history_window(history,len(history),requests,set(),{},[current],10000)
        self.assertIn(0,keep)
        self.assertNotIn("0",archived["turns"])
        self.assertEqual(archived["turns"]["1"]["ownerMessages"],[1,2])
        self.assertEqual(archived["turns"]["1"]["requestId"],"pair")
        self.assertEqual(archived["turns"]["1"]["assistantReply"],3)


if __name__=="__main__": unittest.main()
