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
        if method == 'session.policy_source':
            raise edit.c.CapabilityUnavailable('older client')
        if method == 'buddy-actions.reload_behavior':
            self.revision = hashlib.sha256(params['source'].encode()).hexdigest() if params['source'] else ''
            if self.error:
                raise edit.c.ConversationError('Lost response')
            if self.replace:
                self.sid = 'body-b'
        return dict(selfGuid=self.guid, sessionId=self.sid, revision=self.revision)


class SpellInspectionTests(unittest.TestCase):
    def rpc(self):
        class SpellRPC:
            def __init__(self):self.calls=[];self.replacement=None
            def call(self,method,params):
                self.calls.append((method,params))
                if method=='buddy-actions.status':return dict(sessionId=self.replacement or 'body-a',selfGuid='4')
                if method=='buddy-actions.spell_toolkit':return dict(sessionId='body-a',selfGuid='4',spellbookKnown=True,
                    spellbookRevision='9',spells=[dict(spellId=10,name='Blizzard',metadataKnown=True,channeled=True,
                        genericCastSupported=True,classificationReason='automatic_selection_unclassified',
                        mechanics=dict(targetMask=64,effects=[dict(targetSelectorA=8,targetSelectorB=0)])),
                        dict(spellId=4294967294,metadataKnown=False,genericCastSupported=True,classificationReason='metadata_unknown')])
                if method=='session.describe':return dict(sessionId='body-a',characterGuid='4',revision='catalog-1',
                    methods=[dict(name='buddy-actions.cast_spell',inputType='CastSpellRequest',effect='mutation'),
                        dict(name='buddy-actions.cast_self',inputType='SelfCastRequest',effect='mutation')],
                    messages={'CastSpellRequest':dict(fields=[dict(name='targets',kind='message',type='Targets')]),
                        'Targets':dict(fields=[dict(name='destination',kind='message',type='Position')]),
                        'Position':dict(fields=[dict(name='x',kind='double')]),'SelfCastRequest':dict(fields=[])})
                raise AssertionError('Unexpected mutation: '+method)
        return SpellRPC()

    def test_exact_learned_raw_mechanics_and_installed_cast_inputs_are_read_only(self):
        rpc=self.rpc()
        result=edit.inspect_spell(4,'body-a',rpc,10)
        self.assertTrue(result['learned']);self.assertTrue(result['spell']['channeled'])
        self.assertEqual(result['spell']['mechanics']['effects'][0]['targetSelectorA'],8)
        self.assertEqual(set(result['castInputs']),{'CastSpellRequest','Targets','Position','SelfCastRequest'})
        self.assertEqual([m for m,_ in rpc.calls],['buddy-actions.status','buddy-actions.spell_toolkit','session.describe','buddy-actions.status'])
        unfamiliar=edit.inspect_spell(4,'body-a',rpc,4294967294)
        self.assertTrue(unfamiliar['learned']);self.assertFalse(unfamiliar['spell']['metadataKnown'])
        self.assertTrue(unfamiliar['spell']['genericCastSupported'])
        self.assertFalse(edit.inspect_spell(4,'body-a',rpc,999)['learned'])

    def test_foreign_body_and_replacement_fail_without_cast_or_reload(self):
        rpc=self.rpc()
        original=rpc.call
        def call(method,params):
            result=original(method,params)
            if method=='session.describe':rpc.replacement='replacement'
            return result
        rpc.call=call
        with self.assertRaisesRegex(edit.c.ConversationError,'body changed'):
            edit.inspect_spell(4,'body-a',rpc,10)
        self.assertFalse(any('cast_' in method or 'reload' in method for method,_ in rpc.calls))


