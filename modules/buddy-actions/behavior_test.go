package buddyactions

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/protobuf/types/known/emptypb"
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
