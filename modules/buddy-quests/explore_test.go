package buddyquests

import (
	"context"
	"encoding/hex"
	"errors"
	"math"
	"testing"
	"time"

	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func explorationFixture() (*Service, *fakeSession, *questsv1.ExploreRequest) {
	f := fixture()
	f.world.QuestGivers = nil // Scouting is not an NPC interaction.
	p := f.world.Self
	p.PositionKnown, p.MovementFlagsKnown, p.UnitFlagsKnown = true, true, true
	p.X, p.Y, p.Z = scoutX, scoutY, scoutZ
	f.actions.Position.X, f.actions.Position.Y, f.actions.Position.Z = scoutX, scoutY, scoutZ
	f.world.SelfQuestLog[0].QuestId = 62
	f.onAcquire = func() { f.actions.InteractionLeaseRemainingMs = 15000 }
	return NewService(f, "scout-body", 1), f, &questsv1.ExploreRequest{SessionId: "scout-body", RequestId: "scout_1", QuestId: 62, QuestLogRevision: 1}
}

func TestExplorationNormalPacketRequiresActualNewerOwnLogAndReleases(t *testing.T) {
	s, f, r := explorationFixture()
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode != 0xb4 || hex.EncodeToString(p.Payload) != "58000000" {
			t.Errorf("wrong normal scout packet: %v", p)
		}
		f.mu.Lock()
		f.world.SelfQuestLog[0].Complete = true
		f.world.SelfQuestLogRevision++
		f.mu.Unlock()
		return nil
	}
	out, err := s.Explore(context.Background(), r)
	if err != nil || !out.TransportWritten || !out.ServerResponseReceived ||
		out.State.LastExploration.Status != questsv1.ExplorationStatus_EXPLORATION_CONFIRMED ||
		out.State.LastExploration.ConfirmedLogRevision != 2 || !out.State.LastExploration.LeaseReleased {
		t.Fatal(out, err)
	}
	if n, a, released, held := f.counters(); n != 1 || a != 1 || released != 1 || held {
		t.Fatal(n, a, released, held)
	}
	if _, err := s.Explore(context.Background(), r); err != nil {
		t.Fatal("exact replay should return same receipt", err)
	}
	r.RequestId = "different"
	if _, err := s.Explore(context.Background(), r); status.Code(err) != codes.AlreadyExists {
		t.Fatal("new request may not repeat trigger", err)
	}
	if len(f.packets) != 1 {
		t.Fatal("idempotent replay sent another packet")
	}
}

func TestExplorationStrictCharacterMapRadiusRevisionAndRequestGuards(t *testing.T) {
	cases := map[string]func(*fakeSession, *questsv1.ExploreRequest){
		"foreign session":             func(_ *fakeSession, r *questsv1.ExploreRequest) { r.SessionId = "another-body" },
		"unsupported quest":           func(_ *fakeSession, r *questsv1.ExploreRequest) { r.QuestId = 77 },
		"raw trigger impossible":      func(_ *fakeSession, r *questsv1.ExploreRequest) { r.RequestId = "bad\n" },
		"stale own log":               func(_ *fakeSession, r *questsv1.ExploreRequest) { r.QuestLogRevision++ },
		"unknown own log":             func(f *fakeSession, _ *questsv1.ExploreRequest) { f.world.SelfQuestLogKnown = false },
		"absent quest":                func(f *fakeSession, _ *questsv1.ExploreRequest) { f.world.SelfQuestLog[0].QuestId = 0 },
		"failed quest":                func(f *fakeSession, _ *questsv1.ExploreRequest) { f.world.SelfQuestLog[0].Failed = true },
		"foreign character":           func(f *fakeSession, _ *questsv1.ExploreRequest) { f.world.Self.Guid = 4 },
		"dead":                        func(f *fakeSession, _ *questsv1.ExploreRequest) { f.world.Self.Alive = false },
		"map mismatch":                func(f *fakeSession, _ *questsv1.ExploreRequest) { f.actions.Position.MapId = 1 },
		"other map":                   func(f *fakeSession, _ *questsv1.ExploreRequest) { f.world.Self.MapId = 1; f.actions.Position.MapId = 1 },
		"transfer":                    func(f *fakeSession, _ *questsv1.ExploreRequest) { f.world.Self.MapKnown = false },
		"outside conservative sphere": func(f *fakeSession, _ *questsv1.ExploreRequest) { f.actions.Position.X += 9.1 },
		"nonfinite":                   func(f *fakeSession, _ *questsv1.ExploreRequest) { f.actions.Position.X = float32(math.NaN()) },
		"combat":                      func(f *fakeSession, _ *questsv1.ExploreRequest) { f.world.Self.UnitFlags = 0x80000 },
		"unknown movement":            func(f *fakeSession, _ *questsv1.ExploreRequest) { f.world.Self.MovementFlagsKnown = false },
		"flying":                      func(f *fakeSession, _ *questsv1.ExploreRequest) { f.world.Self.MovementFlags = 0x1000000 },
		"motion unconfirmed":          func(f *fakeSession, _ *questsv1.ExploreRequest) { f.actions.MotionUnconfirmed = true },
		"cast pending":                func(f *fakeSession, _ *questsv1.ExploreRequest) { f.actions.CastPending = true },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s, f, r := explorationFixture()
			change(f, r)
			if _, err := s.Explore(context.Background(), r); err == nil {
				t.Fatal("unsafe scout request accepted")
			}
			if n, _, _, held := f.counters(); n != 0 || held {
				t.Fatal("unsafe request sent trigger or leaked lease", n, held)
			}
		})
	}
}

