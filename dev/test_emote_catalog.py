"""Installed emote metadata controls scripts and conversation without action switches."""
import json
from pathlib import Path
import unittest
from dev.test_converse import converse as c, FakeRPC, FakeBrain, poll, message


class EmoteCatalogTests(unittest.TestCase):
    def catalog(self):
        rows = json.loads((Path(__file__).resolve().parent.parent / 'pkg/emotecatalog/emotes.json').read_text())['emotes']
        return dict(available=True, sessionId='chat-body', selfGuid='1', revision='a'*64, emotes=rows)

    def test_all_catalog_ids_including_dance_and_laugh_are_available(self):
        names = c.supported_emotes({'emote_catalog': self.catalog()})
        self.assertEqual(names['DANCE'],34)
        self.assertEqual(names['LAUGH'],60)
        self.assertEqual(len(names),252)
        c.validate_reply(dict(text='Dancing!',emote='DANCE'),names)
        with self.assertRaises(c.ConversationError):
            c.validate_reply(dict(text='Dancing!',emote='DANCE'))

    def test_duplicate_and_unknown_catalog_entries_fail_closed(self):
        for rows in [[{'name':'DANCE','id':34}]*2,[{'name':'DANCE','id':34},{'name':'LAUGH','id':34}],
                     [{'name':'NONE','id':34}],[{'name':'DANCE','id':True}]]:
            with self.subTest(rows=rows), self.assertRaises(c.ConversationError):
                c.supported_emotes({'emote_catalog':dict(available=True,emotes=rows)})

    def test_same_emote_operation_sends_catalog_numeric_id(self):
        rpc=FakeRPC([])
        loop=c.Conversation(rpc,FakeBrain())
        loop.reply_emotes=c.supported_emotes({'emote_catalog':self.catalog()})
        loop.send_reply(dict(text='Dancing!',emote='DANCE'), 'PARTY')
        self.assertEqual(rpc.calls[-1],('buddy-chat.emote',{'emote':34,'targetGuid':'3'}))

    def test_stale_chat_epoch_does_not_advertise_new_emotes(self):
        catalog=self.catalog()
        class Controls:
            events=[]
            def refresh(self): pass
            def context(self): return {'emote_catalog':catalog}
        rpc=FakeRPC([poll(sessionId='different-body'),poll([message(1,'Dance!')],sessionId='different-body')])
        loop=c.Conversation(rpc,FakeBrain({'text':'Dancing!','emote':'DANCE'}),controls=Controls())
        loop.tick()
        with self.assertRaises(c.ConversationError):loop.tick()
        self.assertFalse(any(method=='buddy-chat.emote' for method,params in rpc.calls))


if __name__ == '__main__':unittest.main()
