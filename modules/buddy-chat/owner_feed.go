package buddychat

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	chatv1 "github.com/agent-wow/agent-wow/modules/buddy-chat/api"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

const ownerFeedPrefix = "WBQUEST1\t"
const ownerFeedTTL = 10 * time.Second

type ownerFeedPart struct {
	nonce                         string
	revision, part, parts, active uint32
	tokens                        string
	bytes                         int
}
type ownerFeedAssembly struct {
	nonce                   string
	revision, parts, active uint32
	started                 time.Time
	tokens                  [12]string
	seen                    [12]bool
	count, bytes            int
}
type ownerFeedState struct {
	current                                  *chatv1.OwnerFeedResponse
	updated                                  time.Time
	pending                                  *ownerFeedAssembly
	retired                                  []string
	received, malformed, ignored, incomplete uint64
}

func ownerFeedNumber(text string, min, max uint32) (uint32, error) {
	n, err := strconv.ParseUint(text, 10, 32)
	if err != nil || n < uint64(min) || n > uint64(max) || strconv.FormatUint(n, 10) != text {
		return 0, errors.New("invalid owner feed number")
	}
	return uint32(n), nil
}

// AzerothCore Chat.cpp BuildChatPacket uses the ordinary PARTY layout even for
// LANG_ADDON. ChatHandler.cpp permits PARTY addon messages and caps their whole
// prefix+TAB+payload at255 bytes. Sender is the actual server packet GUID.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Chat/Chat.cpp
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/ChatHandler.cpp
func decodeOwnerFeedPart(p []byte) (*ownerFeedPart, bool, error) {
	if len(p) < 29+len(ownerFeedPrefix) || p[0] != byte(chatv1.Channel_PARTY) || int32(binary.LittleEndian.Uint32(p[1:5])) != -1 || binary.LittleEndian.Uint64(p[5:13]) != 3 || !bytes.HasPrefix(p[29:], []byte(ownerFeedPrefix)) {
		return nil, false, nil
	}
	bad := errors.New("invalid owner quest addon packet")
	length := binary.LittleEndian.Uint32(p[25:29])
	if binary.LittleEndian.Uint32(p[13:17]) != 0 || binary.LittleEndian.Uint64(p[17:25]) != 0 || length < uint32(len(ownerFeedPrefix)+2) || length > maxTextBytes+1 || len(p) != 29+int(length)+1 || p[29+int(length)-1] != 0 {
		return nil, true, bad
	}
	body := p[29+len(ownerFeedPrefix) : 29+int(length)-1]
	if len(body) == 0 || len(body) > 240 {
		return nil, true, bad
	}
	for _, b := range body {
		if b < 0x20 || b > 0x7e {
			return nil, true, bad
		}
	}
	fields := strings.Split(string(body), "|")
	if len(fields) != 7 || fields[0] != "1" || len(fields[1]) != 12 || fields[6] == "" {
		return nil, true, bad
	}
	for _, b := range []byte(fields[1]) {
		if !(b >= '0' && b <= '9') && !(b >= 'a' && b <= 'f') {
			return nil, true, bad
		}
	}
	revision, e1 := ownerFeedNumber(fields[2], 1, 0x7fffffff)
	part, e2 := ownerFeedNumber(fields[3], 1, 12)
	parts, e3 := ownerFeedNumber(fields[4], 1, 12)
	active, e4 := ownerFeedNumber(fields[5], 0, 25)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || part > parts {
		return nil, true, bad
	}
	return &ownerFeedPart{nonce: fields[1], revision: revision, part: part, parts: parts, active: active, tokens: fields[6], bytes: len(body)}, true, nil
}