func TestExplorationRechecksAfterOwnedStopAndNeverReservesBeforeSafety(t *testing.T) {
	for _, mutate := range []func(*fakeSession){
		func(f *fakeSession) { f.actions.Position.X += 20 },
		func(f *fakeSession) { f.world.SelfQuestLogRevision++ },
		func(f *fakeSession) { f.actions.InteractionLeaseRemainingMs = 500 },
	} {
		s, f, r := explorationFixture()
		f.onAcquire = func() { f.actions.InteractionLeaseRemainingMs = 15000; mutate(f) }
		if _, err := s.Explore(context.Background(), r); err == nil || s.explorationRequest != nil {
			t.Fatal("changed scene consumed or sent trigger", err)
		}
		if n, _, released, held := f.counters(); n != 0 || released != 1 || held {
			t.Fatal(n, released, held)
		}
	}
}

func TestExplorationAmbiguousWriteNeverRetriesButLateActualProofResolves(t *testing.T) {
	s, f, r := explorationFixture()
	f.onSend = func(context.Context, *modv1.SendPacketRequest) error { return errors.New("lost write response") }
	out, err := s.Explore(context.Background(), r)
	if err != nil || out.State.LastExploration.Status != questsv1.ExplorationStatus_EXPLORATION_WRITE_UNCONFIRMED || !out.State.LastExploration.LeaseReleased {
		t.Fatal(out, err)
	}
	if _, err := s.Explore(context.Background(), r); err != nil || len(f.packets) != 1 {
		t.Fatal("ambiguous write replayed", err)
	}
	f.mu.Lock()
	f.world.SelfQuestLog[0].Complete = true
	f.world.SelfQuestLogRevision++
	f.mu.Unlock()
	if got := snapshot(t, s).LastExploration; got.Status != questsv1.ExplorationStatus_EXPLORATION_CONFIRMED || got.TransportWritten {
		t.Fatal("late normal log proof lost", got)
	}
}

func TestExplorationTransportAndUnchangedCompletionRevisionDoNotProveCredit(t *testing.T) {
	s, f, r := explorationFixture()
	f.onSend = func(context.Context, *modv1.SendPacketRequest) error {
		f.mu.Lock()
		f.world.SelfQuestLog[0].Complete = true // No newer actual revision.
		f.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	out, err := s.Explore(ctx, r)
	if err != nil || out.ServerResponseReceived || out.State.LastExploration.Status != questsv1.ExplorationStatus_EXPLORATION_UNCONFIRMED || !out.TransportWritten {
		t.Fatal(out, err)
	}
}

func TestExplorationAlreadyCompleteAndCanceledMakeNoGameWrite(t *testing.T) {
	s, f, r := explorationFixture()
	f.world.SelfQuestLog[0].Complete = true
	out, err := s.Explore(context.Background(), r)
	if err != nil || out.State.LastExploration.Status != questsv1.ExplorationStatus_EXPLORATION_ALREADY_COMPLETE || len(f.packets) != 0 || f.acquired != 0 {
		t.Fatal(out, err)
	}
	s, f, r = explorationFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Explore(ctx, r); status.Code(err) != codes.Canceled || len(f.packets) != 0 {
		t.Fatal(err)
	}
}

func TestExplorationPayloadCapturedAndConcurrentReplayDoesNotWrite(t *testing.T) {
	s, f, r := explorationFixture()
	entered, finish := make(chan struct{}), make(chan struct{})
	f.onSend = func(_ context.Context, packet *modv1.SendPacketRequest) error {
		if packet.Opcode != opcode.CMSGAreatrigger {
			t.Error("wrong opcode")
		}
		close(entered)
		<-finish
		return errors.New("ambiguous")
	}
	done := make(chan struct{})
	copyRequest := proto.Clone(r).(*questsv1.ExploreRequest)
	go func() { _, _ = s.Explore(context.Background(), copyRequest); close(done) }()
	<-entered
	if _, err := s.Explore(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	close(finish)
	<-done
	if n, _, released, held := f.counters(); n != 1 || released != 1 || held {
		t.Fatal(n, released, held)
	}
}
