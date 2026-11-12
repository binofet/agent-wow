package buddyquests

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const guardObject55 uint64 = 0xf110000037000001
const guardObject56 uint64 = 0xf110000038000002

func guardObjectRig(entry uint32) (*Service, *fakeSession, uint64) {
	f := fixture()
	f.world.QuestGivers = nil
	f.world.Self.PositionKnown, f.world.Self.UnitFlagsKnown, f.world.Self.MovementFlagsKnown = true, true, true
	f.world.Self.PositionRevision = 1
	guid := guardObject55
	if entry == 56 {
		guid = guardObject56
	}
	f.world.QuestObjects = []*worldv1.GameObjectState{{Guid: guid, Entry: entry, Present: true, MapKnown: true, PositionKnown: true, X: 1, PositionRevision: 1, TypeKnown: true, GameObjectType: 2, FlagsKnown: true, CreatedByKnown: true, StateKnown: true, State: 1}}
	f.onAcquire = func() { f.actions.InteractionLeaseRemainingMs = 15000 }
	return NewService(f, "guard-body", 1), f, guid
}
func objectList(t *testing.T, guid uint64, id uint32) []byte {
	p := unhex(t, listHex)
	binary.LittleEndian.PutUint64(p, guid)
	binary.LittleEndian.PutUint32(p[27:], id)
	return p
}
func objectReward(guid uint64, id uint32) []byte {
	p := rewardFixture(false)
	binary.LittleEndian.PutUint64(p, guid)
	binary.LittleEndian.PutUint32(p[8:], id)
	return p
}

func TestLostGuardGiverDiscoveryAndNormalObjectUseAccept(t *testing.T) {
	for _, entry := range []uint32{55, 56} {
		s, f, guid := guardObjectRig(entry)
		id := uint32(45)
		if entry == 56 {
			id = 71
		}
		d, err := s.Discover(context.Background(), nil)
		if err != nil || len(d.Nearby) != 1 || d.Nearby[0].Creature != nil || d.Nearby[0].GameObject.Guid != guid || !d.Nearby[0].CanInteract {
			t.Fatal(d, err)
		}
		f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
			switch p.Opcode {
			case 0xb1: // CMSG_GAMEOBJ_USE, not NPC HELLO or a spell.
				if !bytes.Equal(p.Payload, npcPacket(guid)) {
					t.Error("object use GUID mismatch")
				}
				packet(t, s, opcode.SMSGQuestgiverQuestList, objectList(t, guid, id))
			case opcode.CMSGQuestgiverAcceptQuest:
				if !bytes.Equal(p.Payload, questPacket(guid, id, true)) {
					t.Error("accept binding mismatch")
				}
				f.confirm(id)
			default:
				t.Errorf("unexpected packet %x", p.Opcode)
			}
			return nil
		}
		o, err := s.Offer(context.Background(), &questsv1.OfferRequest{SessionId: "guard-body", NpcGuid: guid})
		if err != nil || !o.ServerResponseReceived || o.State.Offer == nil || o.State.Offer.NpcGuid != guid {
			t.Fatal(o, err)
		}
		r := &questsv1.QuestRequest{SessionId: "guard-body", NpcGuid: guid, QuestId: id, OfferRevision: o.State.Offer.Revision}
		accepted, err := s.Accept(context.Background(), r)
		if err != nil || accepted.State.LastAcceptance.Status != questsv1.AcceptanceStatus_CONFIRMED_IN_LOG || accepted.State.LastAcceptance.ConfirmedLogRevision <= 1 {
			t.Fatal(accepted, err)
		}
		if _, err = s.Accept(context.Background(), r); err == nil || len(f.packets) != 2 {
			t.Fatal("accepted twice", err)
		}
		if _, _, released, held := f.counters(); released != 2 || held {
			t.Fatal("lease not released", released, held)
		}
	}
}

