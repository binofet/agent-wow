package modcb

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

// PolicySpellService advertises that the running Actions binary requires the
// dispatch token on its cast callback. Older binaries have no such service.
const PolicySpellService = "agentwow.policy-spell.v1"

// PolicyHostileSpellService identifies Actions builds with the exact offense
// RPC and the policy-only Sap path that requires observed Stealth.
const PolicyHostileSpellService = "agentwow.policy-hostile-spell.v1"

// PolicySupportSpellService identifies exact resurrection and interrupt bridges.
const PolicySupportSpellService = "agentwow.policy-support-spell.v1"

// PolicyMeleeService identifies Actions builds with exact, acknowledged start
// and stop operations on the policy callback bridge.
const PolicyMeleeService = "agentwow.policy-melee.v1"

const (
	policySpellKey        = "agentwow-policy-spell"
	policyReceiptKey      = "agentwow-policy-spell-receipt-bin"
	policyMeleeKey        = "agentwow-policy-melee"
	policyMeleeReceiptKey = "agentwow-policy-melee-receipt-bin"
)

// WithPolicySpell sends one host-issued token on a typed Actions RPC.
func WithPolicySpell(ctx context.Context, token string) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.Pairs(policySpellKey, token))
}

// IncomingPolicySpell distinguishes a legacy call from malformed attribution.
func IncomingPolicySpell(ctx context.Context) (token string, supplied bool) {
	values := metadata.ValueFromIncomingContext(ctx, policySpellKey)
	if len(values) == 1 && len(values[0]) == 64 {
		return values[0], true
	}
	return "", len(values) != 0
}

// PolicySpellToken returns the token for the operation reserved in Actions.
func PolicySpellToken(ctx context.Context) string {
	token, _ := IncomingPolicySpell(ctx)
	return token
}

// ForwardPolicySpell carries only the token to the cast's SendPacket callback.
func ForwardPolicySpell(ctx context.Context) context.Context {
	if token, supplied := IncomingPolicySpell(ctx); supplied {
		return WithPolicySpell(ctx, token)
	}
	return ctx
}

// ReportPolicySpellEffect returns a typed native observation in the response
// to the packet delivery that produced it. Setting trailers performs no IO.
func ReportPolicySpellEffect(ctx context.Context, token string, effect proto.Message) bool {
	return reportPolicyEffect(ctx, policySpellKey, policyReceiptKey, token, effect)
}

// PolicySpellEffect reads the optional typed observation from packet delivery.
func PolicySpellEffect(trailer metadata.MD) (token string, body []byte, ok bool) {
	return readPolicyEffect(trailer, policySpellKey, policyReceiptKey)
}

// ReportPolicyMeleeEffect returns an attack acknowledgement from packet delivery.
// Its trailers are separate from spell effects so a lifecycle event can retire both.
func ReportPolicyMeleeEffect(ctx context.Context, token string, effect proto.Message) bool {
	return reportPolicyEffect(ctx, policyMeleeKey, policyMeleeReceiptKey, token, effect)
}

// PolicyMeleeEffect reads a typed attack acknowledgement from packet delivery.
func PolicyMeleeEffect(trailer metadata.MD) (token string, body []byte, ok bool) {
	return readPolicyEffect(trailer, policyMeleeKey, policyMeleeReceiptKey)
}

func reportPolicyEffect(ctx context.Context, tokenKey, bodyKey, token string, effect proto.Message) bool {
	body, err := proto.Marshal(effect)
	return err == nil && grpc.SetTrailer(ctx, metadata.Pairs(tokenKey, token, bodyKey, string(body))) == nil
}

func readPolicyEffect(trailer metadata.MD, tokenKey, bodyKey string) (token string, body []byte, ok bool) {
	tokens, bodies := trailer.Get(tokenKey), trailer.Get(bodyKey)
	if len(tokens) != 1 || len(tokens[0]) != 64 || len(bodies) != 1 || len(bodies[0]) > 2048 {
		return "", nil, false
	}
	return tokens[0], []byte(bodies[0]), true
}
