"""Pure task-stack decisions through the real local Go Lua bridge, no game RPC."""
import copy
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parent.parent
loader = importlib.machinery.SourceFileLoader('task_stack_host_tested', str(ROOT / 'dev/task-runtime'))
spec = importlib.util.spec_from_loader(loader.name, loader)
host = importlib.util.module_from_spec(spec)
loader.exec_module(host)
BINARY = os.environ.get('BUDDY_LUAPOLICY_TEST_BINARY')

DRIVER = '''
return {decide=function(state)
    local handlers = {}
    handlers.root = function(context, frame)
        if frame.stage == "consumed" then return tasks.await(context, "reply_retained") end
        local reply, waiting = tasks.call(context, frame, state.goal.params.method, {}, "requesting")
        if waiting then return waiting end
        frame.stage, frame.reply = "consumed", reply
        return tasks.await(context, "reply_consumed")
    end
    return tasks.run(state, handlers, "root")
end}
'''


@unittest.skipUnless(BINARY, 'set BUDDY_LUAPOLICY_TEST_BINARY to the local compiled bridge')
class TaskStackTests(unittest.TestCase):
    def setUp(self):
        self.bridge = host.Bridge(BINARY, ROOT / 'modules')
        self.source = 'local tasks=(function()\n' + (ROOT / 'behaviors/task-stack.lua').read_text() + '\nend)();\n' + DRIVER
        self.state = dict(goal=dict(id='stack-goal', params=dict(method='buddy-world.snapshot')),
                          memory={}, body=dict(sessionId='fake-body', selfGuid='87'),
                          observations={}, pending=[], now=1)

    def evaluate(self, last=None):
        if last is not None:
            self.state['last'] = copy.deepcopy(last)
        result = self.bridge.evaluate(self.source, self.state)
        self.state['memory'] = copy.deepcopy(result['memory'])
        return result

    def reserve(self):
        first = self.evaluate()
        self.assertEqual(first['action']['id'], 'stack-goal:1')
        self.assertEqual(first['action']['method'], 'buddy-world.snapshot')
        self.assertEqual(self.state['memory']['serial'], 1)
        return dict(id=first['action']['id'], method=first['action']['method'])

    def test_exact_read_error_consumed_once_and_persisted_without_new_action(self):
        reply = self.reserve()
        result = self.evaluate(dict(reply, effect='read', error='route has no verified floor'))
        self.assertEqual(result['phase'], 'reply_consumed')
        self.assertEqual(result['memory']['stack'][0]['reply'], {'readError': 'route has no verified floor'})
        self.assertNotIn('waiting', result['memory']['stack'][0])
        self.assertNotIn('action', result)
        # Recreate the policy VM with the saved frame, as after a restart.
        resumed = self.evaluate()
        self.assertEqual(resumed['phase'], 'reply_retained')
        self.assertEqual(resumed['memory']['serial'], 1)
        self.assertNotIn('action', resumed)

    def test_unknown_query_and_mutation_errors_keep_exact_waiting_reservation(self):
        for effect in (None, 'query', 'mutation', 'unspecified', 'READ'):
            with self.subTest(effect=effect):
                self.setUp()
                reply = self.reserve()
                last = dict(reply, error='transport outcome unknown')
                if effect is not None: last['effect'] = effect
                result = self.evaluate(last)
                self.assertEqual(result['phase'], 'blocked_on_ambiguous_call')
                self.assertEqual(result['memory']['stack'][0]['waiting'], reply)
                self.assertEqual(result['memory']['serial'], 1)
                self.assertNotIn('action', result)
                self.assertEqual(self.evaluate()['phase'], 'blocked_on_ambiguous_call')

    def test_reply_identity_and_method_cannot_consume_a_different_call(self):
        reply = self.reserve()
        result = self.evaluate(dict(reply, id='another:1', effect='read', error='definite read refusal'))
        self.assertEqual(result['phase'], 'waiting_for_call_result')
        self.assertEqual(result['memory']['stack'][0]['waiting'], reply)
        with self.assertRaises(host.Error):
            self.evaluate(dict(reply, method='buddy-supplies.buy', effect='read', error='foreign method'))
        self.assertEqual(self.state['memory']['stack'][0]['waiting'], reply)

    def test_success_reply_keeps_existing_contract(self):
        reply = self.reserve()
        result = self.evaluate(dict(reply, effect='query', response={'revision': '4'}))
        self.assertEqual(result['phase'], 'reply_consumed')
        self.assertEqual(result['memory']['stack'][0]['reply'], {'revision': '4'})
        self.assertNotIn('action', result)

    def test_error_detail_is_bounded_and_malformed_read_error_stays_unconsumed(self):
        for error in ('', 'x' * 257):
            with self.subTest(length=len(error)):
                self.setUp()
                reply = self.reserve()
                result = self.evaluate(dict(reply, effect='read', error=error))
                self.assertEqual(result['memory']['stack'][0]['reply'], {'readError': 'read_error_detail_unavailable'})
        self.setUp()
        reply = self.reserve()
        with self.assertRaises(host.Error):
            self.evaluate(dict(reply, effect='read', error={'not': 'a string'}))
        self.assertEqual(self.state['memory']['stack'][0]['waiting'], reply)


if __name__ == '__main__':
    unittest.main()
