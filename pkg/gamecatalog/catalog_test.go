package gamecatalog

import "testing"

func TestGeneratedKnowledgeAndImmutableLookup(t *testing.T) {
	c := Default()
	if len(c.objects) < 1000 {
		t.Fatal("catalog unexpectedly sparse")
	}
	r, ok := c.Container(161557)
	if !ok || r.OpenSpell != 6478 || len(r.QuestItems) != 1 || r.QuestItems[0].QuestID != 3904 {
		t.Fatal(r)
	}
	r.QuestItems[0].ItemID = 0
	r, _ = c.Container(161557)
	if r.QuestItems[0].ItemID != 11119 {
		t.Fatal("mutable shared knowledge")
	}
	if _, ok = c.Container(0); ok {
		t.Fatal("invented record")
	}
}