func decodeOwnerFeedSnapshot(a *ownerFeedAssembly) (*chatv1.OwnerFeedResponse, error) {
	bad := errors.New("invalid complete owner quest addon snapshot")
	out := &chatv1.OwnerFeedResponse{Available: true, AddonSession: a.nonce, Revision: a.revision}
	quests, rewards, accepts := map[uint32]bool{}, map[uint32]bool{}, map[uint32]bool{}
	completed := map[uint32]bool{}
	levelKnown := false
	for _, part := range a.tokens[:a.parts] {
		for _, token := range strings.Split(part, ";") {
			if len(token) < 3 || token[1] != '=' {
				return nil, bad
			}
			fields := strings.Split(token[2:], ",")
			switch token[0] {
			case 'L':
				if len(fields) != 1 || levelKnown {
					return nil, bad
				}
				level, err := ownerFeedNumber(fields[0], 1, 80)
				if err != nil {
					return nil, bad
				}
				out.Level, levelKnown = level, true
			case 'Q':
				if len(fields) != 2 || len(quests) >= 25 {
					return nil, bad
				}
				id, e1 := ownerFeedNumber(fields[0], 1, 0x7fffffff)
				state, e2 := ownerFeedNumber(fields[1], 0, 2)
				if e1 != nil || e2 != nil || quests[id] {
					return nil, bad
				}
				quests[id] = true
				out.Quests = append(out.Quests, &chatv1.OwnerQuestState{QuestId: id, State: state})
			case 'C':
				if len(fields) != 1 || len(completed) >= 64 {
					return nil, bad
				}
				id, err := ownerFeedNumber(fields[0], 1, 0x7fffffff)
				if err != nil || completed[id] {
					return nil, bad
				}
				completed[id] = true
				out.CompletedQuestIds = append(out.CompletedQuestIds, id)
			case 'R', 'A':
				if len(fields) != 3 || len(fields[1]) != 16 {
					return nil, bad
				}
				id, e1 := ownerFeedNumber(fields[0], 1, 0x7fffffff)
				guid, e2 := strconv.ParseUint(fields[1], 16, 64)
				age, e3 := ownerFeedNumber(fields[2], 0, 900)
				if e1 != nil || e2 != nil || e3 != nil || guid>>48 != 0xf130 || guid>>24&0xffffff == 0 || guid&0xffffff == 0 {
					return nil, bad
				}
				event := &chatv1.OwnerQuestEvent{QuestId: id, NpcGuid: guid, AgeMs: uint64(age) * 1000}
				if token[0] == 'R' {
					if len(rewards) >= 8 || rewards[id] {
						return nil, bad
					}
					rewards[id] = true
					out.Rewarded = append(out.Rewarded, event)
				} else {
					if len(accepts) >= 8 || accepts[id] {
						return nil, bad
					}
					accepts[id] = true
					out.Accepted = append(out.Accepted, event)
				}
			default:
				return nil, bad
			}
		}
	}
	if !levelKnown || len(quests) != int(a.active) {
		return nil, bad
	}
	sort.Slice(out.Quests, func(i, j int) bool { return out.Quests[i].QuestId < out.Quests[j].QuestId })
	sort.Slice(out.CompletedQuestIds, func(i, j int) bool { return out.CompletedQuestIds[i] < out.CompletedQuestIds[j] })
	return out, nil
}

func (f *ownerFeedState) expire(now time.Time) {
	if f.pending != nil && (now.Before(f.pending.started) || now.Sub(f.pending.started) >= ownerFeedTTL) {
		f.pending = nil
		f.incomplete++
	}
}

// Returns true only for this fixed prefix on the authenticated owner's PARTY
// addon channel. Such traffic never enters, advances or overflows the chat ring.
func (s *Service) ingestOwnerFeedLocked(payload []byte) bool {
	part, handled, err := decodeOwnerFeedPart(payload)
	if !handled {
		return false
	}
	if s.independent {
		s.ignored++
		return true
	}
	f, now := &s.ownerFeed, s.now()
	f.expire(now)
	f.received++
	if err != nil {
		f.malformed++
		return true
	}
	for _, nonce := range f.retired {
		if nonce == part.nonce {
			f.ignored++
			return true
		}
	}
	if f.current != nil && part.nonce == f.current.AddonSession && part.revision <= f.current.Revision {
		f.ignored++
		return true
	}
	a := f.pending
	if a != nil && a.nonce == part.nonce && part.revision < a.revision {
		f.ignored++
		return true
	}
	if a == nil || a.nonce != part.nonce || a.revision != part.revision {
		if a != nil {
			f.incomplete++
		}
		a = &ownerFeedAssembly{nonce: part.nonce, revision: part.revision, parts: part.parts, active: part.active, started: now}
		f.pending = a
	}
	index := part.part - 1
	if a.parts != part.parts || a.active != part.active || a.seen[index] && a.tokens[index] != part.tokens {
		f.malformed++
		f.pending = nil
		return true
	}
	if a.seen[index] {
		f.ignored++
		return true
	}
	if a.bytes+part.bytes > 2160 {
		f.malformed++
		f.pending = nil
		return true
	}
	a.tokens[index], a.seen[index] = part.tokens, true
	a.bytes += part.bytes
	a.count++
	if a.count != int(a.parts) {
		return true
	}
	complete, err := decodeOwnerFeedSnapshot(a)
	f.pending = nil
	if err != nil {
		f.malformed++
		return true
	}
	if f.current != nil && f.current.AddonSession != complete.AddonSession {
		f.retired = append(f.retired, f.current.AddonSession)
		if len(f.retired) > 8 {
			f.retired = f.retired[len(f.retired)-8:]
		}
	}
	// Assembly latency must not make an old UI snapshot or event appear new.
	f.current, f.updated = complete, a.started
	return true
}

func (s *Service) OwnerFeed(ctx context.Context, _ *emptypb.Empty) (*chatv1.OwnerFeedResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.independent {
		return &chatv1.OwnerFeedResponse{BodySessionId: s.sessionID}, nil
	}
	f, now := &s.ownerFeed, s.now()
	f.expire(now)
	out := &chatv1.OwnerFeedResponse{}
	if f.current != nil {
		out = proto.Clone(f.current).(*chatv1.OwnerFeedResponse)
		elapsed := now.Sub(f.updated).Milliseconds()
		if elapsed < 0 {
			elapsed = 0
		}
		out.AgeMs = uint64(elapsed)
		for _, events := range [][]*chatv1.OwnerQuestEvent{out.Rewarded, out.Accepted} {
			for _, event := range events {
				event.AgeMs += uint64(elapsed)
			}
		}
	}
	out.BodySessionId = s.sessionID
	out.ReceivedPackets, out.MalformedPackets, out.IgnoredPackets, out.IncompleteBatches = f.received, f.malformed, f.ignored, f.incomplete
	out.Assembling = f.pending != nil
	return out, nil
}
