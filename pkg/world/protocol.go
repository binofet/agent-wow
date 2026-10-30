package world

import (
	"encoding/binary"
	"errors"
	"time"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// The core handles connection lifecycle and server-commanded movement ACKs.
// Gameplay payloads are still routed unchanged to registered modules.
func (e *engine) handle(op uint16, body []byte) (bool, error) {
	switch op {
	case opcode.SMSGLoginVerifyWorld, opcode.SMSGTransferPending, opcode.SMSGNewWorld, opcode.MSGMoveTeleportAck:
		e.movement.observed(op, body, e.s.state.Character.GUID, e.timestamp())
	}
	switch op {
	case opcode.SMSGLoginVerifyWorld:
		if len(body) != 20 {
			return false, errors.New("invalid login verification length")
		}
		e.verified = true
		e.checkReady()
	case opcode.SMSGTimeSyncReq:
		if len(body) != 4 {
			return false, errors.New("invalid time-sync request")
		}
		response := binary.LittleEndian.AppendUint32(nil, binary.LittleEndian.Uint32(body))
		response = binary.LittleEndian.AppendUint32(response, e.timestamp())
		if err := e.write(opcode.CMSGTimeSyncResp, response); err != nil {
			return false, err
		}
		e.synced = true
		e.checkReady()
	case opcode.MSGMoveTeleportAck, opcode.SMSGNewWorld:
		if replyOp, reply, valid := teleportAcknowledgement(op, body, e.s.state.Character.GUID, e.timestamp()); valid {
			return false, e.write(replyOp, reply)
		}
	case opcode.SMSGForceMoveRoot, opcode.SMSGForceMoveUnroot:
		if replyOp, reply, info, valid := e.movement.acknowledgement(op, body, e.s.state.Character.GUID, e.timestamp()); valid {
			if err := e.write(replyOp, reply); err != nil {
				return false, err
			}
			e.movement.info = info
		}
	case opcode.SMSGPong:
		if len(body) != 4 {
			return false, errors.New("invalid pong")
		}
		sequence := binary.LittleEndian.Uint32(body)
		if sent, ok := e.pings[sequence]; ok {
			if time.Since(sent) >= e.timing.pong {
				delete(e.pings, sequence)
				return false, nil
			}
			e.latency = uint32(time.Since(sent) / time.Millisecond)
			for id, at := range e.pings {
				if !at.After(sent) {
					delete(e.pings, id)
				}
			}
			e.pongTimer.Reset(e.timing.pong)
		}
	case opcode.SMSGCharacterLoginFailed:
		if len(body) != 1 {
			return false, errors.New("invalid login rejection")
		}
		return false, &LoginError{Code: body[0]}
	case opcode.SMSGLogoutResponse:
		if len(body) != 5 || body[4] > 1 {
			return false, errors.New("invalid logout response")
		}
		reason := binary.LittleEndian.Uint32(body)
		if reason != 0 && e.logout != nil {
			op := e.logout
			e.logout = nil
			e.logoutTimer.Stop()
			e.logoutDeadline = nil
			e.s.modules.Resume()
			e.publish()
			op.err = &LogoutError{Reason: reason}
			close(op.done)
		}
	case opcode.SMSGLogoutComplete:
		if len(body) != 0 {
			return false, errors.New("invalid logout completion")
		}
		return true, nil
	case opcode.SMSGWardenData:
		reply, err := e.s.conn.WardenResponse(body)
		if err != nil {
			return false, err
		}
		if reply != nil {
			return false, e.write(opcode.CMSGWardenData, reply)
		}
	}
	return false, nil
}
