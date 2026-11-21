package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
	"testing"
)

type ambiguousDefensiveCastSession struct{ *fakeSession }

func (f *ambiguousDefensiveCastSession) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	result, err := f.fakeSession.SendPacket(ctx, r, opts...)
	if err == nil && r.Opcode == opcode.CMSGCastSpell {
		return nil, errors.New("ambiguous defensive cast transport")
	}
	return result, err
}

func rejectDefensiveCast(t *testing.T, s *Service, op uint32) {
	t.Helper()
	if !s.state.CastPending || s.pendingTarget == 0 {
		t.Fatal("no actual defensive cast to reject", s.stateLocked())
	}
	target := s.pendingTarget
	s.OnPacket(context.Background(), healingRejectionPacket(op, s.selfGUID, s.castCount, s.pendingSpell))
	if s.state.CastPending || !s.state.Assisting || s.failedAssistTarget != target || s.failedAssistCastTarget != target {
		t.Fatal("matching rejection did not settle only its defensive cast", s.stateLocked())
	}
}

func acknowledgeDefensiveAttackStop(s *Service, target uint64) {
	p := append(packedGUID(s.selfGUID), packedGUID(target)...)
	p = binary.LittleEndian.AppendUint32(p, 0)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAttackstop, Payload: p})
}
