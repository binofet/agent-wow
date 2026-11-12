"""One fake-only capability/Unstuck integration check; no SSH or console runs."""
import importlib.machinery
import importlib.util
from pathlib import Path
import tempfile
import types
import unittest
from unittest.mock import patch

loader=importlib.machinery.SourceFileLoader('server_admin_tested',str(Path(__file__).with_name('server-admin')))
spec=importlib.util.spec_from_loader(loader.name,loader);admin=importlib.util.module_from_spec(spec);loader.exec_module(admin)


class ServerAdminTests(unittest.TestCase):
    def test_general_access_and_authenticated_unstuck_use_existing_transport_without_fake_success(self):
        self.assertEqual(admin.TRANSPORT,admin.ROOT/'dev/realm-console')
        self.assertTrue(admin.TRANSPORT.is_file())
        self.assertNotIn('/private/tmp',admin.TRANSPORT.read_text())
        calls=[]
        def run(argv,**kwargs):calls.append(argv);return types.SimpleNamespace(returncode=0,stdout='console response',stderr='')
        with tempfile.TemporaryDirectory() as folder:
            transport=Path(folder)/'existing.py';transport.write_text('# public console transport fixture')
            with patch.object(admin,'TRANSPORT',transport),patch.object(admin.subprocess,'run',run):
                self.assertTrue(admin.console(['help','lookup quest Elmore'])['transportCompleted'])
                self.assertEqual(calls[-1][2:],['help','lookup quest Elmore'])
                self.assertTrue(admin.ssh(['uname -a; pwd'])['transportCompleted'])
                self.assertEqual(calls[-1][-1],'cd /home/jmax/dev/warcraft_buddy && uname -a; pwd')
                admin.rescue(3);self.assertEqual(calls[-1][-1],'tele name Joel Goldshire')
                admin.rescue(8);self.assertEqual(calls[-1][-1],'tele name Nixwick Goldshire')
                self.assertFalse(admin.capabilities()['connectionVerified'])
        from dev.test_team_control import TeamTests
        host,rpc,_,_,row,_=TeamTests().rig();row['action']='UNSTUCK';rpc.requests[0]['action']='UNSTUCK';rescued=[]
        rpc.scene['world']['self']['positionRevision']='1'
        def rescue(guid):
            rescued.append(guid);rpc.scene['world']['self'].update(x=-9465.,y=65.,z=56.,positionRevision='2')
            return dict(transportCompleted=True)
        host.rescue=rescue;host.execute(row)
        self.assertEqual(rescued,[1]);self.assertEqual(row['phase'],'COMPLETE')
        self.assertEqual([w['kind'] for w in row['writes']],['public_server_rescue'])
        self.assertFalse(any(method=='buddy-actions.travel_to' for method,_ in rpc.calls))
        host.execute(row);self.assertEqual(rescued,[1])
        self.assertEqual(row['phase'],'OUTCOME_UNCONFIRMED')
