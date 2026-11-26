package modrt

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
)

type policyMeleeWorld struct {
	policySpellWorld
	combat       atomic.Bool
	exposeNearby bool
}

const policyMeleeVisit = 1

func (w *policyMeleeWorld) Snapshot(ctx context.Context, req *worldv1.SnapshotRequest) (*worldv1.SnapshotResponse, error) {
	targetRequested := req.GetCreatureGuid() == policyWolfGUID
	if w.exposeNearby && !targetRequested {
		req = &worldv1.SnapshotRequest{CreatureGuid: policyWolfGUID}
	}
	view, err := w.policySpellWorld.Snapshot(ctx, req)
	if err != nil {
		return nil, err
	}
	view.VisitRevision, view.ObservedPackets = policyMeleeVisit, 10
	self := view.Self
	self.ClassId, self.PowerType = 4, 3 // Rogue with energy.
	self.LevelKnown, self.Level = true, 10
	inCombat := w.combat.Load()
	if inCombat {
		self.UnitFlags = 0x80000
	}
	self.ChannelSpellKnown, self.ChannelObjectKnown = true, true
	self.CombatReachKnown, self.CombatReach = true, 1.5
	self.BaseAttackTimeKnown, self.BaseAttackTimeMs = true, 2000
	self.Mainhand = &worldv1.EquippedWeapon{
		SlotKnown: true, ItemGuid: 0x4000000000000001, Entry: fixtureDagger, MetadataKnown: true,
		DurabilityKnown: true, Durability: 20, MaxDurability: 20,
	}
	if creature := view.RequestedCreature; creature != nil {
		creature.X = 3
		creature.MaxHealth = 100
		creature.LevelKnown, creature.Level = true, 10
		creature.CombatReachKnown, creature.CombatReach = true, 1.5
		creature.DynamicFlagsKnown = true
		if inCombat {
			creature.UnitFlags = 0x80000
			creature.AttackVictimKnown, creature.AttackVictimGuid, creature.AttackAgeMs = true, self.Guid, 10
			creature.TargetKnown, creature.TargetGuid = true, self.Guid
		}
		if w.exposeNearby {
			view.NearbyCreatures = []*worldv1.CreatureState{creature}
		}
		if !targetRequested {
			view.RequestedCreature = nil
		}
	}
	return view, nil
}

func policyMeleeFixture(t *testing.T, observed bool) (*Manager, context.Context, <-chan policySpellWrite, *policyMeleeWorld) {
	t.Helper()
	binDir := os.Getenv("BUDDY_ACTIONS_PACKAGED_BIN_DIR")
	if binDir == "" {
		t.Skip("set BUDDY_ACTIONS_PACKAGED_BIN_DIR to a native buddy-actions build with the hosted melee bridge")
	}
	binaryPath, err := filepath.Abs(filepath.Join(binDir, "buddy-actions"))
	if err != nil {
		t.Fatal(err)
	}
	world := &policyMeleeWorld{exposeNearby: observed}
	runner := &policySpellRunner{binary: binaryPath, peers: make(map[string]*grpc.Server), world: world, hostOwnsEncounter: observed}
	manager := New(packagedPolicySpellRegistry(t), runner, nil)
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	writes := make(chan policySpellWrite, 16)
	if err := manager.Start(ctx, modsession.Session{
		Identity: modsession.Identity{CharacterGUID: policyMageGUID, CharacterName: "Fixture"},
		Clock:    func() uint32 { return 12345 },
		SendPacket: func(ctx context.Context, code uint32, body []byte) error {
			select {
			case writes <- policySpellWrite{code, bytes.Clone(body), modsession.WriteOriginFromContext(ctx)}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}); err != nil {
		t.Fatal(err)
	}
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGLoginVerifyWorld, make([]byte, 20))
	book := []byte{0, 3, 0}
	for _, spell := range []uint32{6603, fixtureSap, fixtureStealth} {
		book = binary.LittleEndian.AppendUint32(book, spell)
		book = append(book, 0, 0)
	}
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGInitialSpells, append(book, 0, 0))
	catalog, _, err := manager.observedSession()
	if err != nil {
		t.Fatal(err)
	}
	if observed {
		// The hosted encounter reads equipped weapon metadata on its own tick.
		checkPackagedWrite(t, ctx, writes, opcode.CMSGItemQuerySingle, binary.LittleEndian.AppendUint32(nil, fixtureDagger),
			modsession.WriteOrigin{SessionID: catalog.SessionID, Module: actionsModule, Writer: "module"})
		deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGItemQuerySingleResponse, packagedDaggerResponse())
	} else {
		primePackagedSap(t, ctx, manager, writes, catalog.SessionID)
	}
	noPackagedWrites(t, writes)
	world.combat.Store(!observed)
	return manager, ctx, writes, world
}

