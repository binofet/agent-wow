"""Temporary local claim ledgers only; no real worktree, runtime or RPC writes."""
import concurrent.futures
import contextlib
import copy
import importlib.machinery
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import threading
import unittest
from unittest.mock import patch

loader = importlib.machinery.SourceFileLoader('task_claim_tested', str(Path(__file__).with_name('task-claim')))
spec = importlib.util.spec_from_loader(loader.name, loader)
m = importlib.util.module_from_spec(spec); loader.exec_module(m)


class ClaimsTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(dir='/private/tmp'); self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name) / 'repo'; self.common = self.root / '.git'
        self.common.mkdir(parents=True)
        self.workspaces = [self.root / 'data/self-edit/workspaces' / name for name in ('arlen', 'mira')]
        rows = []
        for guid, name, workspace in zip((1, 4), ('arlen', 'mira'), self.workspaces):
            (workspace / 'dev').mkdir(parents=True); (self.common / 'worktrees' / name).mkdir(parents=True)
            rows.append(dict(guid=guid, name=name, workspace=str(workspace), branch='buddy/' + name, base='a' * 40))
        self.registry = self.root / 'data/self-edit/workspaces.json'
        self.registry.write_text(json.dumps(dict(characters=rows))); self.registry.chmod(0o600)
        def git(cwd, *args):
            workspace = next((p for p in self.workspaces if Path(cwd).is_relative_to(p)), self.root)
            if args == ('rev-parse', '--show-toplevel'): return str(workspace)
            if args == ('rev-parse', '--path-format=absolute', '--git-common-dir'): return str(self.common)
            if args == ('rev-parse', '--absolute-git-dir'): return str(self.common / 'worktrees' / workspace.name)
            if args == ('symbolic-ref', '--short', 'HEAD'): return 'buddy/' + workspace.name
            if args == ('rev-parse', 'HEAD'): return 'b' * 40
            raise AssertionError((cwd, args))
        mock = patch.object(m, 'git', side_effect=git); mock.start(); self.addCleanup(mock.stop)
        self.a = m.context(self.workspaces[0] / 'dev', 1)
        self.b = m.context(self.workspaces[1], 4)

    def claim(self, ctx=None, task='skin repair', files=None, ttl=20, now=100):
        return m.operate(ctx or self.a, 'claim', task=task, files=files or ['dev/repair'], ttl=ttl, now=now)

    def modify(self, result, command, ctx=None, now=101, **kwargs):
        return m.operate(ctx or self.a, command, claim_id=result['claim']['id'], token=result['token'], now=now, **kwargs)

    def inspect(self, now=101):
        return m.operate(m.context(self.root, actor_required=False), 'inspect', now=now)['claims']

    def test_registry_and_actual_git_worktree_bind_both_actors_to_shared_root(self):
        self.assertEqual(self.a['root'], self.b['root'])
        self.assertEqual(self.a['actor']['guid'], 1); self.assertEqual(self.b['actor']['guid'], 4)
        self.assertEqual(self.a['actor']['workspace'], str(self.workspaces[0]))
        with self.assertRaisesRegex(m.Error, 'registered_actor_worktree'): m.context(self.workspaces[0], 4)
        with self.assertRaisesRegex(m.Error, 'registered_actor_worktree'): m.context(self.root, 1)
        changed = json.loads(self.registry.read_text()); changed['characters'][0]['branch'] = 'wrong-branch'
        self.registry.write_text(json.dumps(changed))
        with self.assertRaisesRegex(m.Error, 'registered_branch'): m.context(self.workspaces[0], 1)

    def test_semantic_task_and_overlapping_file_paths_conflict_across_worktrees(self):
        result = self.claim(task='Fix Skinning', files=['modules/skin.go'])
        for task, files in [('fix_skinning', ['dev/other']), ('fix skinning ', ['dev/other']), ('different-planner', ['modules/Skin.go']),
                            ('another', ['modules']), ('another', ['modules/skin.go/child'])]:
            with self.subTest(task=task, files=files), self.assertRaises(m.Error) as error:
                self.claim(self.b, task, files)
            self.assertEqual(error.exception.conflicts[0]['id'], result['claim']['id'])
            self.assertNotIn('tokenHash', error.exception.conflicts[0])
        other = self.claim(self.b, 'unrelated', ['dev/unrelated'])
        self.assertNotEqual(result['claim']['id'], other['claim']['id'])
        self.assertEqual(len(self.inspect()), 2)

    def test_concurrent_claims_hold_real_file_lock_and_cannot_overwrite_peer(self):
        entered, proceed = threading.Event(), threading.Event(); original = m.save
        def slow_save(folder, data):
            entered.set(); self.assertTrue(proceed.wait(2)); original(folder, data)
        with patch.object(m, 'save', side_effect=slow_save), concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            winner = pool.submit(self.claim)
            self.assertTrue(entered.wait(2))
            try:
                loser = pool.submit(self.claim, self.b, 'different-planner', ['dev/repair'])
                with self.assertRaisesRegex(m.Error, 'claim_store_busy'): loser.result(timeout=2)
            finally: proceed.set()
            result = winner.result(timeout=2)
        with self.assertRaisesRegex(m.Error, 'active_claim_conflict'):
            self.claim(self.b, 'different-planner', ['dev/repair'])
        self.assertEqual([r['id'] for r in self.inspect()], [result['claim']['id']])

    def test_expired_claim_stays_visible_and_cannot_renew_over_new_owner(self):
        old = self.claim(ttl=10)
        self.assertEqual(self.inspect(now=110)[0]['state'], 'expired')
        new = self.claim(self.b, ttl=20, now=110)
        with self.assertRaisesRegex(m.Error, 'expired_claim_cannot_renew'): self.modify(old, 'renew', now=111, ttl=20)
        self.modify(old, 'release', now=111)
        rows = self.inspect(now=112)
        self.assertEqual([r['state'] for r in rows], ['released', 'active'])
        self.assertEqual(rows[1]['id'], new['claim']['id'])
        self.assertEqual(rows[0]['expiresAt'], 110)

    def test_token_and_worktree_identity_guard_renewal_and_release(self):
        result = self.claim(); path = self.root / 'data/task-claims/claims.json'; before = path.read_bytes()
        for command in ('renew', 'release'):
            options = dict(ttl=30) if command == 'renew' else {}
            with self.assertRaisesRegex(m.Error, 'token_or_owner'):
                m.operate(self.a, command, claim_id=result['claim']['id'], token='0' * 64, now=101, **options)
            with self.assertRaisesRegex(m.Error, 'token_or_owner'):
                self.modify(result, command, self.b, **options)
            moved = copy.deepcopy(self.a); moved['actor']['gitInode'] += 1
            with self.assertRaisesRegex(m.Error, 'token_or_owner'): self.modify(result, command, moved, **options)
            self.assertEqual(path.read_bytes(), before)
        self.a['head'] = 'c' * 40
        renewed = self.modify(result, 'renew', now=105, ttl=30)
        self.assertEqual(renewed['claim']['expiresAt'], 135)
        self.assertEqual(renewed['claim']['head'], 'c' * 40)
        self.modify(result, 'release', now=106)
        with self.assertRaisesRegex(m.Error, 'already_released'): self.modify(result, 'renew', now=107, ttl=30)
        self.assertNotIn(result['token'], json.dumps(self.inspect(now=107)))

    def test_resume_own_rotates_token_preserving_exact_claim_id_history_and_creation(self):
        old=self.claim(files=['dev/b','dev/a']);peer=self.claim(self.b,'peer',['dev/peer'])
        self.a['head']='c'*40
        fresh=m.operate(self.a,'resume-own',task='Skin_Repair',files=['dev/a','dev/b'],ttl=60,now=105)
        self.assertEqual(fresh['claim']['id'],old['claim']['id']);self.assertEqual(fresh['claim']['createdAt'],100)
        self.assertEqual(fresh['claim']['expiresAt'],165);self.assertEqual(fresh['claim']['head'],'c'*40)
        self.assertNotEqual(fresh['token'],old['token'])
        for command in ('renew','release'):
            with self.assertRaisesRegex(m.Error,'token_or_owner'):
                self.modify(old,command,now=106,**({'ttl':20} if command=='renew' else {}))
        rows=self.inspect(now=106);self.assertEqual(len(rows),2);self.assertEqual(rows[1]['id'],peer['claim']['id'])
        self.modify(fresh,'release',now=107);self.assertEqual(self.inspect(now=108)[0]['state'],'released')

    def test_resume_own_rejects_peer_changed_scope_identity_expired_and_mixed_claims_without_write(self):
        old=self.claim(files=['dev/a','dev/b']);path=self.root/'data/task-claims/claims.json';original=path.read_bytes()
        moved=copy.deepcopy(self.a);moved['actor']['worktreeInode']+=1
        cases=[(self.b,'skin repair',['dev/a','dev/b'],101),(self.a,'different',['dev/a','dev/b'],101),
               (self.a,'skin repair',['dev/a'],101),(self.a,'skin repair',['dev/a','dev/b','dev/c'],101),
               (moved,'skin repair',['dev/a','dev/b'],101),(self.a,'skin repair',['dev/a','dev/b'],120)]
        for ctx,task,files,now in cases:
            with self.subTest(ctx=ctx['actor']['guid'],files=files,now=now),self.assertRaisesRegex(m.Error,'resume_own_exact'):
                m.operate(ctx,'resume-own',task=task,files=files,ttl=20,now=now)
            self.assertEqual(path.read_bytes(),original)
        data=json.loads(original);mixed=copy.deepcopy(data['claims'][0]);mixed.update(id='f'*32,actor=self.b['actor'])
        data['claims'].append(mixed);m.save(path.parent,data);before=path.read_bytes()
        with self.assertRaisesRegex(m.Error,'resume_own_exact'):
            m.operate(self.a,'resume-own',task='skin repair',files=['dev/a','dev/b'],ttl=20,now=101)
        self.assertEqual(path.read_bytes(),before)

    def test_bounded_inputs_reject_aliases_metadata_paths_and_bad_expiry(self):
        for files in (['../escape'], ['/absolute'], ['./dev/repair'], ['.git/config'], ['.GIT/config'],
                      ['dev/a', 'DEV/A'], ['dev/\u202ehidden'], ['x'] * 65):
            with self.subTest(files=files), self.assertRaises(m.Error): self.claim(files=files)
        for ttl in (0, -1, 28801, True, float('nan')):
            with self.subTest(ttl=ttl), self.assertRaises(m.Error): self.claim(ttl=ttl)
        for task in ('', 'x' * 97, 'bad\nkey'):
            with self.subTest(task=task), self.assertRaises(m.Error): self.claim(task=task)
        self.assertFalse((self.root / 'data/task-claims').exists())

    def test_symlinks_and_unowned_or_public_state_are_refused(self):
        outside = self.root.parent / 'outside'; outside.write_text('untouched'); outside.chmod(0o600)
        (self.workspaces[0] / 'dev/linked').symlink_to(outside)
        with self.assertRaisesRegex(m.Error, 'symlink'): self.claim(files=['dev/linked'])
        result = self.claim(); folder = self.root / 'data/task-claims'
        self.assertEqual(folder.stat().st_mode & 0o777, 0o700)
        for name in ('claims.json', 'lock'):
            target = folder / name; saved = target.read_bytes(); target.unlink(); target.symlink_to(outside)
            with self.assertRaises((m.Error, OSError)): self.inspect()
            target.unlink(); target.write_bytes(saved); target.chmod(0o600)
        folder.chmod(0o755)
        with self.assertRaisesRegex(m.Error, 'private_claim_directory'): self.inspect()
        folder.chmod(0o700); (folder / 'claims.json').chmod(0o644)
        with self.assertRaisesRegex(m.Error, 'private_owned_regular'): self.inspect()
        self.assertEqual(outside.read_text(), 'untouched')
        self.registry.chmod(0o644)
        with self.assertRaisesRegex(m.Error, 'private_owned_regular'): m.context(self.workspaces[0], 1)

    def test_atomic_write_failure_preserves_original_claim_and_cleans_temporary(self):
        first = self.claim(); folder = self.root / 'data/task-claims'; before = (folder / 'claims.json').read_bytes()
        with patch.object(m.os, 'replace', side_effect=OSError('fake failure')):
            with self.assertRaises(OSError): self.claim(self.b, 'other', ['dev/other'])
        self.assertEqual((folder / 'claims.json').read_bytes(), before)
        self.assertEqual(sorted(p.name for p in folder.iterdir()), ['claims.json', 'lock'])
        self.assertEqual(self.inspect()[0]['id'], first['claim']['id'])

    def test_history_capacity_clock_rollback_and_corrupt_json_fail_closed(self):
        self.claim(ttl=1)
        with patch.object(m, 'MAX_CLAIMS', 1):
            with self.assertRaisesRegex(m.Error, 'history_full'): self.claim(self.b, now=102)
        with self.assertRaisesRegex(m.Error, 'clock_precedes'): self.claim(self.b, 'other', ['dev/other'], now=99)
        path = self.root / 'data/task-claims/claims.json'
        path.write_text('{"version":1,"version":1,"claims":[]}')
        with self.assertRaisesRegex(m.Error, 'duplicate_json_key'): self.inspect()

    def test_cli_claim_renew_inspect_release_uses_only_actual_registry_identity(self):
        def run(args):
            with patch.object(m.Path, 'cwd', return_value=self.workspaces[0]), contextlib.redirect_stdout(io.StringIO()) as out:
                code = m.main(args)
            return code, json.loads(out.getvalue())
        code, result = run(['claim', '--guid', '1', '--task', 'repair', '--file', 'dev/repair', '--ttl-seconds', '30'])
        self.assertEqual(code, 0); token, claim = result['token'], result['claim']['id']
        code,resumed=run(['resume-own','--guid','1','--task','repair','--file','dev/repair','--ttl-seconds','60'])
        self.assertEqual(code,0);self.assertEqual(resumed['claim']['id'],claim)
        self.assertNotEqual(resumed['token'],token);token=resumed['token']
        for command in ('renew', 'release'):
            args = [command, '--guid', '1', '--claim-id', claim, '--token', token]
            if command == 'renew': args += ['--ttl-seconds', '60']
            self.assertEqual(run(args)[0], 0)
        code, state = run(['inspect']); self.assertEqual(code, 0)
        self.assertEqual(state['claims'][0]['state'], 'released')
        self.assertEqual(run(['claim', '--guid', '4', '--task', 'other', '--file', 'dev/other', '--ttl-seconds', '30'])[0], 1)


if __name__ == '__main__': unittest.main()
