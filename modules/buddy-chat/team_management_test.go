package buddychat

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	chatv1 "github.com/agent-wow/agent-wow/modules/buddy-chat/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type teamSender struct{ *sessionRecorder }

func (s teamSender) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return s.sessionRecorder.SendPacket(ctx, r)
}

func teamPacket(body string) []byte {
	p := makePlayerChat(chatv1.Channel_PARTY, 3, 0, teamPrefix+body)
	binary.LittleEndian.PutUint32(p[1:], 0xffffffff)
	return p
}
func teamBody(s *Service, nonce string, seq uint32, subject uint64, action string) string {
	return fmt.Sprintf("R|%s|%s|%d|%d|%s", s.sessionID, nonce, seq, subject, action)
}
func ingestTeam(t *testing.T, s *Service, body string) {
	t.Helper()
	if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGMessagechat, Payload: teamPacket(body)}); err != nil {
		t.Fatal(err)
	}
}
func management(t *testing.T, s *Service) *chatv1.ManagementRequestsResponse {
	t.Helper()
	r, err := s.ManagementRequests(context.Background(), &chatv1.ManagementRequestsRequest{Limit: 64})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTeamManagementLiteralLuaWireAndGeneratedRPC(t *testing.T) {
	recorder := &sessionRecorder{}
	callback := connectTestGRPC(t, func(server *grpc.Server) { modv1.RegisterSessionServer(server, recorder) })
	s := NewService(modv1.NewSessionClient(callback), 1)
	s.sessionID = "ABCDEF123456"
	connection := connectTestGRPC(t, func(server *grpc.Server) { chatv1.RegisterBuddyChatServer(server, s) })
	client := chatv1.NewBuddyChatClient(connection)
	// Independent PARTY/ADDON literal: Lua prefix WBTEAM1 + tab + six fields.
	wire, _ := hex.DecodeString("02ffffffff03000000000000000000000000000000000000002e00000057425445414d3109527c4142434445463132333435367c6131623263336434653566367c317c357c52455345540000")
	if _, err := client.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGMessagechat, Payload: wire}); err != nil {
		t.Fatal(err)
	}
	r, err := client.ManagementRequests(context.Background(), &chatv1.ManagementRequestsRequest{SessionId: s.sessionID})
	if err != nil || r.SelfGuid != 1 || len(r.Requests) != 1 || r.Requests[0].SubjectGuid != 5 || r.Requests[0].RequestId != "a1b2c3d4e5f6.1" || r.Requests[0].Action != chatv1.TeamAction_RESET || !r.Requests[0].Available || len(recorder.packets) != 0 {
		t.Fatal(r, err)
	}
	// Subject5 need not be this receiving character: the host can hear an
	// offline peer request through any actual online fixed companion.
	out, err := client.PublishTeamStatus(context.Background(), &chatv1.TeamStatusRequest{SessionId: s.sessionID, SubjectGuid: 5, Phase: chatv1.TeamPhase_MANAGING_SKILLS})
	if err != nil || !out.TransportWritten || out.Sequence != 1 || out.TtlSeconds != 15 || len(recorder.packets) != 1 {
		t.Fatal(out, err)
	}
	const want = "02000000ffffffff57425445414d3109537c4142434445463132333435367c317c357c4d414e4147494e475f534b494c4c537c2d7c307c313500"
	if recorder.packets[0].Opcode != opcode.CMSGMessagechat || hex.EncodeToString(recorder.packets[0].Payload) != want {
		t.Fatal("Lua status wire mismatch", recorder.packets[0])
	}
	chat, _ := s.Poll(context.Background(), &chatv1.PollRequest{})
	owner, _ := s.OwnerFeed(context.Background(), &emptypb.Empty{})
	if chat.LatestSequence != 0 || owner.Available || owner.OwnerGear != nil {
		t.Fatal("team request polluted another feed", chat, owner)
	}
}

