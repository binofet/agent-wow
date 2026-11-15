package gamecatalog

import "testing"

func TestLockpickingRequirementsFollowExactContainerEntry(t *testing.T) {
	for entry, want := range map[uint32]uint32{75295: 1, 74447: 25, 75296: 70, 75297: 125} {
		chest, known := Default().Container(entry)
		got, pickable := LockpickingRequirement(chest.LockID)
		if !known || chest.Type != 3 || !pickable || got != want {
			t.Errorf("chest %d lock %d: rank = %d, pickable = %t, want %d", entry, chest.LockID, got, pickable, want)
		}
	}
	if _, ok := LockpickingRequirement(0); ok {
		t.Error("no lock should not require lockpicking")
	}
}
