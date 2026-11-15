package buddyworld

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func creatureCastFixture(op uint32, source, caster, target uint64, count byte, spell, duration uint32) []byte {
	var p []byte
	if op == opcode.SMSGSpellStart || op == opcode.SMSGSpellGo {
		p = packedFixture(source)
	}
	p = append(p, packedFixture(caster)...)
	p = binary.LittleEndian.AppendUint32(append(p, count), spell)
	if op == opcode.SMSGSpellFailure || op == opcode.SMSGSpellFailedOther {
		return append(p, 0)
	}
	p = binary.LittleEndian.AppendUint32(p, 0)
	p = binary.LittleEndian.AppendUint32(p, duration)
	if op == opcode.SMSGSpellGo {
		p = append(p, 0, 0) // no hit or miss GUIDs
	}
	if target == 0 {
		return binary.LittleEndian.AppendUint32(p, 0)
	}
	return append(binary.LittleEndian.AppendUint32(p, 2), packedFixture(target)...)
}

func TestCreatureCastSnapshotTracksSourceWindowAndMatchingTerminal(t *testing.T) {
	s := NewService(1)
	now := time.Unix(100, 0)
	s.now = func() time.Time { return now }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(npcGUID, 3, npcFields(0)))
	start := creatureCastFixture(opcode.SMSGSpellStart, 0x400000000000002a, npcGUID, npcGUID+1, 0, 2054, 3000)
	packet(t, s, opcode.SMSGSpellStart, start)
	cast := combatView(t, s, npcGUID).GetActiveCast()
	if cast == nil || cast.SpellId != 2054 || cast.TargetGuid != npcGUID+1 || cast.RemainingMs != 3000 || cast.Revision == 0 {
		t.Fatal("did not identify the actual creature caster and its window", cast)
	}
	now = now.Add(700 * time.Millisecond)
	packet(t, s, opcode.SMSGSpellGo, creatureCastFixture(opcode.SMSGSpellGo, npcGUID, npcGUID, 0, 1, 2054, 0))
	packet(t, s, opcode.SMSGSpellFailure, creatureCastFixture(opcode.SMSGSpellFailure, 0, npcGUID, 0, 0, 2055, 0))
	if view := combatView(t, s, npcGUID).GetActiveCast(); view.GetRemainingMs() != 2300 || view.GetRevision() != cast.Revision {
		t.Fatal("unrelated spell ended or changed the active cast", view)
	}
	terminal := creatureCastFixture(opcode.SMSGSpellGo, npcGUID, npcGUID, 0, 0, 2054, 0)
	if r := packet(t, s, opcode.SMSGSpellGo, terminal[:len(terminal)-1]); r.MalformedPackets != 1 || combatView(t, s, npcGUID).GetActiveCast() == nil {
		t.Fatal("truncated terminal invalidated the cast", r)
	}
	packet(t, s, opcode.SMSGSpellGo, terminal)
	if combatView(t, s, npcGUID).GetActiveCast() != nil {
		t.Fatal("matching go did not end the cast")
	}
	packet(t, s, opcode.SMSGSpellStart, start)
	if view := combatView(t, s, npcGUID).GetActiveCast(); view.GetRevision() <= cast.Revision {
		t.Fatal("NPC cast count zero did not receive a fresh revision", view)
	}
	packet(t, s, opcode.SMSGSpellFailedOther, creatureCastFixture(opcode.SMSGSpellFailedOther, 0, npcGUID, 0, 0, 2054, 0))
	if combatView(t, s, npcGUID).GetActiveCast() != nil {
		t.Fatal("matching interruption did not end the cast")
	}
}

func TestCreatureCastExpiresAndCannotSurviveDeathOrWorldChange(t *testing.T) {
	s := NewService(1)
	now := time.Unix(100, 0)
	s.now = func() time.Time { return now }
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	start := creatureCastFixture(opcode.SMSGSpellStart, npcGUID, npcGUID, 0, 0, 2054, ^uint32(0))
	if r := packet(t, s, opcode.SMSGSpellStart, start); len(r.NearbyCreatures) != 0 {
		t.Fatal("cast-only packet invented a creature")
	}
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(npcGUID, 3, npcFields(0)))
	packet(t, s, opcode.SMSGSpellStart, start)
	if view := combatView(t, s, npcGUID).GetActiveCast(); view.GetRemainingMs() != maxCreatureCastMs {
		t.Fatal("server duration exceeded the verified protocol ceiling", view)
	}
	now = now.Add(maxCreatureCastMs * time.Millisecond)
	if combatView(t, s, npcGUID).GetActiveCast() != nil {
		t.Fatal("expired cast still visible")
	}
	packet(t, s, opcode.SMSGSpellStart, start)
	packet(t, s, opcode.SMSGUpdateObject, deltaFixture(npcGUID, map[int]uint32{unitHealth: 0}))
	packet(t, s, opcode.SMSGUpdateObject, deltaFixture(npcGUID, map[int]uint32{unitHealth: 1}))
	if combatView(t, s, npcGUID).GetActiveCast() != nil {
		t.Fatal("cast came back after creature death")
	}
	packet(t, s, opcode.SMSGSpellStart, start)
	packet(t, s, opcode.SMSGTransferPending, make([]byte, 4))
	packet(t, s, opcode.SMSGNewWorld, login())
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(npcGUID, 3, npcFields(0)))
	if combatView(t, s, npcGUID).GetActiveCast() != nil {
		t.Fatal("cast came back after entering a new world")
	}
}