func TestTeamManagementOwnerHeaderMalformedAndBodyEpoch(t *testing.T) {
	for _, mode := range []string{"peer", "self", "npc", "say", "party_leader", "language", "flags", "receiver", "length", "nul", "trailing", "prefix", "subject", "action", "seq_zero", "leading_zero", "overflow", "nonce", "epoch", "control"} {
		t.Run(mode, func(t *testing.T) {
			s := NewService(nil, 1)
			body := teamBody(s, "a1b2c3d4e5f6", 1, 8, "UNSTUCK")
			switch mode {
			case "subject":
				body = teamBody(s, "a1b2c3d4e5f6", 1, 3, "RESET")
			case "action":
				body = strings.Replace(body, "UNSTUCK", ".tele", 1)
			case "seq_zero":
				body = teamBody(s, "a1b2c3d4e5f6", 0, 8, "RESET")
			case "leading_zero":
				body = strings.Replace(body, "|1|8|", "|01|8|", 1)
			case "overflow":
				body = teamBody(s, "a1b2c3d4e5f6", 0x80000000, 8, "RESET")
			case "nonce":
				body = teamBody(s, "A1B2C3D4E5F6", 1, 8, "RESET")
			case "epoch":
				body = strings.Replace(body, s.sessionID, "OLD_BODY_EPOCH", 1)
			case "control":
				body += "\n"
			}
			p := teamPacket(body)
			switch mode {
			case "peer":
				binary.LittleEndian.PutUint64(p[5:], 4)
			case "self":
				binary.LittleEndian.PutUint64(p[5:], 1)
			case "npc":
				binary.LittleEndian.PutUint64(p[5:], 0xf130000001000001)
			case "say":
				p[0] = 1
			case "party_leader":
				p[0] = 51
			case "language":
				binary.LittleEndian.PutUint32(p[1:], 7)
			case "flags":
				binary.LittleEndian.PutUint32(p[13:], 1)
			case "receiver":
				binary.LittleEndian.PutUint64(p[17:], 1)
			case "length":
				binary.LittleEndian.PutUint32(p[25:], 0xffffffff)
			case "nul":
				p[len(p)-2] = 'x'
			case "trailing":
				p = append(p, 0)
			case "prefix":
				p[29] = 'X'
			}
			s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGMessagechat, Payload: p})
			if got := management(t, s); len(got.Requests) != 0 {
				t.Fatal("untrusted request admitted", got)
			}
		})
	}
	valid := NewService(nil, 1)
	p := teamPacket(teamBody(valid, "a1b2c3d4e5f6", 1, 1, "RESET"))
	for end := 0; end < len(p); end++ {
		s := NewService(nil, 1)
		s.sessionID = valid.sessionID
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGMessagechat, Payload: p[:end]})
		if len(management(t, s).Requests) != 0 {
			t.Fatal("truncated request admitted", end)
		}
	}
	// A valid newer module does not inherit an old relay's request authority.
	newBody := NewService(nil, 1)
	newBody.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGMessagechat, Payload: p})
	if len(management(t, newBody).Requests) != 0 {
		t.Fatal("old relay epoch replayed on new body")
	}
	if _, err := newBody.ManagementRequests(context.Background(), &chatv1.ManagementRequestsRequest{SessionId: valid.sessionID}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	// Normal server chat tags are not part of owner identity (e.g. GM tag4).
	p[len(p)-1] = 4
	valid.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGMessagechat, Payload: p})
	if len(management(t, valid).Requests) != 1 {
		t.Fatal("normal chat tag hid authenticated owner")
	}
}

func TestTeamManagementReplayNonceRetirementRingAndTTL(t *testing.T) {
	s := NewService(nil, 5)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	for i := uint32(1); i <= 70; i++ {
		ingestTeam(t, s, teamBody(s, "a1b2c3d4e5f6", i, []uint64{1, 4, 5, 8}[i%4], "RESET"))
	}
	ingestTeam(t, s, teamBody(s, "a1b2c3d4e5f6", 70, 1, "UNSTUCK"))
	ingestTeam(t, s, teamBody(s, "a1b2c3d4e5f6", 2, 1, "UNSTUCK"))
	r := management(t, s)
	if !r.CursorLost || r.OldestSequence != 7 || r.NextSequence != 70 || len(r.Requests) != 64 || r.IgnoredPackets != 2 {
		t.Fatal(r)
	}
	r.Requests[0].SubjectGuid = 3
	if management(t, s).Requests[0].SubjectGuid == 3 {
		t.Fatal("caller changed retained request")
	}
	now = now.Add(30 * time.Second)
	if r := management(t, s); r.Requests[0].Available || r.Requests[0].AgeMs != 30000 {
		t.Fatal("expired request appeared fresh", r)
	}
	now = now.Add(-31 * time.Second)
	if management(t, s).Requests[0].Available {
		t.Fatal("clock rollback made request fresh")
	}
	ingestTeam(t, s, teamBody(s, "111111111111", 1, 8, "UNSTUCK"))
	ingestTeam(t, s, teamBody(s, "a1b2c3d4e5f6", 71, 1, "RESET"))
	if management(t, s).LatestSequence != 71 {
		t.Fatal("retired addon nonce replayed")
	}
	// Exhaustion is fail-closed: old nonce tombstones never fall out of a ring.
	for i := 2; i < 17; i++ {
		ingestTeam(t, s, teamBody(s, fmt.Sprintf("%012x", i), 1, 8, "RESET"))
	}
	before := management(t, s).LatestSequence
	ingestTeam(t, s, teamBody(s, "ffffffffffff", 1, 8, "RESET"))
	ingestTeam(t, s, teamBody(s, "a1b2c3d4e5f6", 999, 8, "RESET"))
	if management(t, s).LatestSequence != before {
		t.Fatal("nonce capacity reopened old/new authority")
	}
}

