package buddyworld

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

func combatFixture(t *testing.T, text string) []byte {
	t.Helper()
	data, err := hex.DecodeString(text)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func combatHealFixture(target, caster uint64, spell, amount, overheal uint32) []byte {
	data := append(packedFixture(target), packedFixture(caster)...)
	for _, word := range []uint32{spell, amount, overheal, 0} {
		data = binary.LittleEndian.AppendUint32(data, word)
	}
	return append(data, 0, 0)
}
func combatCastFixture(caster, target uint64, spell uint32, goEvent bool) []byte {
	data := append([]byte{0}, packedFixture(caster)...)
	data = append(data, 7)
	for _, word := range []uint32{spell, 0, 1500} {
		data = binary.LittleEndian.AppendUint32(data, word)
	}
	if goEvent {
		data = append(data, 0, 0)
	}
	data = binary.LittleEndian.AppendUint32(data, 2)
	return append(data, packedFixture(target)...)
}
func combatKillFixture(source, target uint64) []byte {
	return binary.LittleEndian.AppendUint64(binary.LittleEndian.AppendUint64(nil, source), target)
}

func TestCombatEventsDecodeExplicitServerFacts(t *testing.T) {
	spellDamage := combatFixture(t, "c9013330f101014902000009000000000000000200000000000000000000000000000000000000")
	melee := binary.LittleEndian.AppendUint32(nil, 0)
	melee = append(melee, packedFixture(5)...)
	melee = append(melee, packedFixture(npcGUID)...)
	for _, word := range []uint32{17, 0} {
		melee = binary.LittleEndian.AppendUint32(melee, word)
	}
	melee = append(melee, 1)
	for _, word := range []uint32{1, 0x41880000, 17} {
		melee = binary.LittleEndian.AppendUint32(melee, word)
	}
	melee = append(melee, 1)
	melee = append(melee, make([]byte, 8)...)
	ownFail := append([]byte{7}, binary.LittleEndian.AppendUint32(nil, 2050)...)
	ownFail = append(ownFail, 49)
	broadcastFail := append(packedFixture(4), ownFail...)
	tests := []struct {
		op                       uint32
		payload                  []byte
		kind                     string
		source, target           uint64
		spell, amount            uint32
		amountKnown, resultKnown bool
	}{
		{opcode.SMSGSpellnonmeleedamagelog, spellDamage, "spell_damage", 1, 0xf130000033000001, 585, 9, true, false},
		{opcode.SMSGAttackerstateupdate, melee, "melee_damage", 5, npcGUID, 0, 17, true, false},
		{opcode.SMSGSpellheallog, combatHealFixture(3, 1, 2050, 45, 5), "heal", 1, 3, 2050, 40, true, false},
		{opcode.SMSGSpellheallog, combatHealFixture(3, 1, 2050, 45, 45), "heal", 1, 3, 2050, 0, true, false},
		{opcode.SMSGSpellStart, combatCastFixture(4, npcGUID, 118, false), "cast_start", 4, npcGUID, 118, 0, false, false},
		{opcode.SMSGSpellGo, combatCastFixture(4, npcGUID, 118, true), "cast_go", 4, npcGUID, 118, 0, false, false},
		{opcode.SMSGCastFailed, ownFail, "cast_failure", 1, 0, 2050, 0, false, true},
		{opcode.SMSGSpellFailure, broadcastFail, "cast_failure", 4, 0, 2050, 0, false, true},
		{opcode.SMSGSpellFailedOther, broadcastFail, "cast_failure", 4, 0, 2050, 0, false, true},
		{opcode.SMSGPartykilllog, combatKillFixture(5, npcGUID), "death", 5, npcGUID, 0, 0, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.kind+"_"+opcode.WorldName(tt.op), func(t *testing.T) {
			got := decodeCombatEvent(tt.op, tt.payload, 1)
			if got == nil || got.Malformed || got.Kind != tt.kind || got.SourceGuid != tt.source || got.TargetGuid != tt.target || got.SpellId != tt.spell || got.Amount != tt.amount || got.AmountKnown != tt.amountKnown || got.ResultKnown != tt.resultKnown {
				t.Fatalf("%+v", got)
			}
			if tt.resultKnown && got.Result != 49 {
				t.Fatal(got)
			}
			if tt.kind == "cast_go" && (got.AmountKnown || got.ResultKnown) {
				t.Fatal("launch invented an applied effect")
			}
		})
	}
}

func TestCombatEventsMalformedAndUnsupportedRetainRawWithoutEffects(t *testing.T) {
	var durable bytes.Buffer
	s := NewService(1)
	s.ConfigureCombatJournal("aw-combat", &durable)
	damage := combatFixture(t, "c9013330f101014902000009000000000000000200000000000000000000000000000000000000")
	for n := 0; n < len(damage); n++ {
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellnonmeleedamagelog, Payload: damage[:n]})
	}
	unsupported := []byte{1, 2, 3}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGPeriodicauralog, Payload: unsupported})
	unsupported[0] = 99
	// Real object health observations must not become inferred damage/death logs.
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGUpdateObject, Payload: deltaFixture(1, map[int]uint32{unitHealth: 0})})
	r, err := s.CombatEvents(context.Background(), &worldv1.CombatEventsRequest{SessionId: "aw-combat", Limit: 1000})
	if err != nil || len(r.Events) != len(damage)+1 || len(r.Gaps) != 0 {
		t.Fatal(r, err)
	}
	for i, event := range r.Events {
		if event.Kind != "unknown" || event.AmountKnown || event.ResultKnown || event.SourceGuid != 0 || event.TargetGuid != 0 {
			t.Fatal(event)
		}
		if i < len(damage) && (!event.Malformed || !bytes.Equal(event.RawPayload, damage[:i])) {
			t.Fatal(event)
		}
	}
	if last := r.Events[len(r.Events)-1]; last.Malformed || !bytes.Equal(last.RawPayload, []byte{1, 2, 3}) {
		t.Fatal(last)
	}
	// Durable JSONL retains the complete original body and exact sequence.
	lines := bytes.Split(bytes.TrimSpace(durable.Bytes()), []byte{'\n'})
	if len(lines) != len(r.Events) {
		t.Fatal(len(lines))
	}
	var last worldv1.CombatEvent
	if err := protojson.Unmarshal(lines[len(lines)-1], &last); err != nil || last.Sequence != uint64(len(lines)) || !bytes.Equal(last.RawPayload, []byte{1, 2, 3}) {
		t.Fatal(&last, err)
	}
}

