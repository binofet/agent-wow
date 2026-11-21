package buddyactions

import (
	"context"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func sendClientControl(s *Service, guid uint64, allowed byte) {
	p := append(packedGUID(guid), allowed)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGClientControlUpdate, Payload: p})
}
