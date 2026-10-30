package buddyquests

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"

	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func sharedAlternate(reward bool, flags uint32) ([]byte, uint32, int, int) {
	p := requirementsFixture(false)
	op := uint32(opcode.SMSGQuestgiverRequestItems)
	if reward {
		p, op = rewardFixture(true), opcode.SMSGQuestgiverOfferReward
	}
	binary.LittleEndian.PutUint64(p, 3)
	binary.LittleEndian.PutUint32(p[8:], 7)
	r := reader{data: p[12:]}
	r.text(512)
	r.text(16384)
	at := len(p) - len(r.data)
	active, flagAt := at+8, at+12
	if reward {
		active, flagAt = at, at+1
		p[active] = 1
	} else {
		binary.LittleEndian.PutUint32(p[active:], 1)
	}
	binary.LittleEndian.PutUint32(p[flagAt:], flags)
	return p, op, active, flagAt
}

func TestSharedAutocompleteStrictFullFramesAndNpcIsolation(t *testing.T) {
	for _, reward := range []bool{false, true} {
		for _, flags := range []uint32{8, 8 | 0x200, 8 | 0x10000} {
			// Flags8 alone is the legitimate Method0 case. HIDDEN_REWARDS
			// does not change either of these complete producer layouts.
			p, _, active, flagAt := sharedAlternate(reward, flags)
			o, err := decodeSharedAutocomplete(p, reward)
			if err != nil || o.SharerGuid != 3 || o.Kind != questsv1.SharedQuestKind_SHARED_AUTOCOMPLETE || o.Quest.QuestId != 7 || o.Quest.Title != "A Threat Within" || o.Quest.Flags != flags || !o.Quest.Available || o.Quest.Details != "" || o.Quest.Objectives != "" {
				t.Fatal(reward, o, err)
			}
			if _, err := decodeTurnIn(p, reward); err == nil {
				t.Fatal("player share became NPC reward offer")
			}
			for n := range len(p) {
				if _, err := decodeSharedAutocomplete(p[:n], reward); err == nil {
					t.Fatal("accepted alternate prefix", reward, n)
				}
			}
			for _, mode := range []string{"foreign", "recipient", "creature", "unsharable", "inactive", "tail"} {
				q := append([]byte(nil), p...)
				switch mode {
				case "foreign":
					binary.LittleEndian.PutUint64(q, 4)
				case "recipient":
					binary.LittleEndian.PutUint64(q, 1)
				case "creature":
					binary.LittleEndian.PutUint64(q, npcGUID)
				case "unsharable":
					binary.LittleEndian.PutUint32(q[flagAt:], flags&^8)
				case "inactive":
					q[active] = 0
				case "tail":
					q = append(q, 0)
				}
				if _, err := decodeSharedAutocomplete(q, reward); err == nil {
					t.Fatal("accepted unsupported alternate identity/frame", reward, mode)
				}
			}
		}
	}
}

func TestSharedAutocompleteAcceptsThroughOwnLogOnlyAndNeverRemoteRewards(t *testing.T) {
	for _, reward := range []bool{false, true} {
		s, f, r := sharedRig(t, false)
		p, op, _, _ := sharedAlternate(reward, 8)
		packet(t, s, op, p)
		r.ShareSequence = snapshot(t, s).PendingShare.Sequence
		f.onSend = func(context.Context, *modv1.SendPacketRequest) error { f.confirm(7); return nil }
		out, err := s.AcceptShared(context.Background(), r)
		if err != nil || !out.TransportWritten || !out.ServerResponseReceived || out.State.LastSharedAcceptance.Status != questsv1.AcceptanceStatus_CONFIRMED_IN_LOG || out.State.LastSharedAcceptance.Kind != questsv1.SharedQuestKind_SHARED_AUTOCOMPLETE || out.State.LastSharedAcceptance.BaselineLogRevision != 1 || out.State.LastSharedAcceptance.ConfirmedLogRevision != 2 || out.State.Offer != nil || out.State.TurnInOffer != nil || out.State.LastReward != nil {
			t.Fatal(out, err)
		}
		if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGQuestgiverAcceptQuest || hex.EncodeToString(f.packets[0].Payload) != "03000000000000000700000000000000" || f.acquired != 0 || f.released != 0 || !f.actions.Moving || !f.actions.CastPending {
			t.Fatal("alternate share used a reward/interaction path", f.packets)
		}
		if _, err := s.AcceptShared(context.Background(), r); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 1 {
			t.Fatal("alternate sequence replayed", err)
		}
	}
}