func TestLostGuardTurnInRequiresMatchingRewardReceiptAndOwnRemoval(t *testing.T) {
	for _, entry := range []uint32{55, 56} {
		s, f, guid := guardObjectRig(entry)
		id := uint32(37)
		if entry == 56 {
			id = 45
		}
		f.world.SelfQuestLog[0].QuestId = id
		f.world.SelfQuestLog[0].Complete = true
		f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
			switch p.Opcode {
			case opcode.CMSGQuestgiverCompleteQuest:
				packet(t, s, opcode.SMSGQuestgiverOfferReward, objectReward(guid, id))
			case opcode.CMSGQuestgiverChooseReward:
				if !bytes.Equal(p.Payload, rewardPacket(guid, id, 0)) {
					t.Error("wrong chosen reward binding")
				}
				packet(t, s, opcode.SMSGQuestgiverQuestComplete, rewardReceiptFixture(id))
				f.confirm(0)
			}
			return nil
		}
		o, err := s.TurnIn(context.Background(), &questsv1.TurnInRequest{SessionId: "guard-body", NpcGuid: guid, QuestId: id, QuestLogRevision: 1})
		if err != nil || o.State.TurnInOffer == nil {
			t.Fatal(o, err)
		}
		r := &questsv1.RewardRequest{SessionId: "guard-body", NpcGuid: guid, QuestId: id, OfferRevision: o.State.TurnInOffer.Revision, NoChoice: true}
		out, err := s.ClaimReward(context.Background(), r)
		if err != nil || out.State.LastReward.Status != questsv1.RewardStatus_REWARD_CONFIRMED || !out.State.LastReward.ServerRewardObserved || !out.State.LastReward.LogRemovalConfirmed {
			t.Fatal(out, err)
		}
		if _, err = s.ClaimReward(context.Background(), r); err == nil || len(f.packets) != 2 {
			t.Fatal("reward replayed", err)
		}
	}
}

func TestLostGuardTypedIdentitySceneAndLeaseFailClosed(t *testing.T) {
	cases := map[string]func(*fakeSession){
		"missing":          func(f *fakeSession) { f.world.QuestObjects = nil },
		"entry mismatch":   func(f *fakeSession) { f.world.QuestObjects[0].Entry = 56 },
		"wrong type":       func(f *fakeSession) { f.world.QuestObjects[0].GameObjectType = 3 },
		"unknown type":     func(f *fakeSession) { f.world.QuestObjects[0].TypeKnown = false },
		"unknown flags":    func(f *fakeSession) { f.world.QuestObjects[0].FlagsKnown = false },
		"locked":           func(f *fakeSession) { f.world.QuestObjects[0].Flags = 2 },
		"summoned":         func(f *fakeSession) { f.world.QuestObjects[0].CreatedByGuid = 3 },
		"other map":        func(f *fakeSession) { f.world.QuestObjects[0].MapId = 1 },
		"unknown pose":     func(f *fakeSession) { f.world.QuestObjects[0].PositionKnown = false },
		"outside range":    func(f *fakeSession) { f.world.QuestObjects[0].X = 5 },
		"other floor":      func(f *fakeSession) { f.world.QuestObjects[0].Z = 3 },
		"combat":           func(f *fakeSession) { f.world.Self.UnitFlags = 0x80000 },
		"dead":             func(f *fakeSession) { f.world.Self.Alive = false },
		"moving":           func(f *fakeSession) { f.actions.Moving = true },
		"cast":             func(f *fakeSession) { f.actions.CastPending = true },
		"motion ambiguous": func(f *fakeSession) { f.actions.MotionUnconfirmed = true },
		"expired lease":    func(f *fakeSession) { f.actions.InteractionLeaseRemainingMs = 0 },
		"correction":       func(f *fakeSession) { f.world.Self.X = 100; f.world.Self.PositionRevision++ },
		"contradictory": func(f *fakeSession) {
			o := proto.Clone(f.world.QuestObjects[0]).(*worldv1.GameObjectState)
			o.X = 2
			f.world.QuestObjects = append(f.world.QuestObjects, o)
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s, f, guid := guardObjectRig(55)
			f.onAcquire = func() { f.actions.InteractionLeaseRemainingMs = 15000; change(f) }
			if _, err := s.Offer(context.Background(), &questsv1.OfferRequest{SessionId: "guard-body", NpcGuid: guid}); err == nil {
				t.Fatal("unsafe object accepted")
			}
			if n, _, _, held := f.counters(); n != 0 || held || len(f.approaches) != 0 {
				t.Fatal("object triggered movement/write or retained lease", n, held)
			}
		})
	}
}

