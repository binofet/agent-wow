package modrt

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"strconv"
	"testing"
	"time"

	buddyworld "github.com/agent-wow/agent-wow/modules/buddy-world"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// Ordinary build-12340 CREATE/VALUES block. Packed GUID, movement and update
// masks are built independently of the World parser.
func encounterUnitUpdate(guid uint64, create bool, objectType byte, x float32, fields map[int]uint32) []byte {
	block := byte(0)
	if create {
		block = 3
	}
	body := append([]byte{block}, spellFixturePackedGUID(guid)...)
	if create {
		body = append(body, objectType, 0x20, 0)
		body = binary.LittleEndian.AppendUint32(body, 0)
		body = binary.LittleEndian.AppendUint16(body, 0)
		body = binary.LittleEndian.AppendUint32(body, 5)
		for _, f := range []float32{x, 0, 0, 0} {
			body = binary.LittleEndian.AppendUint32(body, math.Float32bits(f))
		}
		body = binary.LittleEndian.AppendUint32(body, 0)
		for _, f := range []float32{2.5, 7, 4.5, 4.7, 2.5, 7, 4.5, 3.14, 3.14} {
			body = binary.LittleEndian.AppendUint32(body, math.Float32bits(f))
		}
	}
	maxField := 0
	for key := range fields {
		maxField = max(maxField, key)
	}
	masks := make([]uint32, 1+maxField/32)
	for key := range fields {
		masks[key/32] |= 1 << uint(key%32)
	}
	body = append(body, byte(len(masks)))
	for _, mask := range masks {
		body = binary.LittleEndian.AppendUint32(body, mask)
	}
	for index := 0; index <= maxField; index++ {
		if v, ok := fields[index]; ok {
			body = binary.LittleEndian.AppendUint32(body, v)
		}
	}
	return body
}