func TestPackagedActionsPolicyExactMelee(t *testing.T) {
	manager, ctx, writes, _ := policyMeleeFixture(t, false)
	policy, err := manager.StartPolicy(ctx, fmt.Sprintf(`
local receipts = ""
local settled = {}
return {
  on_event = function(event)
    if event.receipt then
      receipts = receipts .. "/" .. event.receipt.outcome .. ":" .. event.receipt.action_id
      if event.receipt.outcome == "confirmed" then settled = event.receipt.payload end
    end
    return true
  end,
  decide = function(frame)
    if frame.input.capability then
      return { action = {
        capability = frame.input.capability, sessionId = frame.session_id,
        selfGuid = "%d", targetGuid = "%d", visitId = frame.input.visitId or "%d",
      } }
    end
    return { receipts = receipts, settled = settled }
  end,
  export_state = function() return {version = 1, data = {receipts = receipts, settled = settled}} end,
  import_state = function(checkpoint)
    receipts, settled = checkpoint.data.receipts, checkpoint.data.settled
    return true
  end,
}`, policyMageGUID, policyWolfGUID, policyMeleeVisit))
	if err != nil {
		t.Fatal(err)
	}

	dispatch := func(capability string, id uint64, code uint32, body []byte) {
		t.Helper()
		before := policy.vm.Status()
		_, receipt, err := policy.Step(ctx, map[string]any{"capability": capability}, nil)
		if err != nil || receipt == nil || receipt.ActionID != id || receipt.Outcome != behavior.ActionTransportAccepted {
			t.Fatalf("%s transport receipt: %+v %v", capability, receipt, err)
		}
		checkPackagedWrite(t, ctx, writes, code, body, modsession.WriteOrigin{
			SessionID: before.SessionID, Module: "buddy-actions", Writer: "lua",
			PolicyRevision: before.Revision, Generation: before.Epoch, ActionID: strconv.FormatUint(id, 10),
		})
		noPackagedWrites(t, writes)
	}
	start := func(actor, target uint64) {
		t.Helper()
		body := binary.LittleEndian.AppendUint64(nil, actor)
		deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGAttackstart, binary.LittleEndian.AppendUint64(body, target))
	}
	stop := func(actor, target uint64) {
		t.Helper()
		body := append(spellFixturePackedGUID(actor), spellFixturePackedGUID(target)...)
		deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGAttackstop, binary.LittleEndian.AppendUint32(body, 0))
	}
	attackState := func(active bool, target uint64) {
		t.Helper()
		raw, err := manager.InvokeJSON(ctx, "buddy-actions.status", nil)
		if err != nil {
			t.Fatal(err)
		}
		state := &actionsv1.State{}
		if err := protojson.Unmarshal(raw, state); err != nil {
			t.Fatal(err)
		}
		if state.GetAutoAttacking() != active || state.GetAutoAttackTargetGuid() != target || state.GetMoving() || state.GetCastPending() {
			t.Fatalf("unexpected Actions melee state: %v", state)
		}
	}
	settledBinding := func(capability, phase string) {
		t.Helper()
		view, receipt, err := policy.Step(ctx, nil, nil)
		settled, _ := view["settled"].(map[string]any)
		if err != nil || receipt != nil || settled["capability"] != capability || settled["phase"] != phase ||
			settled["self_guid"] != fmt.Sprint(policyMageGUID) || settled["target_guid"] != fmt.Sprint(policyWolfGUID) ||
			settled["visit_id"] != fmt.Sprint(policyMeleeVisit) || settled["transport_written"] != true || settled["effect_observed"] != true {
			t.Fatalf("unexpected Lua melee confirmation: %+v %+v %v", view, receipt, err)
		}
	}
	_, rejected, err := policy.Step(ctx, map[string]any{"capability": "melee_start", "visitId": "2"}, nil)
	if err != nil || rejected == nil || rejected.ActionID != 1 || rejected.Outcome != behavior.ActionNotDispatched {
		t.Fatalf("different visit was not definitely rejected: %+v %v", rejected, err)
	}
	noPackagedWrites(t, writes)

	// Vary the entry portion while retaining the low creature ID. Matching the
	// low GUID alone must never confirm either operation.
	const otherTarget = policyWolfGUID ^ (1 << 24)
	dispatch("melee_start", 2, opcode.CMSGAttackswing, binary.LittleEndian.AppendUint64(nil, policyWolfGUID))
	const starting = "/not_dispatched:1/transport_accepted:2"
	checkHostileReceipt(t, ctx, policy, starting, 1)
	start(policyMageGUID+1, policyWolfGUID)
	checkHostileReceipt(t, ctx, policy, starting, 1)
	start(policyMageGUID, otherTarget)
	checkHostileReceipt(t, ctx, policy, starting, 1)
	start(policyMageGUID, policyWolfGUID)
	const started = starting + "/confirmed:2"
	checkHostileReceipt(t, ctx, policy, started, 0)
	settledBinding("melee_start", "confirmed_attack_start")
	attackState(true, policyWolfGUID)

	dispatch("melee_stop", 3, opcode.CMSGAttackstop, nil)
	const stopping = started + "/transport_accepted:3"
	checkHostileReceipt(t, ctx, policy, stopping, 1)
	stop(policyMageGUID+1, policyWolfGUID)
	checkHostileReceipt(t, ctx, policy, stopping, 1)
	stop(policyMageGUID, otherTarget)
	checkHostileReceipt(t, ctx, policy, stopping, 1)
	stop(policyMageGUID, policyWolfGUID)
	checkHostileReceipt(t, ctx, policy, stopping+"/confirmed:3", 0)
	settledBinding("melee_stop", "confirmed_attack_stop")
	attackState(false, 0)
	noPackagedWrites(t, writes)
}