func TestLostGuardOnlyFixedQuestRelationsAndSourceReplies(t *testing.T) {
	s, f, guid := guardObjectRig(55)
	for _, bad := range []uint64{3, npcGUID & 0xffff, 0xf110000039000001, 0xf110000037000000} {
		if _, err := s.Offer(context.Background(), &questsv1.OfferRequest{SessionId: "guard-body", NpcGuid: bad}); status.Code(err) != codes.InvalidArgument {
			t.Fatal(bad, err)
		}
	}
	if _, err := s.TurnIn(context.Background(), &questsv1.TurnInRequest{SessionId: "guard-body", NpcGuid: guid, QuestId: 45}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	s.state.Offer = &questsv1.Offer{NpcGuid: guid, Revision: 1, Quests: []*questsv1.Quest{{QuestId: 71, Available: true}}}
	s.offerUntil = time.Now().Add(time.Minute)
	if _, err := s.Accept(context.Background(), &questsv1.QuestRequest{SessionId: "guard-body", NpcGuid: guid, QuestId: 71, OfferRevision: 1}); err == nil {
		t.Fatal("cross-object quest accepted")
	}
	if len(f.packets) != 0 || f.acquired != 0 {
		t.Fatal("invalid relation acted")
	}
	packet(t, s, opcode.SMSGQuestgiverQuestList, objectList(t, guid, 45))
	if s.state.Offer.Revision != 1 {
		t.Fatal("unsolicited object reply replaced offer")
	}
	for _, g := range []uint64{guardObject55, guardObject56} {
		p := unhex(t, detailsHex)
		binary.LittleEndian.PutUint64(p, g)
		if _, err := decodeDetails(p); err != nil {
			t.Fatal("ordinary GO details rejected", err)
		}
		binary.LittleEndian.PutUint64(p[8:], 3)
		if _, err := decodeDetails(p); err == nil {
			t.Fatal("GO accepted player share divider")
		}
	}
}

func TestLostGuardAmbiguousRewardCannotRepeat(t *testing.T) {
	s, f, guid := guardObjectRig(55)
	f.world.SelfQuestLog[0].QuestId = 37
	s.state.TurnInOffer = &questsv1.TurnInOffer{NpcGuid: guid, QuestId: 37, Revision: 2, Source: "reward_offer", CanComplete: true}
	s.turnIn.offerUntil = time.Now().Add(time.Minute)
	f.onSend = func(context.Context, *modv1.SendPacketRequest) error { return errors.New("lost reply") }
	r := &questsv1.RewardRequest{SessionId: "guard-body", NpcGuid: guid, QuestId: 37, OfferRevision: 2, NoChoice: true}
	if _, err := s.ClaimReward(context.Background(), r); err == nil {
		t.Fatal("lost response hidden")
	}
	if _, err := s.ClaimReward(context.Background(), r); err == nil || len(f.packets) != 1 {
		t.Fatal("ambiguous reward repeated", err)
	}
}

func TestLostGuardClientEstimateRequiresCurrentRawPoseAnchor(t *testing.T) {
	for _, valid := range []bool{true, false} {
		s, f, guid := guardObjectRig(55)
		f.actions.Position.Estimated = true
		f.actions.Position.X = 100
		f.world.QuestObjects[0].X = 101
		f.world.QuestObjects[0].Flags = 4 // Actual INTERACT_COND is not LOCKED.
		f.world.DiscoveryAnchor = &worldv1.DiscoveryAnchor{SelfGuid: 1, MapId: 0, ServerPositionRevision: 1, Estimated: true, X: 100}
		if !valid {
			f.world.Self.PositionRevision++
		}
		f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
			packet(t, s, opcode.SMSGQuestgiverQuestList, objectList(t, guid, 45))
			return nil
		}
		out, err := s.Offer(context.Background(), &questsv1.OfferRequest{SessionId: "guard-body", NpcGuid: guid})
		if valid {
			if err != nil || out.State.Offer == nil {
				t.Fatal(out, err)
			}
		} else if err == nil || len(f.packets) != 0 {
			t.Fatal("corrected raw pose reused old client estimate", out, err)
		}
	}
}