class SelfEditTests(unittest.TestCase):
    def test_composed_source_uses_reflected_host_budget_and_exact_readback(self):
        class SessionRPC(RPC):
            def __init__(self):
                super().__init__();self.source='return {}';self.revision=edit.digest(self.source)
            def call(self,method,params):
                self.calls.append((method,params))
                if method=='session.reload_policy':self.source=params['source'];self.revision=edit.digest(self.source)
                return dict(characterGuid=self.guid,sessionId=self.sid,revision=self.revision,source=self.source,sourceLimitBytes=512*1024)
        with tempfile.TemporaryDirectory() as folder:
            rpc=SessionRPC();source='return {}\n--'+'x'*(256*1024)
            candidate=Path(folder)/'source.lua';candidate.write_text(source)
            self.assertEqual(edit.read_source(candidate),source)
            result=edit.edit('apply',4,'body-a',rpc,source,rpc.revision,folder)
            self.assertEqual(result['revision'],edit.digest(source));self.assertEqual(rpc.source,source)
            with self.assertRaisesRegex(edit.c.ConversationError,'configured host source budget'):
                edit.edit('apply',4,'body-a',rpc,source+'x'*(300*1024),rpc.revision,folder)
            self.assertEqual(sum(m=='session.reload_policy' for m,p in rpc.calls),1)

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

    def test_session_edit_installs_source_on_the_attached_runtime(self):
        class SessionRPC(RPC):
            def __init__(self):
                super().__init__()
                self.source = self.builtin = 'return {decide=function() return {} end}'
                self.revision = edit.digest(self.builtin)

            def call(self, method, params):
                self.calls.append((method, params))
                if method == 'session.reload_policy':
                    self.source = self.builtin if params.get('useBuiltin') else params['source']
                    self.revision = edit.digest(self.source)
                return dict(characterGuid=self.guid, sessionId=self.sid, revision=self.revision,
                            source=self.source)

        with tempfile.TemporaryDirectory() as folder:
            rpc = SessionRPC()
            source = 'return {decide=function() return {new=true} end}'
            expected = rpc.revision
            result = edit.edit('apply', 4, 'body-a', rpc, source, expected, folder)
            self.assertEqual(result['revision'], edit.digest(source))
            self.assertFalse(result['reconnected'])
            shown = edit.edit('show', 4, 'body-a', rpc, data=folder)
            self.assertEqual((shown['runtime'], shown['source']), ('session', source))
            edit.edit('builtin', 4, 'body-a', rpc, expected=result['revision'], data=folder)
            self.assertEqual(rpc.source, rpc.builtin)
            self.assertTrue(all(method.startswith('session.') for method, _ in rpc.calls))

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

