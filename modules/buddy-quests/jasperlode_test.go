package buddyquests

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func jasperlodeRig() (*Service, *fakeSession, *questsv1.ExploreRequest) {
	s, f, r := explorationFixture()
	spec, _ := fixedExploration(76)
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = float32(spec.x), float32(spec.y), float32(spec.z)
	f.actions.Position.X, f.actions.Position.Y, f.actions.Position.Z = f.world.Self.X, f.world.Self.Y, f.world.Self.Z
	f.world.SelfQuestLog[0].QuestId = 76
	r.QuestId, r.RequestId = 76, "jasperlode_1"
	return s, f, r
}
func TestJasperlodeExactNormalTrigger87AndIndividualNewerCompletion(t *testing.T) {
	s, f, r := jasperlodeRig()
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode != 0xb4 || len(p.Payload) != 4 || binary.LittleEndian.Uint32(p.Payload) != 87 {
			t.Fatal("not fixed trigger87", p)
		}
		f.mu.Lock()
		f.world.SelfQuestLog[0].Complete = true
		f.world.SelfQuestLogRevision++
		f.mu.Unlock()
		return nil
	}
	out, err := s.Explore(context.Background(), r)
	if err != nil || !out.ServerResponseReceived || out.State.LastExploration.QuestId != 76 || out.State.LastExploration.TriggerId != 87 || out.State.LastExploration.Status != questsv1.ExplorationStatus_EXPLORATION_CONFIRMED || !out.State.LastExploration.LeaseReleased {
		t.Fatal(out, err)
	}
	if _, err = s.Explore(context.Background(), r); err != nil || len(f.packets) != 1 {
		t.Fatal("same request repeated", err)
	}
	r.RequestId = "new_id"
	if _, err = s.Explore(context.Background(), r); status.Code(err) != codes.AlreadyExists || len(f.packets) != 1 {
		t.Fatal("new ID repeated trigger", err)
	}
}
func TestJasperlodeRadiusMapQuestAndLateCompletionAreBound(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*fakeSession, *questsv1.ExploreRequest)
	}{
		{"outside29", func(f *fakeSession, _ *questsv1.ExploreRequest) { f.actions.Position.X += 29.1 }},
		{"wrong trigger location", func(f *fakeSession, _ *questsv1.ExploreRequest) {
			f.actions.Position.X = scoutX
			f.actions.Position.Y = scoutY
			f.actions.Position.Z = scoutZ
		}},
		{"other map", func(f *fakeSession, _ *questsv1.ExploreRequest) { f.world.Self.MapId = 1; f.actions.Position.MapId = 1 }},
		{"other own quest", func(f *fakeSession, _ *questsv1.ExploreRequest) { f.world.SelfQuestLog[0].QuestId = 62 }},
		{"revision", func(_ *fakeSession, r *questsv1.ExploreRequest) { r.QuestLogRevision++ }},
		{"failed", func(f *fakeSession, _ *questsv1.ExploreRequest) { f.world.SelfQuestLog[0].Failed = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, f, r := jasperlodeRig()
			test.mutate(f, r)
			if _, err := s.Explore(context.Background(), r); err == nil || len(f.packets) != 0 {
				t.Fatal("invalid scene wrote", err)
			}
		})
	}
	s, f, r := jasperlodeRig()
	f.onSend = func(context.Context, *modv1.SendPacketRequest) error { return errors.New("uncertain") }
	out, err := s.Explore(context.Background(), r)
	if err != nil || out.State.LastExploration.Status != questsv1.ExplorationStatus_EXPLORATION_WRITE_UNCONFIRMED {
		t.Fatal(out, err)
	}
	f.mu.Lock()
	f.world.SelfQuestLog[1].QuestId = 62
	f.world.SelfQuestLog[1].Complete = true
	f.world.SelfQuestLogRevision++
	f.mu.Unlock()
	if got := snapshot(t, s).LastExploration; got.Status == questsv1.ExplorationStatus_EXPLORATION_CONFIRMED {
		t.Fatal("other quest proved Jasperlode", got)
	}
	f.mu.Lock()
	f.world.SelfQuestLog[0].Complete = true
	f.world.SelfQuestLogRevision++
	f.mu.Unlock()
	if got := snapshot(t, s).LastExploration; got.Status != questsv1.ExplorationStatus_EXPLORATION_CONFIRMED || got.TransportWritten {
		t.Fatal("actual late own completion lost", got)
	}
}
func TestScoutAndJasperlodeEachReserveOnceWithoutCrossReceiptReplay(t *testing.T) {
	s, f, r62 := explorationFixture()
	f.world.SelfQuestLog[0].Complete = true
	first, err := s.Explore(context.Background(), r62)
	if err != nil || first.State.LastExploration.Status != questsv1.ExplorationStatus_EXPLORATION_ALREADY_COMPLETE {
		t.Fatal(first, err)
	}
	f.mu.Lock()
	f.world.SelfQuestLog[1].QuestId = 76
	f.world.SelfQuestLogRevision++
	spec, _ := fixedExploration(76)
	f.actions.Position.X, f.actions.Position.Y, f.actions.Position.Z = float32(spec.x), float32(spec.y), float32(spec.z)
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = f.actions.Position.X, f.actions.Position.Y, f.actions.Position.Z
	f.mu.Unlock()
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if binary.LittleEndian.Uint32(p.Payload) != 87 {
			t.Error("wrong second trigger")
		}
		f.mu.Lock()
		f.world.SelfQuestLog[1].Complete = true
		f.world.SelfQuestLogRevision++
		f.mu.Unlock()
		return nil
	}
	r76 := &questsv1.ExploreRequest{SessionId: r62.SessionId, RequestId: "jasper_second", QuestId: 76, QuestLogRevision: 2}
	second, err := s.Explore(context.Background(), r76)
	if err != nil || second.State.LastExploration.Status != questsv1.ExplorationStatus_EXPLORATION_CONFIRMED {
		t.Fatal(second, err)
	}
	replay, err := s.Explore(context.Background(), r62)
	if err != nil || replay.State.LastExploration.QuestId != 62 || replay.State.LastExploration.TriggerId != 88 || replay.State.LastExploration.Status != questsv1.ExplorationStatus_EXPLORATION_ALREADY_COMPLETE || len(f.packets) != 1 {
		t.Fatal("old quest got latest receipt", replay, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = s.Explore(ctx, &questsv1.ExploreRequest{SessionId: r62.SessionId, RequestId: "new62", QuestId: 62, QuestLogRevision: 3}); status.Code(err) != codes.AlreadyExists {
		t.Fatal(err)
	}
}