func TestCombatEventsCursorRetainsBurstAndIsSessionBound(t *testing.T) {
	s := NewService(1)
	s.ConfigureCombatJournal("aw-ordered", io.Discard)
	s.now = func() time.Time { return time.UnixMilli(1700000000123) }
	const total = 4097 // Larger than previous fixed packet queues; not a retention cap.
	for i := 1; i <= total; i++ {
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGPartykilllog, Payload: combatKillFixture(1, uint64(i))})
	}
	var after uint64
	for after < total {
		r, err := s.CombatEvents(context.Background(), &worldv1.CombatEventsRequest{SessionId: "aw-ordered", AfterSequence: after, Limit: 97})
		if err != nil || len(r.Events) == 0 || len(r.Gaps) != 0 || r.FirstSequence != 1 || r.LatestSequence != total || r.HasMore != (r.NextSequence < total) {
			t.Fatal(r, err)
		}
		for _, event := range r.Events {
			after++
			if event.Sequence != after || event.TargetGuid != after || event.ReceivedAtUnixMs != 1700000000123 {
				t.Fatal(event, after)
			}
		}
		if r.NextSequence != after {
			t.Fatal(r.NextSequence, after)
		}
	}
	first, _ := s.CombatEvents(context.Background(), &worldv1.CombatEventsRequest{SessionId: "aw-ordered", Limit: 1})
	first.Events[0].RawPayload[0] = 99 // callers cannot mutate retained evidence.
	again, _ := s.CombatEvents(context.Background(), &worldv1.CombatEventsRequest{SessionId: "aw-ordered", Limit: 1})
	if again.Events[0].RawPayload[0] != 1 {
		t.Fatal("reader changed journal")
	}
	if _, err := s.CombatEvents(context.Background(), &worldv1.CombatEventsRequest{SessionId: "old-session"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	ahead, err := s.CombatEvents(context.Background(), &worldv1.CombatEventsRequest{SessionId: "aw-ordered", AfterSequence: total + 9})
	if err != nil || len(ahead.Events) != 0 || len(ahead.Gaps) != 1 || ahead.Gaps[0].Reason != "cursor_ahead" || ahead.NextSequence != total {
		t.Fatal(ahead, err)
	}
}

type failingCombatWriter struct{}

func (failingCombatWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }
func TestCombatEventsDurabilityFailureIsExplicitAndDoesNotStopDelivery(t *testing.T) {
	s := NewService(1)
	s.ConfigureCombatJournal("aw-disk", failingCombatWriter{})
	for i := 0; i < 3; i++ {
		if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGPartykilllog, Payload: combatKillFixture(1, npcGUID)}); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.CombatEvents(context.Background(), &worldv1.CombatEventsRequest{SessionId: "aw-disk", AfterSequence: 1, Limit: 1})
	if err != nil || len(r.Events) != 1 || len(r.Gaps) != 1 || r.Gaps[0].FromSequence != 2 || r.Gaps[0].ThroughSequence != 2 || r.Gaps[0].Reason != "durability_unavailable" {
		t.Fatal(r, err)
	}
}
func TestCombatEventsConcurrentReadAndReceive(t *testing.T) {
	s := NewService(1)
	s.ConfigureCombatJournal("aw-race", io.Discard)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGPartykilllog, Payload: combatKillFixture(1, npcGUID)})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			s.CombatEvents(context.Background(), &worldv1.CombatEventsRequest{SessionId: "aw-race", Limit: 1})
		}
	}()
	wg.Wait()
	r, _ := s.CombatEvents(context.Background(), &worldv1.CombatEventsRequest{SessionId: "aw-race", Limit: 2000})
	if len(r.Events) != 1000 {
		t.Fatal(len(r.Events))
	}
}
