// Package channelstate interprets the client's observed channel fields.
package channelstate

import worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"

// Idle requires the complete observed channel spell and both object-GUID words.
// buddy-world baselines omitted zero words on a complete own-player CREATE;
// login position, unavailable snapshots and partial VALUES do not establish it.
// A partial nonzero channel stays busy even before the remaining word arrives.
func Idle(player *worldv1.PlayerState) bool {
	return player != nil && player.Present && player.ChannelSpellKnown && player.ChannelObjectKnown &&
		player.ChannelSpellId == 0 && player.ChannelObjectGuid == 0
}
