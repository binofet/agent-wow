package buddyactions

import "encoding/binary"

// This exact login frame was captured by the public client in
// rollouts-level5/rpc-20261031T051135296430Z.json (4250d9abb27b7e16363d932748458d3b33c01a10be298d8a3ebe62b4e8849969).
// Payload SHA256:a8163d30687a0405ebf97d502662e992c7ac21588299c2747d11adc87a9ffb76.
// The pinned993b459 public/client catalog proof (receipt2c89508a...) identifies
// 836 as the instant, zero-cost, non-channel login visual with only an empty
// SPAWN handler. This is not general support for zero unit targets or GO-only
// completion. The timestamp is consumed but grants no freshness authority.
func roadLogin836Frame(p []byte) bool {
	return len(p) == 32 && p[0] == 1 && p[1] == 8 && p[2] == 1 && p[3] == 8 && p[4] == 0 &&
		binary.LittleEndian.Uint32(p[5:]) == 836 && binary.LittleEndian.Uint32(p[9:]) == 0x40101 &&
		p[17] == 1 && binary.LittleEndian.Uint64(p[18:]) == 8 && p[26] == 0 && binary.LittleEndian.Uint32(p[27:]) == 2 && p[31] == 0
}

func (s *Service) roadLogin836AllowedLocked(p []byte) bool {
	if !roadLogin836Frame(p) || s.closed || !s.independent || s.selfGUID != 8 || !s.roadInitialSeen || s.roadInitialCount != 1 || !s.roadEpochKnown || !s.roadSpellbookKnown || !s.knownSpells[shadowBolt] || !s.roadCast.known || s.roadCast.busy || s.roadCast.started || s.roadCast.poisoned || s.roadFirstRejected != nil || s.roadCast.spell != 0 || s.roadCast.target != 0 || s.roadCast.count != 0 || s.roadOwnStartSeen || s.roadOwnGoSeen || s.roadLogin836Accepted || s.roadOutgoingSeen || s.roadAnyWriteAttempted || s.state.TransportWrites != 0 {
		return false
	}
	return !s.state.CastPending && s.pendingSpell == 0 && s.pendingTarget == 0 && !s.road.selected && len(s.roadRequests) == 0 && len(s.roadBodies) == 0 && !s.state.Following && !s.state.Healing && !s.state.Assisting && !s.state.Practicing && !s.state.Traveling && !s.questApproaching && s.state.InteractionOwner == "" && !s.state.Moving && !s.state.MotionUnconfirmed && !s.state.AutoAttacking && !s.state.AutoAttackRequested && !s.state.AutoAttackUnconfirmed && s.autoTarget == 0 && !s.recovery.state.Enabled && !s.fargodeep.active && !s.coldridgeGoal.leveling && s.coldridgeGoal.questID == 0 && len(s.coldridgeLevelingRequests) == 0
}
