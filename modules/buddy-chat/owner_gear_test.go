package buddychat

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
	"time"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func gearPackets(revision int, nonce string) [][]byte {
	tokens := []string{"G=1,1,10,1,1"}
	for i := 0; i < 19; i++ {
		tokens = append(tokens, fmt.Sprintf("E=%d,1,0,1,1,0,1,0,0", i))
	}
	for id := range ownerProficiencies {
		tokens = append(tokens, fmt.Sprintf("P=%d,1", id))
	}
	var bodies []string
	for _, token := range tokens {
		if len(bodies) == 0 || len(bodies[len(bodies)-1])+len(token)+1 > 140 {
			bodies = append(bodies, token)
		} else {
			bodies[len(bodies)-1] += ";" + token
		}
	}
	var packets [][]byte
	for i, b := range bodies {
		p := makePlayerChat(2, 3, 0, ownerGearPrefix+fmt.Sprintf("1|%s|%d|%d|%d|19|%s", nonce, revision, i+1, len(bodies), b))
		binary.LittleEndian.PutUint32(p[1:], 0xffffffff)
		packets = append(packets, p)
	}
	return packets
}

func ingestGear(t *testing.T, s *Service, p []byte) {
	t.Helper()
	if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: uint32(opcode.SMSGMessagechat), Payload: p}); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerGearAtomicAuthenticatedIndependentFeed(t *testing.T) {
	s := NewService(nil, 1)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	ingestOwner(t, s, "1|a1b2c3d4e5f6|1|1|1|0|L=10")
	packets := gearPackets(1, "a1b2c3d4e5f6")
	for i := len(packets) - 1; i >= 1; i-- {
		ingestGear(t, s, packets[i])
	}
	if g := ownerState(t, s).OwnerGear; g != nil && g.Available {
		t.Fatal("partial equipment exposed", g)
	}
	now = now.Add(time.Second)
	ingestGear(t, s, packets[0])
	g := ownerState(t, s).OwnerGear
	if g == nil || !g.Available || !g.GearKnown || !g.ProficienciesKnown || g.AgeMs != 1000 || len(g.Slots) != 19 || len(g.Proficiencies) != 20 {
		t.Fatal(g)
	}
	g.Slots[0].Entry = 999
	if ownerState(t, s).OwnerGear.Slots[0].Entry != 0 {
		t.Fatal("mutable equipment snapshot")
	}
	if !ownerState(t, s).Available {
		t.Fatal("gear disrupted quest feed")
	}
	if s.latest != 0 || s.ignored != 0 || s.malformed != 0 {
		t.Fatal("gear leaked into chat")
	}
	for _, sender := range []uint64{1, 4, 8} {
		other := NewService(nil, 1)
		for _, p := range packets {
			p = append([]byte(nil), p...)
			binary.LittleEndian.PutUint64(p[5:], sender)
			ingestGear(t, other, p)
		}
		if ownerState(t, other).OwnerGear != nil {
			t.Fatal("untrusted sender accepted", sender)
		}
	}
	independent := NewIndependentService(nil, 1)
	for _, p := range packets {
		ingestGear(t, independent, p)
	}
	if ownerState(t, independent).OwnerGear != nil {
		t.Fatal("independent acquired owner authority")
	}
}

func TestOwnerGearNewReportsInvalidateOldComparisonsUntilSettled(t *testing.T) {
	s := NewService(nil, 1)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	first := gearPackets(1, "a1b2c3d4e5f6")
	for _, p := range first {
		ingestGear(t, s, p)
	}
	second := gearPackets(2, "a1b2c3d4e5f6")
	ingestGear(t, s, second[0])
	if ownerState(t, s).OwnerGear.Available {
		t.Fatal("older equipment valid during assembly")
	}
	now = now.Add(11 * time.Second)
	if ownerState(t, s).OwnerGear.Available {
		t.Fatal("expiry resurrected old equipment")
	}
	for _, p := range first {
		ingestGear(t, s, p)
	}
	if ownerState(t, s).OwnerGear.Available {
		t.Fatal("replay settled incomplete report")
	}
	for _, p := range second {
		ingestGear(t, s, p)
	}
	if !ownerState(t, s).OwnerGear.Available {
		t.Fatal("fresh complete equipment remained blocked")
	}
	// Structurally recognized but malformed authenticated equipment invalidates
	// destructive comparisons; a normal heartbeat can restore them.
	bad := append([]byte(nil), second[0]...)
	bad[len(bad)-2] = 'X'
	ingestGear(t, s, bad)
	if ownerState(t, s).OwnerGear.Available {
		t.Fatal("malformed report left comparison enabled")
	}
	for _, p := range gearPackets(3, "a1b2c3d4e5f6") {
		ingestGear(t, s, p)
	}
	if !ownerState(t, s).OwnerGear.Available {
		t.Fatal("heartbeat failed to restore")
	}
	now = now.Add(-time.Second)
	if ownerState(t, s).OwnerGear.Available {
		t.Fatal("clock rollback reported fresh equipment")
	}
}

func TestOwnerGearRejectsIncompleteContradictoryAndEnhancedFacts(t *testing.T) {
	base := &ownerFeedAssembly{nonce: "a1b2c3d4e5f6", revision: 1, active: 19, parts: 1}
	tokens := []string{"G=1,1,10,1,0"}
	for i := 0; i < 19; i++ {
		tokens = append(tokens, fmt.Sprintf("E=%d,1,0,1,1,0,1,0,0", i))
	}
	good := strings.Join(tokens, ";")
	base.tokens[0] = good
	if _, err := decodeOwnerGearSnapshot(base); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(good, "E=18,", "E=17,", 1), strings.Replace(good, "E=18,1,0,1,1,0,1,0,0", "E=18,0,0,0,0,0,0,0,0", 1), strings.Replace(good, "G=1,1,10,1,0", "G=1,1,10,1,1", 1), good + ";P=999,1", good + ";E=19,1,0,1,1,0,1,0,0", strings.Replace(good, "E=18,1,0,1,1,0,1,0,0", "E=18,1,0,1,1,1,1,0,0", 1)} {
		base.tokens[0] = bad
		if _, err := decodeOwnerGearSnapshot(base); err == nil {
			t.Fatal("invalid gear accepted", bad)
		}
	}
	// Enhancements are positive protection facts, never silently erased.
	base.tokens[0] = strings.Replace(good, "E=18,1,0,1,1,0,1,0,0", "E=18,1,100,1,1,1,1,5,10", 1)
	g, err := decodeOwnerGearSnapshot(base)
	if err != nil || !g.OwnerGear.Slots[18].HasEnhancements {
		t.Fatal(g, err)
	}
}

func TestOwnerGearCompleteExpiresWithoutRefreshingOnReadOrReplay(t *testing.T) {
	s := NewService(nil, 1)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	packets := gearPackets(1, "a1b2c3d4e5f6")
	for _, p := range packets {
		ingestGear(t, s, p)
	}
	now = now.Add(10 * time.Second)
	if ownerState(t, s).OwnerGear.Available {
		t.Fatal("expired complete snapshot available")
	}
	for _, p := range packets {
		ingestGear(t, s, p)
	}
	if ownerState(t, s).OwnerGear.Available {
		t.Fatal("replay refreshed old equipment")
	}
	for _, p := range gearPackets(2, "a1b2c3d4e5f6") {
		ingestGear(t, s, p)
	}
	if !ownerState(t, s).OwnerGear.Available {
		t.Fatal("fresh heartbeat failed")
	}
}
