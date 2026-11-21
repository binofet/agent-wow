package buddyactions

import (
	"context"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestExactResurrectionNeverChoosesAnOmittedOrDifferentLearnedRank(t *testing.T) {
	s, f, _ := resRig(t)
	learnSpells(s, 2006, 2010)
	request := &actionsv1.ResurrectOnceRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: 3}
	if _, err := s.ResurrectOnce(context.Background(), request); status.Code(err) != codes.InvalidArgument || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("native chose a rank for omitted input", err, f.packets)
	}
	request.SpellId = 10880
	if _, err := s.ResurrectOnce(context.Background(), request); status.Code(err) != codes.FailedPrecondition || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("native substituted a different learned rank", err, f.packets)
	}
	request.SpellId = 2006
	if _, err := s.ResurrectOnce(context.Background(), request); err != nil || lastCast(f) != 2006 || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("lower exact caller rank was replaced", err, f.packets)
	}
}