class DurablePolicyTests(unittest.TestCase):
    def fixture(self, folder):
        root=Path(folder)/'retained'; source=Path(folder)/'profile.lua'
        source.write_text('return {decide=function() return {} end}')
        identity=dict(profile='mage',character='Mira',character_guid=4,rpc_port=8087)
        release=dict(AGENT_WOW_POLICY_FILE=str(source),AGENT_WOW_POLICY_CONTEXT='encounter')
        return root,source,identity,release

    def rpc(self, source, sid='body-a', lost=False, crash_readback=False):
        class SessionRPC:
            def __init__(self):self.source=source;self.sid=sid;self.calls=[];self.entered=False
            def call(self,method,params):
                self.calls.append(method)
                if method=='session.reload_policy':
                    self.source=params.get('source',source);self.entered=True
                    if lost:raise edit.c.ConversationError('Lost reply')
                if self.entered and crash_readback:raise edit.c.ConversationError('Process crashed before readback')
                return dict(characterGuid='4',sessionId=self.sid,revision=edit.digest(self.source),source=self.source)
        return SessionRPC()

    def test_confirmed_source_survives_two_sessions_without_runtime_checkpoint(self):
        with tempfile.TemporaryDirectory() as folder:
            root,source,identity,release=self.fixture(folder)
            launch=edit.policy_binding.prepare(root,4,identity,source,release)
            edit.policy_binding.ready(root,4,launch,'body-a')
            rpc=self.rpc(source.read_text()); learned=source.read_text()+'\n-- learned improvement'
            result=edit.edit('apply',4,'body-a',rpc,learned,edit.digest(rpc.source),root)
            self.assertTrue(result['startupBindingConfirmed'])
            second=edit.policy_binding.prepare(root,4,identity,source,release)
            self.assertEqual(Path(second['policyFile']).read_text(),learned)
            edit.policy_binding.ready(root,4,second,'body-b')
            # Only source was selected; no old checkpoint/action/token is transferred.
            self.assertEqual(set(second),{'anchor','revision','policyFile'})
            self.assertIsNone(edit.policy_binding.capture(root/'4','body-a',edit.digest(learned)))
            new=self.rpc(learned,'body-b')
            self.assertTrue(edit.edit('apply',4,'body-b',new,learned+'\n-- second',edit.digest(learned),root)['startupBindingConfirmed'])

    def test_lost_reply_or_crash_before_confirmed_readback_never_promotes_candidate(self):
        for lost,crash in ((True,False),(False,True)):
            with self.subTest(lost=lost),tempfile.TemporaryDirectory() as folder:
                root,source,identity,release=self.fixture(folder)
                launch=edit.policy_binding.prepare(root,4,identity,source,release)
                edit.policy_binding.ready(root,4,launch,'body-a')
                rpc=self.rpc(source.read_text(),lost=lost,crash_readback=crash)
                candidate=rpc.source+'\n-- uncertain'
                with self.assertRaises(edit.c.ConversationError):
                    edit.edit('apply',4,'body-a',rpc,candidate,edit.digest(rpc.source),root)
                self.assertTrue((root/'4'/(edit.digest(candidate)+'.lua')).exists())
                second=edit.policy_binding.prepare(root,4,identity,source,release)
                self.assertEqual(second['policyFile'],str(source))
                self.assertFalse((root/'4'/'accepted.json').exists())

    def test_new_cohort_or_custom_source_wins_and_old_capture_cannot_promote(self):
        with tempfile.TemporaryDirectory() as folder:
            root,source,identity,release=self.fixture(folder)
            launch=edit.policy_binding.prepare(root,4,identity,source,release)
            edit.policy_binding.ready(root,4,launch,'body-a')
            rpc=self.rpc(source.read_text()); learned=rpc.source+'\n-- accepted'
            edit.edit('apply',4,'body-a',rpc,learned,edit.digest(rpc.source),root)
            captured=edit.policy_binding.capture(root/'4','body-a',edit.digest(learned))
            changed=dict(release,AGENT_WOW_POLICY_CONTEXT='new cohort')
            newer=edit.policy_binding.prepare(root,4,identity,source,changed)
            self.assertEqual(newer['policyFile'],str(source))
            self.assertFalse(edit.policy_binding.promote(root/'4',captured,edit.digest(learned),'body-a'))
            source.write_text('return {decide=function() return {new=true} end}')
            self.assertEqual(edit.policy_binding.prepare(root,4,identity,source,release)['policyFile'],str(source))

    def test_missing_binding_stale_revision_and_corrupt_retained_content_fail_closed(self):
        with tempfile.TemporaryDirectory() as folder:
            root,source,identity,release=self.fixture(folder)
            launch=edit.policy_binding.prepare(root,4,identity,source,release)
            self.assertIsNone(edit.policy_binding.capture(root/'4','body-a',launch['revision']))
            edit.policy_binding.ready(root,4,launch,'body-a')
            self.assertIsNone(edit.policy_binding.capture(root/'4','body-a','0'*64))
            rpc=self.rpc(source.read_text());learned=rpc.source+'\n-- accepted'
            edit.edit('apply',4,'body-a',rpc,learned,edit.digest(rpc.source),root)
            (root/'4'/(edit.digest(learned)+'.lua')).write_text('corrupt')
            with self.assertRaisesRegex(ValueError,'source changed'):
                edit.policy_binding.prepare(root,4,identity,source,release)