func TestPackagedEncounterCreatureFactsReachActualScheduledLua(t *testing.T) {
	runner := &policySpellRunner{world: buddyworld.NewService(policyMageGUID), hostOwnsEncounter: true}
	manager, ctx, writes := startPackagedPolicySpellWith(t, runner, fixtureSmite)
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGLoginVerifyWorld, make([]byte, 20))
	const count = 33
	const victim = policyWolfGUID + count - 1
	id := strconv.FormatUint(victim, 10)
	// The own selected NPC is the 33rd by distance. The ordinary nearby list
	// remains bounded and incomplete, while this personally selected unit is visible.
	blocks := binary.LittleEndian.AppendUint32(nil, count+1)
	blocks = append(blocks, encounterUnitUpdate(policyMageGUID, true, 4, 0, map[int]uint32{0x17: 1 | (5 << 8), 0x18: 100, 0x20: 100, 0x36: 44, 0x37: 1, 0x12: uint32(victim & 0xffffffff), 0x13: uint32(victim >> 32)})...)
	for i := 0; i < count; i++ {
		blocks = append(blocks, encounterUnitUpdate(policyWolfGUID+uint64(i), true, 3, float32(i+2), map[int]uint32{
			3: 555, 0x18: 735, 0x20: 735, 0x36: uint32(51 + i), 0x37: 14, 0x42: math.Float32bits(2.75)})...)
	}
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGUpdateObject, blocks)
	// A normal, self-cast enemy buff and another creature's debuff share the visible list.
	auras := binary.LittleEndian.AppendUint32(append(spellFixturePackedGUID(victim), 7), 8269)
	auras = append(auras, 0x19, 48, 0)
	auras = binary.LittleEndian.AppendUint32(append(auras, 12), 18173)
	auras = append(auras, 0xa1, 46, 3)
	auras = append(auras, spellFixturePackedGUID(policyWolfGUID)...)
	auras = binary.LittleEndian.AppendUint32(auras, 60000)
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGAuraUpdateAll, binary.LittleEndian.AppendUint32(auras, 60000))
	name := binary.LittleEndian.AppendUint32(nil, 555)
	name = append(name, "Test Beast\x00\x00\x00\x00Watcher\x00\x00"...)
	tail := make([]byte, 77)
	binary.LittleEndian.PutUint32(tail[4:], 1)
	binary.LittleEndian.PutUint32(tail[8:], 21)
	binary.LittleEndian.PutUint32(tail[12:], 1)
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGCreatureQueryResponse, append(name, tail...))
	// A hostile spell START is visible even without a learned interrupt.
	caster := spellFixturePackedGUID(victim)
	cast := append(append([]byte{}, caster...), caster...)
	cast = binary.LittleEndian.AppendUint32(append(cast, 2), 2054)
	cast = binary.LittleEndian.AppendUint32(cast, 0)
	cast = binary.LittleEndian.AppendUint32(cast, 12000)
	cast = append(binary.LittleEndian.AppendUint32(cast, 2), spellFixturePackedGUID(policyMageGUID)...)
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGSpellStart, cast)
	stop := startPackagedEncounterBundle(t, ctx, manager, writes)
	defer stop()
	manager.mu.Lock()
	policy := manager.policy
	manager.mu.Unlock()
	check := func(level float64, known bool) {
		t.Helper()
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			state, err := policy.vm.State(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if state.LatestDecision != nil {
				rows, _ := state.LatestDecision.Output["queue"].([]any)
				if len(rows) == count {
					body, err := json.Marshal(state.LatestDecision.Output)
					if err != nil || len(body) > 32*1024 {
						t.Fatal("decision budget", len(body), err)
					}
					for _, r := range rows {
						row := r.(map[string]any)
						value, present := row["level"]
						if row["targetGuid"] == id && present == known && (!known || value == level) {
							t.Logf("%d current rows, %d-byte real Lua decision", len(rows), len(body))
							return
						}
					}
				}
			}
			select {
			case <-ctx.Done():
				t.Fatalf("selected %s level %v known=%v failed to reach actual Lua: %+v", id, level, known, state.LatestDecision)
			case <-ticker.C:
			}
		}
	}
	check(83, true)
	sid := policy.vm.Status().SessionID
	obs, err := manager.readPolicyObservation(ctx, sid, encounterObservationMethod)
	if err != nil {
		t.Fatal(err)
	}
	targets := obs["targets"].(map[string]any)
	selected := targets[id].(map[string]any)
	visible, _ := selected["visibleAuras"].(map[string]any)
	slots, _ := visible["slots"].([]any)
	if visible["complete"] != true || len(slots) != 2 {
		t.Fatalf("selected creature's normal visible aura list missing: %v", visible)
	}
	buff, debuff := slots[0].(map[string]any), slots[1].(map[string]any)
	if buff["spellId"] != float64(8269) || buff["casterGuid"] != id || buff["positive"] != true || buff["remainingMs"] != nil ||
		debuff["spellId"] != float64(18173) || debuff["stacksOrCharges"] != float64(3) || debuff["negative"] != true || debuff["casterGuid"] != strconv.FormatUint(policyWolfGUID, 10) || debuff["maxDurationMs"] != float64(60000) {
		t.Fatal("visible spell, count, timer or source changed in the ordinary policy feed", slots)
	}
	templates := obs["creatureTemplates"].(map[string]any)
	tooltip := templates["555"].(map[string]any)
	self := obs["players"].(map[string]any)[strconv.FormatUint(policyMageGUID, 10)].(map[string]any)
	active := selected["activeCast"].(map[string]any)
	if self["selectedTargetGuid"] != id || selected["level"] != float64(83) || selected["levelKnown"] != true || active["spellId"] != float64(2054) || active["targetGuid"] != strconv.FormatUint(policyMageGUID, 10) ||
		tooltip["name"] != "Test Beast" || tooltip["subName"] != "Watcher" || tooltip["rank"] != float64(1) || len(templates) != 1 || len(targets) != count || obs["nearbyCreaturesComplete"] != false {
		t.Fatalf("current selected row=%v tooltip=%v self=%v count=%d", selected, tooltip, self, len(targets))
	}
	frame, err := json.Marshal(obs)
	if err != nil || len(frame) > behavior.DefaultSessionValueBytes-512 {
		t.Fatal("observation exceeds existing limit", len(frame), err)
	}
	t.Logf("%d-byte actual full encounter feed (%d creatures, one template)", len(frame), len(targets))
	update := func(fields map[int]uint32) {
		deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGUpdateObject, append(binary.LittleEndian.AppendUint32(nil, 1), encounterUnitUpdate(victim, false, 0, 0, fields)...))
	}
	update(map[int]uint32{0x36: 0xffffffff})
	check(0, false)
	update(map[int]uint32{0x36: 39})
	check(39, true)
	noPackagedWrites(t, writes)
}
