"""Companion repair queue and immutable review gates, without model calls."""
import importlib.machinery
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import time
import types
import unittest
from unittest.mock import patch

loader=importlib.machinery.SourceFileLoader('tested_workshop',str(Path(__file__).with_name('workshop')))
spec=importlib.util.spec_from_loader(loader.name,loader)
w=importlib.util.module_from_spec(spec);loader.exec_module(w)


class WorkshopTests(unittest.TestCase):
    def test_report_can_be_promoted_once_without_replaying_consumed_job(self):
        with tempfile.TemporaryDirectory() as directory,patch.object(w,'DATA',Path(directory)):
            first=w.submit(4,'Area sweep refused',{'capturedAt':1})
            second=w.submit(4,'Area sweep refused',{'capturedAt':2},repair=True)
            self.assertEqual(first['id'],second['id'])
            self.assertEqual(second['phase'],'queued')
            second['phase']='coding';w.save(Path(directory)/'reports'/(second['id']+'.json'),second)
            self.assertEqual(w.submit(4,'Area sweep refused',repair=True)['phase'],'coding')
            self.assertEqual(len(list((Path(directory)/'reports').glob('*.json'))),1)

    def test_reports_reject_unknown_identity_and_bound_payload(self):
        for guid,problem,actual in ((999,'x',{}),(1,'x'*2001,{}),(1,'x',{'x':'z'*16001})):
            with self.assertRaises(w.Error):w.submit(guid,problem,actual)

    def test_unavailable_or_wrong_body_caches_are_not_current_evidence(self):
        controls=types.SimpleNamespace(character_guid=4,party_available=True,party_session='new',
            actions_available=True,actions={'selfGuid':'4','sessionId':'new','reason':'idle'},
            quests_available=False,quests={'sessionId':'new','reason':'stale'},
            loot_available=True,loot={'sessionId':'old','reason':'stale'},
            quest_items_available=True,quest_items={'sessionId':'new','selfGuid':'5'},
            professions={'available':False},rpc=types.SimpleNamespace(last_failure={'at':time.time()-20,'method':'unrelated'}))
        evidence=w.observation(controls)
        self.assertEqual(evidence['bodySessionId'],'new')
        self.assertTrue(evidence['modules']['actions']['available'])
        for name in ('quests','loot','quest_items','professions'):
            self.assertEqual(evidence['modules'][name],{'available':False})
        self.assertNotIn('rpcFailure',evidence)
        controls.party_available=False
        self.assertFalse(w.observation(controls)['modules']['actions']['available'])

    def git(self,folder,*args):
        return subprocess.check_output(['git',*args],cwd=folder,text=True,stderr=subprocess.DEVNULL).strip()

    def repository(self,folder):
        self.git(folder,'init','-q')
        self.git(folder,'config','user.name','Workshop test')
        self.git(folder,'config','user.email','test@example.invalid')
        (folder/'controller.py').write_text('old\n')
        self.git(folder,'add','controller.py');self.git(folder,'commit','-qm','base')
        return self.git(folder,'rev-parse','HEAD')

    def test_staged_protected_file_cannot_bypass_scope(self):
        with tempfile.TemporaryDirectory() as directory:
            folder=Path(directory);base=self.repository(folder)
            (folder/'README.md').write_text('forbidden\n');self.git(folder,'add','README.md')
            (folder/'controller.py').write_text('fixed\n')
            with self.assertRaises(w.Error):w.candidate(folder,base)

    def test_frozen_candidate_rejects_edit_staging_and_agent_commit(self):
        for mutation in ('unstaged','staged','commit'):
            with self.subTest(mutation=mutation),tempfile.TemporaryDirectory() as directory:
                folder=Path(directory);base=self.repository(folder)
                (folder/'controller.py').write_text('fixed\n')
                names,tree=w.candidate(folder,base)
                self.assertEqual(names,['controller.py']);w.unchanged(folder,base,tree)
                (folder/'controller.py').write_text('different\n')
                if mutation in ('staged','commit'):self.git(folder,'add','controller.py')
                if mutation=='commit':self.git(folder,'commit','-qm','unreviewed')
                with self.assertRaises(w.Error):w.unchanged(folder,base,tree)
                if mutation=='commit':
                    with self.assertRaises(w.Error):w.candidate(folder,base)

    def test_failed_test_never_reviews_publishes_or_restarts(self):
        with tempfile.TemporaryDirectory() as directory,patch.object(w,'DATA',Path(directory)/'private'):
            folder=Path(directory)/'repo';folder.mkdir();base=self.repository(folder)
            report=w.submit(1,'Broken control',repair=True)
            real_run=w.run
            def git_run(command,cwd=w.ROOT,**kwargs):
                return real_run(command,cwd=folder if cwd==w.ROOT else cwd,**kwargs)
            def edit(prompt,worktree,job,**kwargs):
                (worktree/'controller.py').write_text('fixed\n')
                return {'ready':True,'summary':'Fix the control','tests':[]}
            real_subprocess_run=subprocess.run
            def command_run(command,**kwargs):
                if command[0]=='git':return real_subprocess_run(command,**kwargs)
                return subprocess.CompletedProcess(command,1,'intentional failure','')
            with patch.object(w,'run',side_effect=git_run),patch.object(w,'generate',side_effect=edit) as generate,patch.object(w.subprocess,'run',side_effect=command_run):
                result=w.process(report,publish=True)
            self.assertEqual(result['phase'],'needs_attention')
            self.assertIn('go-tests failed',result['failure'])
            self.assertEqual(generate.call_count,1)
            self.assertNotIn('head',result)

    def test_reviewer_is_read_only_and_neither_process_uses_background_agents(self):
        command=w.coding_command(Path('/tmp/worktree'),Path('/tmp/schema'),Path('/tmp/answer'),review=True)
        self.assertEqual(command[command.index('--sandbox')+1],'read-only')
        self.assertNotIn('--approve-for-me',command)
        self.assertIn('multi_agent',command)
        edit=w.coding_command(Path('/tmp/worktree'),Path('/tmp/schema'),Path('/tmp/answer'))
        self.assertIn('--approve-for-me',edit)
        self.assertNotIn('--sandbox',edit)
        self.assertNotIn('--dangerously-bypass-approvals-and-sandbox',edit)

    def test_failed_or_incomplete_ci_cannot_integrate_or_apply(self):
        for conclusion,status in (('failure','completed'),(None,'in_progress')):
            calls=[]
            def command(command,**kwargs):
                calls.append(command)
                if command[:3]==['git','diff','--name-only']:return 'behaviors/party.lua'
                if command[:3]==['git','rev-parse','HEAD']:return 'base'
                if command[:2]==['git','diff']:return ''
                if command[:2]==['gh','api']:return json.dumps({'object':{'sha':'base'}})
                if command[:3]==['gh','run','list']:return json.dumps([dict(event='push',headSha='head',status=status,conclusion=conclusion,databaseId=123)])
                raise AssertionError(command)
            report=dict(base='base',head='head',files=['behaviors/party.lua'])
            with patch.object(w,'run',side_effect=command):
                if conclusion=='failure':
                    with self.assertRaisesRegex(w.Error,'CI failed'):w.integrate_lua(report)
                else:self.assertFalse(w.integrate_lua(report))
            self.assertFalse(any(c[:2] in (['git','merge'],['git','push']) for c in calls))

    def test_native_code_cannot_take_policy_only_deployment_path(self):
        report=dict(base='base',head='head',files=['modules/buddy-actions/service.go'])
        with patch.object(w,'run',return_value='modules/buddy-actions/service.go'):
            with self.assertRaisesRegex(w.Error,'native client release'):w.integrate_lua(report)

    def test_new_pull_request_head_invalidates_old_review_even_with_green_ci(self):
        calls=[]
        def command(command,**kwargs):
            calls.append(command)
            if command[:3]==['git','diff','--name-only']:return 'behaviors/party.lua'
            if command[:3]==['git','rev-parse','HEAD']:return 'base'
            if command[:2]==['git','diff']:return ''
            if command[:2]==['gh','api']:return json.dumps({'object':{'sha':'base'}})
            if command[:3]==['gh','run','list']:return json.dumps([dict(event='push',headSha='head',status='completed',conclusion='success',databaseId=1)])
            if command[:3]==['gh','run','view']:return json.dumps(dict(headSha='head',jobs=[dict(name='Check and build',conclusion='success',steps=[dict(name=n,conclusion='success') for n in ('Vet','Test','Build')])]))
            if command[:3]==['gh','pr','view']:return json.dumps(dict(headRefOid='unreviewed',headRefName='branch',baseRefName=w.TARGET,state='OPEN',isDraft=False))
            raise AssertionError(command)
        with patch.object(w,'run',side_effect=command):
            with self.assertRaisesRegex(w.Error,'pull request changed'):w.integrate_lua(dict(base='base',head='head',branch='branch',pr='pr',files=['behaviors/party.lua']))
        self.assertFalse(any(c[:2] in (['git','merge'],['git','push']) for c in calls))


if __name__=='__main__':unittest.main()
