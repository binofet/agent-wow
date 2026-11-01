"""Public policy updates bind the body and exact expected revision."""
import hashlib
import importlib.machinery
import importlib.util
import json
from pathlib import Path
import unittest

loader=importlib.machinery.SourceFileLoader('tested_behavior',str(Path(__file__).with_name('behavior')))
spec=importlib.util.spec_from_loader(loader.name,loader);b=importlib.util.module_from_spec(spec);loader.exec_module(b)

class RPC:
    def __init__(self):self.calls=[];self.sid='body-one';self.revision='';self.fail=False
    def call(self,method,params):
        self.calls.append((method,params))
        if method.endswith('reload_behavior'):
            if self.fail:raise b.c.ConversationError('Ambiguous write')
            self.revision=hashlib.sha256(params['source'].encode()).hexdigest() if params['source'] else ''
        return dict(selfGuid='1',sessionId=self.sid,revision=self.revision)

class BehaviorTests(unittest.TestCase):
    def test_exact_revision_and_body_with_no_reconnect(self):
        rpc=RPC();result=b.apply(1,'return {}',rpc)
        self.assertFalse(result['reconnected']);self.assertEqual(result['sessionId'],'body-one')
        write=rpc.calls[2][1];self.assertEqual(write['expectedRevision'],'');self.assertEqual(write['selfGuid'],'1')
        b.apply(1,'return {}',rpc)
        self.assertEqual(sum(m.endswith('reload_behavior') for m,p in rpc.calls),1)
        b.apply(1,None,rpc);self.assertEqual(rpc.revision,'')

    def test_timeout_is_not_retried(self):
        rpc=RPC();rpc.fail=True
        with self.assertRaises(b.c.ConversationError):b.apply(1,'return {}',rpc)
        self.assertEqual(sum(m.endswith('reload_behavior') for m,p in rpc.calls),1)

    def test_immediate_quarantine_cannot_be_reported_as_current(self):
        rpc=RPC();original=rpc.call;loaded=[False]
        def call(method,params):
            if method.endswith('behavior') and not method.endswith('reload_behavior') and loaded[0]:rpc.revision='fallback'
            result=original(method,params)
            if method.endswith('reload_behavior'):loaded[0]=True
            return result
        rpc.call=call
        with self.assertRaisesRegex(b.c.ConversationError,'not current'):b.apply(1,'return {}',rpc)

    def test_generated_bindings_match_committed_metagen_provenance(self):
        root=Path(__file__).resolve().parent.parent
        value=json.loads((root/'dev/behavior-schema/provenance.json').read_text())
        for files in (value['inputs'],value['outputs']):
            for name,expected in files.items():self.assertEqual(hashlib.sha256((root/name).read_bytes()).hexdigest(),expected,name)

if __name__=='__main__':unittest.main()
