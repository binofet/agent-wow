
ä
google/protobuf/any.protogoogle.protobuf"6
Any
type_url (	RtypeUrl
value (RvalueBv
com.google.protobufBAnyProtoPZ,google.golang.org/protobuf/types/known/anypb¢GPBªGoogle.Protobuf.WellKnownTypesbproto3
¾
google/protobuf/empty.protogoogle.protobuf"
EmptyB}
com.google.protobufB
EmptyProtoPZ.google.golang.org/protobuf/types/known/emptypbø¢GPBªGoogle.Protobuf.WellKnownTypesbproto3
§
api/module/v1/session.protoagentwow.module.v1google/protobuf/any.protogoogle/protobuf/empty.proto"?
WorldPacket
opcode (Ropcode
payload (Rpayload"E
SendPacketRequest
opcode (Ropcode
payload (Rpayload"5
ClockResponse$
client_time_ms (RclientTimeMs"u
InvokeModuleRequest
module (	Rmodule
method (	Rmethod.
request (2.google.protobuf.AnyRrequest"D
InvokeModuleResponse,
result (2.google.protobuf.AnyRresult2€
SessionK

SendPacket%.agentwow.module.v1.SendPacketRequest.google.protobuf.EmptyE
GetClock.google.protobuf.Empty!.agentwow.module.v1.ClockResponsea
InvokeModule'.agentwow.module.v1.InvokeModuleRequest(.agentwow.module.v1.InvokeModuleResponseB5Z3github.com/agent-wow/agent-wow/pkg/modules/v1;modv1bproto3
â%
#modules/buddy-actions/actions.protowarcraftbuddy.actions.v1api/module/v1/session.protogoogle/protobuf/empty.proto")
ToggleRequest
enabled (Renabled"÷
ClientPosition
known (Rknown
	map_known (RmapKnown
map_id (RmapId
x (Rx
y (Ry
z (Rz 
orientation (Rorientation
	estimated (R	estimated
age_ms	 (RageMs
revision
 (Rrevision"[
InteractionRequest
owner (	Rowner
token (	Rtoken
lease_ms (RleaseMs"@
InteractionRelease
owner (	Rowner
token (	Rtoken"v
ApproachLootRequest
token (	Rtoken
corpse_guid (R
corpseGuid(
game_object_guid (RgameObjectGuid"G
ApproachQuestRequest
token (	Rtoken
npc_guid (RnpcGuid"_
InteractionLease
token (	Rtoken5
state (2.warcraftbuddy.actions.v1.StateRstate"Ê
State
	following (R	following
healing (Rhealing
moving (Rmoving*
known_lesser_heal (RknownLesserHeal!
cast_pending (RcastPending
reason (	Rreason)
transport_writes (RtransportWrites#
heal_attempts (RhealAttempts,
server_cast_starts	 (RserverCastStarts$
server_cast_go
 (RserverCastGo*
last_cast_failure (RlastCastFailure+
malformed_packets (RmalformedPackets9
follow_lease_remaining_ms (RfollowLeaseRemainingMs;
healing_lease_remaining_ms (RhealingLeaseRemainingMs

limitation (	R
limitation-
motion_unconfirmed (RmotionUnconfirmedD
position (2(.warcraftbuddy.actions.v1.ClientPositionRposition+
interaction_owner (	RinteractionOwnerC
interaction_lease_remaining_ms (RinteractionLeaseRemainingMs
	assisting (R	assisting
known_smite (R
knownSmite'
assist_attempts (RassistAttempts9
assist_lease_remaining_ms (RassistLeaseRemainingMs+
last_cast_spell_id (RlastCastSpellId1
last_cast_target_guid (RlastCastTargetGuid0
server_assist_starts (RserverAssistStarts(
server_assist_go (RserverAssistGo#
assist_reason (	RassistReason.
smite_damage_events (RsmiteDamageEvents,
smite_damage_total (RsmiteDamageTotal
class_known (R
classKnown
class_id  (RclassId

class_name! (	R	className%
known_fireball" (RknownFireball2
known_sinister_strike# (RknownSinisterStrike*
known_auto_attack$ (RknownAutoAttack&
combat_spell_id% (RcombatSpellId!
combat_style& (	RcombatStyle0
assist_damage_events' (RassistDamageEvents.
assist_damage_total( (RassistDamageTotal4
fireball_damage_events) (RfireballDamageEvents2
fireball_damage_total* (RfireballDamageTotalA
sinister_strike_damage_events+ (RsinisterStrikeDamageEvents?
sinister_strike_damage_total, (RsinisterStrikeDamageTotal.
melee_damage_events- (RmeleeDamageEvents,
melee_damage_total. (RmeleeDamageTotal%
auto_attacking/ (RautoAttacking5
auto_attack_target_guid0 (RautoAttackTargetGuid2
auto_attack_requested1 (RautoAttackRequested6
auto_attack_unconfirmed2 (RautoAttackUnconfirmed=
sinister_strike_energy_cost3 (RsinisterStrikeEnergyCost8
mainhand_weapon_verified4 (RmainhandWeaponVerified
	traveling5 (R	traveling&
travel_npc_guid6 (RtravelNpcGuid#
nav_available7 (RnavAvailable

nav_reason8 (	R	navReason

practicing9 (R
practicing%
practice_phase: (	RpracticePhase0
practice_target_guid; (RpracticeTargetGuid=
practice_lease_remaining_ms< (RpracticeLeaseRemainingMs%
practice_pulls= (RpracticePulls%
practice_kills> (RpracticeKills2å
BuddyActionsR
Follow'.warcraftbuddy.actions.v1.ToggleRequest.warcraftbuddy.actions.v1.StateS
Healing'.warcraftbuddy.actions.v1.ToggleRequest.warcraftbuddy.actions.v1.StateR
Assist'.warcraftbuddy.actions.v1.ToggleRequest.warcraftbuddy.actions.v1.StateC
HealOnce.google.protobuf.Empty.warcraftbuddy.actions.v1.StateA
Status.google.protobuf.Empty.warcraftbuddy.actions.v1.Staten
AcquireInteraction,.warcraftbuddy.actions.v1.InteractionRequest*.warcraftbuddy.actions.v1.InteractionLeasec
ReleaseInteraction,.warcraftbuddy.actions.v1.InteractionRelease.warcraftbuddy.actions.v1.State^
ApproachLoot-.warcraftbuddy.actions.v1.ApproachLootRequest.warcraftbuddy.actions.v1.State`
ApproachQuest..warcraftbuddy.actions.v1.ApproachQuestRequest.warcraftbuddy.actions.v1.State^
TravelQuest..warcraftbuddy.actions.v1.ApproachQuestRequest.warcraftbuddy.actions.v1.StateT
Practice'.warcraftbuddy.actions.v1.ToggleRequest.warcraftbuddy.actions.v1.StateC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.Empty>
BeforeLogout.google.protobuf.Empty.google.protobuf.EmptyBDZBgithub.com/agent-wow/agent-wow/modules/buddy-actions/api;actionsv1bproto3