func TestTeamStatusRestrictedPublicationFreshnessAndTransportOnly(t *testing.T) {
	recorder := &sessionRecorder{}
	s := NewService(teamSender{recorder}, 4)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	for _, mode := range []string{"epoch", "subject", "phase", "old", "half_ref", "bad_ref", "overflow_ref", "unreferenced_complete"} {
		r := &chatv1.TeamStatusRequest{SessionId: s.sessionID, SubjectGuid: 8, Phase: chatv1.TeamPhase_OFFLINE}
		switch mode {
		case "epoch":
			r.SessionId = "stale"
		case "subject":
			r.SubjectGuid = 3
		case "phase":
			r.Phase = chatv1.TeamPhase(999)
		case "old":
			r.ObservationAgeMs = 5001
		case "half_ref":
			r.RequestSequence = 1
		case "bad_ref":
			r.RequestSequence = 1
			r.RequestAddonSession = ".command"
		case "overflow_ref":
			r.RequestSequence = 0x80000000
			r.RequestAddonSession = "a1b2c3d4e5f6"
		case "unreferenced_complete":
			r.Phase = chatv1.TeamPhase_COMPLETE
		}
		if _, err := s.PublishTeamStatus(context.Background(), r); err == nil {
			t.Fatal("unsafe status accepted", mode)
		}
	}
	if len(recorder.packets) != 0 {
		t.Fatal("invalid status emitted a packet")
	}
	r := &chatv1.TeamStatusRequest{SessionId: s.sessionID, SubjectGuid: 8, Phase: chatv1.TeamPhase_DEFERRED_COMBAT, RequestAddonSession: "a1b2c3d4e5f6", RequestSequence: 7, ObservationAgeMs: 5000}
	out, err := s.PublishTeamStatus(context.Background(), r)
	if err != nil || !out.TransportWritten || out.TtlSeconds != 10 {
		t.Fatal(out, err)
	}
	if !strings.Contains(string(recorder.packets[0].Payload), "|8|DEFERRED_COMBAT|a1b2c3d4e5f6|7|10\x00") {
		t.Fatal(recorder.packets[0])
	}
	if _, err = s.PublishTeamStatus(context.Background(), r); status.Code(err) != codes.ResourceExhausted {
		t.Fatal("publication burst not bounded", err)
	}
	now = now.Add(time.Second)
	recorder.err = status.Error(codes.Unavailable, "ambiguous callback")
	if out, err = s.PublishTeamStatus(context.Background(), r); out != nil || status.Code(err) != codes.Unavailable || len(recorder.packets) != 2 {
		t.Fatal("transport failure fabricated receipt or retried", out, err)
	}
	if s.team.statusSeq != 2 {
		t.Fatal("failed sequence was not consumed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.PublishTeamStatus(ctx, r); status.Code(err) != codes.Canceled || len(recorder.packets) != 2 {
		t.Fatal(err)
	}
}

func TestTeamManagementIndependentAndUnknownRosterFailClosed(t *testing.T) {
	for _, s := range []*Service{NewIndependentService(teamSender{&sessionRecorder{}}, 8), NewService(teamSender{&sessionRecorder{}}, 42)} {
		ingestTeam(t, s, teamBody(s, "a1b2c3d4e5f6", 1, 5, "RESET"))
		if len(management(t, s).Requests) != 0 {
			t.Fatal("unowned character admitted controller intent")
		}
		if _, err := s.PublishTeamStatus(context.Background(), &chatv1.TeamStatusRequest{SessionId: s.sessionID, SubjectGuid: 1, Phase: chatv1.TeamPhase_ONLINE}); status.Code(err) != codes.FailedPrecondition {
			t.Fatal(err)
		}
	}
}

func FuzzTeamManagementPacket(f *testing.F) {
	f.Add(teamPacket("R|ABCDEF123456|a1b2c3d4e5f6|1|5|RESET"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, p []byte) {
		if len(p) > 1024 {
			return
		}
		decodeTeamRequest(p)
	})
}
