"""Same-body policy changes are immediate CAS writes, never repair jobs."""
import hashlib
import importlib.machinery
import importlib.util
from pathlib import Path
import tempfile
import unittest

loader = importlib.machinery.SourceFileLoader('tested_self_edit', str(Path(__file__).with_name('self-edit')))
spec = importlib.util.spec_from_loader(loader.name, loader)
edit = importlib.util.module_from_spec(spec)
loader.exec_module(edit)


class RPC:
    def __init__(self):
        self.guid, self.sid, self.revision = '4', 'body-a', ''
        self.calls, self.error, self.replace = [], False, False

    def call(self, method, params):
        self.calls.append((method, params))
        if method == 'buddy-actions.reload_behavior':
            self.revision = hashlib.sha256(params['source'].encode()).hexdigest() if params['source'] else ''
            if self.error:
                raise edit.c.ConversationError('Lost response')
            if self.replace:
                self.sid = 'body-b'
        return dict(selfGuid=self.guid, sessionId=self.sid, revision=self.revision)


class SelfEditTests(unittest.TestCase):
    def test_direct_apply_readback_and_persistent_source(self):
        with tempfile.TemporaryDirectory() as folder:
            rpc = RPC()
            source = 'return {decide=function(s) return {} end}'
            result = edit.edit('apply', 4, 'body-a', rpc, source, '', folder)
            self.assertEqual(result['phase'], 'live')
            self.assertFalse(result['reconnected'])
            shown = edit.edit('show', 4, 'body-a', rpc, data=folder)
            self.assertEqual(shown['source'], source)
            self.assertEqual(shown['status']['revision'], result['revision'])
            writes = [p for m, p in rpc.calls if m.endswith('reload_behavior')]
            self.assertEqual(len(writes), 1)
            self.assertEqual(writes[0]['expectedRevision'], '')
            self.assertEqual(writes[0]['selfGuid'], '4')
            edit.edit('builtin', 4, 'body-a', rpc, expected=result['revision'], data=folder)
            self.assertEqual(rpc.revision, '')

    def test_stale_body_and_revision_refuse_before_write(self):
        for sid, revision in [('old-body', ''), ('body-a', 'f' * 64)]:
            with self.subTest(sid=sid, revision=revision), tempfile.TemporaryDirectory() as folder:
                rpc = RPC()
                with self.assertRaises(edit.c.ConversationError):
                    edit.edit('apply', 4, sid, rpc, 'return {}', revision, folder)
                self.assertFalse(any(m.endswith('reload_behavior') for m, p in rpc.calls))

    def test_lost_reply_keeps_exact_source_and_reconcile_is_read_only(self):
        with tempfile.TemporaryDirectory() as folder:
            rpc = RPC()
            rpc.error = True
            source = 'return {decide=function(s) return {} end}'
            with self.assertRaises(edit.c.ConversationError):
                edit.edit('apply', 4, 'body-a', rpc, source, '', folder)
            shown = edit.edit('show', 4, 'body-a', rpc, data=folder)
            self.assertEqual(shown['source'], source)
            self.assertEqual(sum(m.endswith('reload_behavior') for m, p in rpc.calls), 1)

    def test_changed_body_after_write_does_not_report_success(self):
        with tempfile.TemporaryDirectory() as folder:
            rpc = RPC()
            rpc.replace = True
            with self.assertRaises(edit.c.ConversationError):
                edit.edit('apply', 4, 'body-a', rpc, 'return {}', '', folder)

    def test_policy_change_during_status_read_is_not_reported_as_current(self):
        with tempfile.TemporaryDirectory() as folder:
            rpc = RPC()
            original = rpc.call
            def call(method, params):
                if method.endswith('status') and rpc.revision:
                    rpc.revision = 'd' * 64
                return original(method, params)
            rpc.call = call
            with self.assertRaises(edit.c.ConversationError):
                edit.edit('apply', 4, 'body-a', rpc, 'return {}', '', folder)

    def test_unknown_live_source_is_not_guessed(self):
        with tempfile.TemporaryDirectory() as folder:
            rpc = RPC()
            rpc.revision = 'e' * 64
            shown = edit.edit('show', 4, 'body-a', rpc, data=folder)
            self.assertFalse(shown['sourceKnown'])
            self.assertNotIn('source', shown)


if __name__ == '__main__':
    unittest.main()
