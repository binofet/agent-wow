package buddyactions

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/types/known/emptypb"
	"os"
	"testing"
)

func loadBehavior(t *testing.T, s *Service, source string) string {
	t.Helper()
	before, _ := s.Behavior(context.Background(), &emptypb.Empty{})
	after, err := s.ReloadBehavior(context.Background(), &actionsv1.BehaviorReloadRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, ExpectedRevision: before.Revision, Source: source})
	if err != nil {
		t.Fatal(err)
	}
	return after.Revision
}

func TestBehaviorLiveReloadChangesTriageWithoutChangingBodyOrGrantingTargets(t *testing.T) {
	s, f, _ := rig(t)
	enableTriage(t, s)
	f.world.Self.Health = 100
	f.world.Owner.Health = 60
	mira := triagePlayer(f, 4, 15)
	f.world.NearbyPlayers = []*worldv1.PlayerState{mira}
	triageMember(f, 4)
	before := s.healingTargetLocked(context.Background(), f.world, true, false)
	if before == nil || before.Guid != 4 {
		t.Fatal("native triage fixture")
	}
	originalSession := s.sessionID
	loadBehavior(t, s, `return {decide=function(state) if state.kind=="heal_target" then return {targetGuid="3"} end return {} end}`)
	if p := s.healingTargetLocked(context.Background(), f.world, true, false); p == nil || p.Guid != 3 {
		t.Fatal("hot policy was not used")
	}
	loadBehavior(t, s, `return {decide=function(state) return {targetGuid="999"} end}`)
	if p := s.healingTargetLocked(context.Background(), f.world, true, false); p == nil || p.Guid != 4 {
		t.Fatal("unobserved target granted")
	}
	if s.sessionID != originalSession || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("policy change sent game traffic or changed body")
	}
	f.world.Self.Health = 1
	if p := s.healingTargetLocked(context.Background(), f.world, true, false); p == nil || p.Guid != s.selfGUID {
		t.Fatal("script bypassed urgent self preservation")
	}
}

func TestBehaviorCannotSelectUnknownUnlearnedOrUnaffordableSpell(t *testing.T) {
	s, f, _ := buffRig(t, 5, 2050, 2052)
	s.snapshot(context.Background())
	f.world.Owner.Health = 10
	loadBehavior(t, s, `return {decide=function(state) return {spellId=2050} end}`)
	if r := s.bestHealLocked(f.world.Self, f.world.Owner); r.ID != 2050 {
		t.Fatal("actual learned rank not selectable", r.ID)
	}
	for _, source := range []string{`return {decide=function(state) return {spellId=999999} end}`, `return {decide=function(state) return {spellId=2053} end}`} {
		loadBehavior(t, s, source)
		if r := s.bestHealLocked(f.world.Self, f.world.Owner); r.ID == 0 || r.ID == 2053 || r.ID == 999999 {
			t.Fatal("unlearned spell selected", r.ID)
		}
	}
	f.world.Self.Power = 0
	if r := s.bestHealLocked(f.world.Self, f.world.Owner); r.ID != 0 {
		t.Fatal("script created mana", r.ID)
	}
}

func TestBehaviorCASBodyBindingInvalidReloadAndBuiltinRollback(t *testing.T) {
	s, _, _ := rig(t)
	revision := loadBehavior(t, s, `return {decide=function(state) return {} end}`)
	for _, r := range []*actionsv1.BehaviorReloadRequest{
		{SessionId: "old", SelfGuid: s.selfGUID, ExpectedRevision: revision, UseBuiltin: true},
		{SessionId: s.sessionID, SelfGuid: 999, ExpectedRevision: revision, UseBuiltin: true},
		{SessionId: s.sessionID, SelfGuid: s.selfGUID, UseBuiltin: true},
		{SessionId: s.sessionID, SelfGuid: s.selfGUID, ExpectedRevision: revision, Source: "not lua("},
	} {
		if _, err := s.ReloadBehavior(context.Background(), r); err == nil {
			t.Fatal("invalid update accepted")
		}
		if current, _ := s.Behavior(context.Background(), &emptypb.Empty{}); current.Revision != revision {
			t.Fatal("last working policy lost")
		}
	}
	after, err := s.ReloadBehavior(context.Background(), &actionsv1.BehaviorReloadRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, ExpectedRevision: revision, UseBuiltin: true})
	if err != nil || after.Revision != "" {
		t.Fatal("builtin rollback failed", err)
	}
}

func TestShippedLuaPolicyUsesGeneratedContractForActualSpellChoices(t *testing.T) {
	s, f, _ := buffRig(t, 5, 2050, 2052)
	s.snapshot(context.Background())
	source, err := os.ReadFile("../../behaviors/party.lua")
	if err != nil {
		t.Fatal(err)
	}
	revision := loadBehavior(t, s, string(source))
	f.world.Owner.Health = f.world.Owner.MaxHealth - 1
	if s.bestHealLocked(f.world.Self, f.world.Owner).ID == 0 {
		t.Fatal("shipped script could not select actual heal")
	}
	state, _ := s.Behavior(context.Background(), &emptypb.Empty{})
	if state.LastGoodRevision != revision || state.Failures != 0 {
		t.Fatalf("script failed: %s", state.LastError)
	}
}

func TestShippedLuaPolicyPreservesNativeHealTargetTie(t *testing.T) {
	s, f, _ := rig(t)
	enableTriage(t, s)
	f.world.Self.Health = 60
	f.world.Owner.Health = 120
	f.world.Owner.MaxHealth = 200
	mira := triagePlayer(f, 4, 180)
	mira.MaxHealth = 300
	f.world.NearbyPlayers = []*worldv1.PlayerState{mira}
	triageMember(f, mira.Guid)
	if p := s.healingTargetLocked(context.Background(), f.world, true, false); p == nil || p.Guid != f.world.Owner.Guid {
		t.Fatalf("native tie should select Joel, got %v", p)
	}
	source, err := os.ReadFile("../../behaviors/party.lua")
	if err != nil {
		t.Fatal(err)
	}
	loadBehavior(t, s, string(source))
	for _, tc := range []struct {
		name             string
		self, joel, mira uint32
		want             uint64
	}{
		{"equal ratios preserve Joel", 60, 120, 180, 3},
		{"lower self", 50, 120, 180, 1},
		{"lower Joel", 60, 100, 180, 3},
		{"lower Mira", 60, 120, 150, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.world.Self.Health, f.world.Owner.Health, mira.Health = tc.self, tc.joel, tc.mira
			if p := s.healingTargetLocked(context.Background(), f.world, true, false); p == nil || p.Guid != tc.want {
				t.Fatalf("want target %d, got %v", tc.want, p)
			}
			state, _ := s.Behavior(context.Background(), &emptypb.Empty{})
			if state.Failures != 0 {
				t.Fatalf("script failed: %s", state.LastError)
			}
		})
	}
}
