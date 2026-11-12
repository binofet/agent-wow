package buddychat

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	chatv1 "github.com/agent-wow/agent-wow/modules/buddy-chat/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const teamPrefix = "WBTEAM1\t"
const managementCapacity = 64
const managementTTL = 30 * time.Second

type managementEntry struct {
	request *chatv1.ManagementRequest
	at      time.Time
}
type managementState struct {
	requests [managementCapacity]managementEntry
	latest   uint64
	nonce    string
	lastSeq  uint32
	// Never evict a retired nonce and reopen replay authority in this epoch.
	retired                      map[string]bool
	received, ignored, malformed uint64
	statusSeq                    uint32
	statusAt                     map[uint64]time.Time
}

func teamSubject(guid uint64) bool { return guid == 1 || guid == 4 || guid == 5 || guid == 8 }
func teamNonce(v string) bool {
	if len(v) != 12 {
		return false
	}
	for _, b := range []byte(v) {
		if !(b >= '0' && b <= '9') && !(b >= 'a' && b <= 'f') {
			return false
		}
	}
	return true
}
func teamEpoch(v string) bool {
	if len(v) < 12 || len(v) > 64 {
		return false
	}
	for _, b := range []byte(v) {
		if !(b >= 'a' && b <= 'z') && !(b >= 'A' && b <= 'Z') && !(b >= '0' && b <= '9') {
			return false
		}
	}
	return true
}

type teamWireRequest struct {
	epoch, nonce string
	sequence     uint32
	subject      uint64
	action       chatv1.TeamAction
}

// Normal SMSG_MESSAGECHAT PARTY/ADDON framing, with the authoritative sender
// GUID supplied by the server. Owner addon requests are independent of the
// quest/gear feeds and conversational ring. No message text grants authority.
func decodeTeamRequest(p []byte) (*teamWireRequest, bool, error) {
	if len(p) < 29+len(teamPrefix) || p[0] != byte(chatv1.Channel_PARTY) || binary.LittleEndian.Uint32(p[1:5]) != 0xffffffff || binary.LittleEndian.Uint64(p[5:13]) != 3 || !bytes.HasPrefix(p[29:], []byte(teamPrefix)) {
		return nil, false, nil
	}
	bad := errors.New("invalid owner team request")
	n := binary.LittleEndian.Uint32(p[25:29])
	if binary.LittleEndian.Uint32(p[13:17]) != 0 || binary.LittleEndian.Uint64(p[17:25]) != 0 || n < uint32(len(teamPrefix)+2) || n > maxTextBytes+1 || len(p) != 29+int(n)+1 || p[29+int(n)-1] != 0 {
		return nil, true, bad
	}
	body := p[29+len(teamPrefix) : 29+int(n)-1]
	for _, b := range body {
		if b < 0x20 || b > 0x7e {
			return nil, true, bad
		}
	}
	f := strings.Split(string(body), "|")
	if len(f) != 6 || f[0] != "R" || !teamEpoch(f[1]) || !teamNonce(f[2]) {
		return nil, true, bad
	}
	seq, err := ownerFeedNumber(f[3], 1, 0x7fffffff)
	subject, e2 := ownerFeedNumber(f[4], 1, 8)
	if err != nil || e2 != nil || !teamSubject(uint64(subject)) {
		return nil, true, bad
	}
	action := chatv1.TeamAction_TEAM_ACTION_UNSPECIFIED
	switch f[5] {
	case "RESET":
		action = chatv1.TeamAction_RESET
	case "UNSTUCK":
		action = chatv1.TeamAction_UNSTUCK
	default:
		return nil, true, bad
	}
	return &teamWireRequest{f[1], f[2], seq, uint64(subject), action}, true, nil
}

func (s *Service) ingestTeamRequestLocked(payload []byte) bool {
	r, handled, err := decodeTeamRequest(payload)
	if !handled {
		return false
	}
	f := &s.team
	f.received++
	if err != nil {
		f.malformed++
		return true
	}
	if s.independent || !teamSubject(s.selfGUID) || r.epoch != s.sessionID || f.retired[r.nonce] || r.nonce == f.nonce && r.sequence <= f.lastSeq {
		f.ignored++
		return true
	}
	if r.nonce != f.nonce {
		if f.nonce != "" {
			if len(f.retired) >= 16 {
				f.ignored++
				return true
			}
			if f.retired == nil {
				f.retired = map[string]bool{}
			}
			f.retired[f.nonce] = true
		}
		f.nonce, f.lastSeq = r.nonce, 0
	}
	f.lastSeq = r.sequence
	f.latest++
	row := &chatv1.ManagementRequest{Sequence: f.latest, RequestId: r.nonce + "." + strconv.FormatUint(uint64(r.sequence), 10), AddonSession: r.nonce, RequestSequence: r.sequence, SubjectGuid: r.subject, Action: r.action}
	f.requests[(f.latest-1)%managementCapacity] = managementEntry{row, s.now()}
	return true
}

