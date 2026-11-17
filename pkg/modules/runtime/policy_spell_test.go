package modrt

import (
	"context"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestPolicySpellCallbackBindsScopeAndWrite(t *testing.T) {
	m := New(nil, nil, nil)
	t.Cleanup(m.cancel)
	origin := modsession.WriteOrigin{
		SessionID: "s1", Module: actionsModule, Writer: "lua",
		PolicyRevision: "rev", Generation: 2, ActionID: "7",
	}
	token := strings.Repeat("a", 64)
	op := &policySpell{capability: "polymorph", spell: 118, target: 42, origin: origin, admitted: true}
	m.policySpells.active = map[string]*policySpell{token: op}
	body := []byte{1, 118, 0, 0, 0, 0, 2, 0, 0, 0, 1, 42}
	var writes []modsession.WriteOrigin
	m.session.SendPacket = func(ctx context.Context, _ uint32, _ []byte) error {
		writes = append(writes, modsession.WriteOriginFromContext(ctx))
		return nil
	}
	ctx := context.Background()
	incoming := func(outgoing context.Context) context.Context {
		md, _ := metadata.FromOutgoingContext(outgoing)
		return metadata.NewIncomingContext(ctx, md)
	}
	trusted := incoming(modcb.WithPolicySpell(ctx, token))
	rejected := []struct {
		name, session, module string
		ctx                   context.Context
		code                  uint32
		body                  []byte
	}{
		{"wrong module", "s1", "buddy-world", trusted, opcode.CMSGCastSpell, body},
		{"wrong session", "s2", actionsModule, trusted, opcode.CMSGCastSpell, body},
		{"wrong opcode", "s1", actionsModule, trusted, opcode.CMSGUseItem, body},
		{"wrong target", "s1", actionsModule, trusted, opcode.CMSGCastSpell, append(append([]byte(nil), body[:11]...), 43)},
		{"trailing bytes", "s1", actionsModule, trusted, opcode.CMSGCastSpell, append(append([]byte(nil), body...), 0)},
		{"malformed token", "s1", actionsModule, incoming(modcb.WithPolicySpell(ctx, "invalid")), opcode.CMSGCastSpell, body},
	}
	for _, c := range rejected {
		t.Run(c.name, func(t *testing.T) {
			err := m.modulePacketWriter(c.session, c.module)(c.ctx, c.code, c.body)
			if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("invalid attribution: %v", err)
			}
		})
	}
	callback := m.modulePacketWriter("s1", actionsModule)
	if err := callback(ctx, opcode.CMSGCastSpell, body); err != nil {
		t.Fatal(err)
	}
	if err := callback(trusted, opcode.CMSGCastSpell, body); err != nil {
		t.Fatal(err)
	}
	if err := callback(trusted, opcode.CMSGCastSpell, body); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("replayed attribution: %v", err)
	}
	if len(writes) != 2 || writes[0] != (modsession.WriteOrigin{SessionID: "s1", Module: actionsModule, Writer: "module"}) || writes[1] != origin {
		t.Fatalf("got transport writes %+v", writes)
	}
}

func TestPolicySelfChannelEndsWithoutInventingManaGain(t *testing.T) {
	effect := &actionsv1.SpellOperation{
		Capability: "cast_self", SpellId: 12051, Phase: "channel_ended_effect_unverified",
		TransportWritten: true, ServerCastGo: true, ChannelStarted: true,
	}
	if got := spellEffectOutcome(effect); got != "" {
		t.Fatalf("channel still active: %s", got)
	}
	effect.ChannelEnded = true
	if got := spellEffectOutcome(effect); got != behavior.ActionSettledUnverified || effect.EffectObserved {
		t.Fatalf("ended channel: %s, effect observed: %v", got, effect.EffectObserved)
	}
}