func TestSharedAutocompleteRetiresOnlyLocalOfferAndCannotReplayAcrossKinds(t *testing.T) {
	for _, mode := range []string{"already", "expired", "full", "ambiguous_prior"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r := sharedRig(t, false)
			want, before := questsv1.AcceptanceStatus_ALREADY_IN_LOG, 0
			if mode == "ambiguous_prior" {
				f.onSend = func(context.Context, *modv1.SendPacketRequest) error {
					return status.Error(codes.Unavailable, "unknown")
				}
				s.AcceptShared(context.Background(), r)
				f.onSend = nil
				want, before = questsv1.AcceptanceStatus_SHARE_ALREADY_REQUESTED, 1
			}
			p, op, _, _ := sharedAlternate(false, 8)
			packet(t, s, op, p)
			r.ShareSequence = snapshot(t, s).PendingShare.Sequence
			switch mode {
			case "already":
				f.confirm(7)
			case "expired":
				s.shares.until = s.now().Add(-time.Second)
				want = questsv1.AcceptanceStatus_SHARE_EXPIRED
			case "full":
				for i, q := range f.world.SelfQuestLog {
					q.QuestId = uint32(100 + i)
				}
				f.world.SelfQuestLogRevision++
				want = questsv1.AcceptanceStatus_SHARE_LOG_FULL
			}
			out, err := s.AcceptShared(context.Background(), r)
			if err != nil || out.TransportWritten || out.ServerResponseReceived || out.State.LastSharedAcceptance.Status != want || !out.State.PendingShare.AcceptRequested || len(f.packets) != before {
				t.Fatal("alternate local retirement sent divider cleanup or acceptance", out, err, f.packets)
			}
			f.world.SelfQuestLogRevision++
			if snapshot(t, s).LastSharedAcceptance.Status != want {
				t.Fatal("local retirement claimed a new acceptance")
			}
			if mode == "expired" || mode == "full" {
				packet(t, s, op, p)
				fresh := snapshot(t, s).PendingShare
				if fresh.Sequence == r.ShareSequence || fresh.AcceptRequested || len(f.packets) != before {
					t.Fatal("new real share could not replace definite local retirement", fresh)
				}
			}
		})
	}
}

func TestSharedAutocompleteAmbiguousWriteCannotRetryThroughDetails(t *testing.T) {
	s, f, r := sharedRig(t, false)
	p, op, _, _ := sharedAlternate(false, 8)
	packet(t, s, op, p)
	r.ShareSequence = snapshot(t, s).PendingShare.Sequence
	f.onSend = func(context.Context, *modv1.SendPacketRequest) error {
		return status.Error(codes.Unavailable, "unknown")
	}
	if _, err := s.AcceptShared(context.Background(), r); err == nil || snapshot(t, s).LastSharedAcceptance.Status != questsv1.AcceptanceStatus_WRITE_UNCONFIRMED {
		t.Fatal("unconfirmed alternate write claimed acceptance", err)
	}
	packet(t, s, opcode.SMSGQuestgiverQuestDetails, sharedDetails(t))
	r.ShareSequence = snapshot(t, s).PendingShare.Sequence
	f.onSend = nil
	out, err := s.AcceptShared(context.Background(), r)
	if err != nil || out.State.LastSharedAcceptance.Status != questsv1.AcceptanceStatus_SHARE_ALREADY_REQUESTED || len(f.packets) != 2 || f.packets[1].Opcode != opcode.MSGQuestPushResult {
		t.Fatal("ambiguous alternate accepted again as details", out, err, f.packets)
	}
}

func TestSharedAutocompleteRespectsPartyBodyAndIndependentMode(t *testing.T) {
	for _, mode := range []string{"party", "body", "source_changed", "independent"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r := sharedRig(t, false)
			if mode == "independent" {
				s = NewIndependentService(f, "body-one", 1)
			}
			p, op, _, _ := sharedAlternate(true, 8)
			packet(t, s, op, p)
			if mode == "independent" {
				if snapshot(t, s).PendingShare != nil {
					t.Fatal("independent mode cached owner alternate share")
				}
			} else {
				r.ShareSequence = snapshot(t, s).PendingShare.Sequence
			}
			switch mode {
			case "party":
				f.party.Group.InGroup = false
			case "body":
				r.SessionId = "replaced"
			case "source_changed":
				f.onParty = func(n int) {
					if n == 2 {
						packet(t, s, opcode.SMSGQuestConfirmAccept, sharedConfirm(8, 3))
					}
				}
			}
			if _, err := s.AcceptShared(context.Background(), r); err == nil || len(f.packets) != 0 {
				t.Fatal("unproven alternate share wrote", err, f.packets)
			}
		})
	}
}