func (s *Service) ManagementRequests(ctx context.Context, r *chatv1.ManagementRequestsRequest) (*chatv1.ManagementRequestsResponse, error) {
	if r == nil || r.Limit > managementCapacity {
		return nil, status.Error(codes.InvalidArgument, "management limit must be at most64")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.SessionId != "" && r.SessionId != s.sessionID {
		return nil, status.Error(codes.FailedPrecondition, "chat module epoch changed")
	}
	f := &s.team
	if r.AfterSequence > f.latest {
		return nil, status.Error(codes.InvalidArgument, "management cursor is newer than this epoch")
	}
	out := &chatv1.ManagementRequestsResponse{SessionId: s.sessionID, SelfGuid: s.selfGUID, NextSequence: r.AfterSequence, LatestSequence: f.latest, ReceivedPackets: f.received, IgnoredPackets: f.ignored, MalformedPackets: f.malformed}
	if f.latest == 0 {
		return out, nil
	}
	limit := r.Limit
	if limit == 0 {
		limit = 32
	}
	oldest := uint64(1)
	if f.latest > managementCapacity {
		oldest = f.latest - managementCapacity + 1
	}
	out.OldestSequence = oldest
	start := r.AfterSequence + 1
	if start < oldest {
		out.CursorLost = true
		start = oldest
	}
	now := s.now()
	for i := start; i <= f.latest && len(out.Requests) < int(limit); i++ {
		e := f.requests[(i-1)%managementCapacity]
		row := proto.Clone(e.request).(*chatv1.ManagementRequest)
		if !now.Before(e.at) {
			row.AgeMs = uint64(now.Sub(e.at).Milliseconds())
			row.Available = now.Sub(e.at) < managementTTL
		}
		out.Requests = append(out.Requests, row)
		out.NextSequence = i
	}
	return out, nil
}

func teamPhase(phase chatv1.TeamPhase) (string, bool) {
	name, ok := chatv1.TeamPhase_name[int32(phase)]
	return name, ok && phase != chatv1.TeamPhase_TEAM_PHASE_UNSPECIFIED
}

// Explicit host publication only. This reports a structured host observation;
// it cannot run a lifecycle operation, send arbitrary text, or confirm delivery.
func (s *Service) PublishTeamStatus(ctx context.Context, r *chatv1.TeamStatusRequest) (*chatv1.TeamStatusResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "team status is required")
	}
	r = proto.Clone(r).(*chatv1.TeamStatusRequest)
	phase, valid := teamPhase(r.Phase)
	if !valid || !teamSubject(r.SubjectGuid) || r.ObservationAgeMs > 5000 || r.RequestSequence > 0x7fffffff || (r.RequestSequence == 0) != (r.RequestAddonSession == "") || r.RequestAddonSession != "" && !teamNonce(r.RequestAddonSession) {
		return nil, status.Error(codes.InvalidArgument, "fixed subject phase fresh observation and optional exact request reference required")
	}
	if r.Phase == chatv1.TeamPhase_COMPLETE && r.RequestSequence == 0 {
		return nil, status.Error(codes.InvalidArgument, "request completion requires an exact request reference")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	if s.independent || !teamSubject(s.selfGUID) || !teamEpoch(s.sessionID) || r.SessionId != s.sessionID || s.session == nil {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "current companion chat epoch and session callback required")
	}
	f := &s.team
	now := s.now()
	if at, ok := f.statusAt[r.SubjectGuid]; ok && (now.Before(at) || now.Sub(at) < 250*time.Millisecond) {
		s.mu.Unlock()
		return nil, status.Error(codes.ResourceExhausted, "team status subject publication rate exceeded")
	}
	if f.statusSeq == 0x7fffffff {
		s.mu.Unlock()
		return nil, status.Error(codes.ResourceExhausted, "finite status sequence exhausted")
	}
	f.statusSeq++
	sequence := f.statusSeq
	if f.statusAt == nil {
		f.statusAt = map[uint64]time.Time{}
	}
	f.statusAt[r.SubjectGuid] = now
	epoch := s.sessionID
	s.mu.Unlock()
	nonce := r.RequestAddonSession
	if nonce == "" {
		nonce = "-"
	}
	ttl := uint32(15) - (r.ObservationAgeMs+999)/1000
	text := fmt.Sprintf("%sS|%s|%d|%d|%s|%s|%d|%d", teamPrefix, epoch, sequence, r.SubjectGuid, phase, nonce, r.RequestSequence, ttl)
	if len(text) > maxTextBytes {
		return nil, status.Error(codes.Internal, "bounded team status exceeded wire limit")
	}
	payload := make([]byte, 8+len(text)+1)
	binary.LittleEndian.PutUint32(payload, uint32(chatv1.Channel_PARTY))
	binary.LittleEndian.PutUint32(payload[4:], 0xffffffff)
	copy(payload[8:], text)
	if _, err := s.session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: opcode.CMSGMessagechat, Payload: payload}); err != nil {
		return nil, err
	}
	return &chatv1.TeamStatusResponse{TransportWritten: true, Sequence: sequence, TtlSeconds: ttl, SessionId: epoch, SubjectGuid: r.SubjectGuid, Phase: r.Phase}, nil
}
