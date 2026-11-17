package buddyactions

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestSelfManaObservationPreservesKnownZeroAndReportsAttackAndCooldown(t *testing.T) {
	const evocation = 12051
	ctx := context.Background()
	s, f, advance := rig(t)
	s.sessionID = f.party.SessionId
	s.UseHostSelfMana()
	f.world.Self.ClassId, f.world.Self.Power, f.world.Self.ChannelSpellKnown = 8, 0, true
	initial := binary.LittleEndian.AppendUint32([]byte{0, 1, 0}, evocation)
	initial = append(initial, 0, 0, 1, 0)
	initial = binary.LittleEndian.AppendUint32(initial, evocation)
	initial = binary.LittleEndian.AppendUint32(initial, 0)
	initial = binary.LittleEndian.AppendUint32(initial, 2000)
	initial = binary.LittleEndian.AppendUint32(initial, 0)
	_, _ = s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGInitialSpells, Payload: initial})
	before, err := s.SelfManaObservation(ctx, &emptypb.Empty{})
	if err != nil || !before.GetHostOwned() || !before.GetSpellbookKnown() || !before.GetCaster().GetChannelKnown() || !before.GetActivity().GetIdle() || !before.GetThreat().GetKnown() || len(before.GetSpells()) != 1 {
		t.Fatal(before, err)
	}
	spell := before.GetSpells()[0]
	if !spell.GetMetadataKnown() || !spell.GetSelfCastSupported() || !spell.GetChanneled() || !spell.GetReadyKnown() || spell.GetReady() {
		t.Fatal("login cooldown must block the generic learned rank", spell)
	}
	wire, err := protojson.Marshal(before)
	var jsonFrame struct {
		Caster, Threat map[string]any
	}
	if err != nil || json.Unmarshal(wire, &jsonFrame) != nil || jsonFrame.Caster["powerType"] != float64(0) ||
		jsonFrame.Caster["power"] != float64(0) || jsonFrame.Caster["channelSpellId"] != float64(0) || jsonFrame.Caster["inCombat"] != false ||
		jsonFrame.Caster["moving"] != false || jsonFrame.Threat["attackingSelf"] != false || jsonFrame.Threat["attackingParty"] != false {
		t.Fatalf("Lua frame lost a known zero or false: %s (%v)", wire, err)
	}
	advance(3 * time.Second)
	f.world.NearbyCreatures = []*worldv1.CreatureState{{Present: true, AliveKnown: true, Alive: true, MapKnown: true,
		MapId: f.world.Self.MapId, AttackVictimKnown: true, AttackVictimGuid: f.world.Owner.Guid}}
	after, err := s.SelfManaObservation(ctx, &emptypb.Empty{})
	if err != nil || !after.GetSpells()[0].GetReady() || !after.GetThreat().GetAttackingParty() || after.GetThreat().GetAttackingSelf() || len(f.packets) != 0 {
		t.Fatal("fact read failed to see the current cooldown or attack", after, err, f.packets)
	}
	f.party.SessionId = "wrong-session"
	f.world.Self.ChannelSpellKnown = false
	unknown, err := s.SelfManaObservation(ctx, &emptypb.Empty{})
	if err != nil || unknown.GetActivity().GetKnown() || unknown.GetThreat().GetKnown() || unknown.GetCaster().ChannelSpellId != nil {
		t.Fatal("missing channel and wrong roster appeared known", unknown, err)
	}
}
