
‰
google/protobuf/any.protogoogle.protobuf"6
Any
type_url (	RtypeUrl
value (RvalueBv
com.google.protobufBAnyProtoPZ,google.golang.org/protobuf/types/known/anypb¢GPB™Google.Protobuf.WellKnownTypesbproto3
æ
google/protobuf/empty.protogoogle.protobuf"
EmptyB}
com.google.protobufB
EmptyProtoPZ.google.golang.org/protobuf/types/known/emptypb¯¢GPB™Google.Protobuf.WellKnownTypesbproto3
ß
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
result (2.google.protobuf.AnyRresult2Ä
SessionK

SendPacket%.agentwow.module.v1.SendPacketRequest.google.protobuf.EmptyE
GetClock.google.protobuf.Empty!.agentwow.module.v1.ClockResponsea
InvokeModule'.agentwow.module.v1.InvokeModuleRequest(.agentwow.module.v1.InvokeModuleResponseB5Z3github.com/agent-wow/agent-wow/pkg/modules/v1;modv1bproto3
 V
#modules/buddy-actions/actions.protowarcraftbuddy.actions.v1api/module/v1/session.protogoogle/protobuf/empty.proto")
ToggleRequest
enabled (Renabled"◊
FargodeepEncounterRequest
enabled (Renabled
target_guid (R
targetGuid!
engager_guid (RengagerGuid=
expected_quest_log_revision (RexpectedQuestLogRevision

session_id (	R	sessionId"Æ
FargodeepEncounterState
active (Ractive
target_guid (R
targetGuid!
engager_guid (RengagerGuid
phase (	Rphase!
remaining_ms (RremainingMs"9
ColdridgeLevelingRequest

request_id (	R	requestId"ï
ColdridgeLevelingState

request_id (	R	requestId
goal (	Rgoal!
target_level (RtargetLevel
	max_pulls (RmaxPulls
source_guid (R
sourceGuid+
position_revision (RpositionRevision#
pull_reserved (RpullReserved!
remaining_ms (RremainingMs%
observed_level	 (RobservedLevel.
level_five_observed
 (RlevelFiveObserved
	self_guid (RselfGuid"ı
RoadClearRequest

request_id (	R	requestId
target_guid (R
targetGuid

session_id (	R	sessionIdU
expected_position (2(.warcraftbuddy.actions.v1.ClientPositionRexpectedPosition+
position_revision (RpositionRevision"ù
RoadClearState

request_id (	R	requestId
goal (	Rgoal
target_guid (R
targetGuid
source_guid (R
sourceGuid
	self_guid (RselfGuid

session_id (	R	sessionId+
position_revision (RpositionRevision@
origin (2(.warcraftbuddy.actions.v1.ClientPositionRorigin!
remaining_ms	 (RremainingMs
	max_pulls
 (RmaxPulls#
pull_reserved (RpullReserved
active (Ractive
phase (	Rphase%
observed_level (RobservedLevel%
selected_entry (RselectedEntry%
selected_level (RselectedLevel.
selected_max_health (RselectedMaxHealth:
selected_faction_template (RselectedFactionTemplate2
source_death_observed (RsourceDeathObservedC
source_death_position_revision (RsourceDeathPositionRevision"˜
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
 (Rrevision"u
InteractionRequest
owner (	Rowner
token (	Rtoken
lease_ms (RleaseMs
harvest (Rharvest"@
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
npc_guid (RnpcGuid"ì
TravelToRequest
token (	Rtoken
map_id (RmapId
x (Rx
y (Ry
z (Rz)
arrival_distance (RarrivalDistance"(
ExitCryptRequest
token (	Rtoken"_
InteractionLease
token (	Rtoken5
state (2.warcraftbuddy.actions.v1.StateRstate"©
RecoveryState
enabled (Renabled
active (Ractive
phase (	Rphase
reason (	Rreason
death_epoch (R
deathEpoch+
release_attempted (RreleaseAttempted'
release_written (RreleaseWritten%
ghost_observed (RghostObserved,
corpse_query_known	 (RcorpseQueryKnown!
corpse_found
 (RcorpseFoundQ
corpse_location (2(.warcraftbuddy.actions.v1.ClientPositionRcorpseLocation"
corpse_map_id (RcorpseMapId
corpse_guid (R
corpseGuidW
graveyard_location (2(.warcraftbuddy.actions.v1.ClientPositionRgraveyardLocation.
reclaim_delay_known (RreclaimDelayKnown;
reclaim_delay_remaining_ms (RreclaimDelayRemainingMs+
reclaim_attempted (RreclaimAttempted'
reclaim_written (RreclaimWritten%
alive_observed (RaliveObserved
rested (Rrested1
movement_unconfirmed (RmovementUnconfirmed#
verified_legs (RverifiedLegs"Õ	
RoadCastObservedEvent
opcode (Ropcode!
payload_size (RpayloadSize
payload (Rpayload+
payload_truncated (RpayloadTruncated%
payload_sha256 (	RpayloadSha256
reason (	Rreason
revision (Rrevision
age_ms (RageMs!
caster_known	 (RcasterKnown(
cast_object_guid
 (RcastObjectGuid
caster_guid (R
casterGuid!
header_known (RheaderKnown

cast_count (R	castCount
spell_id (RspellId(
cast_flags_known (RcastFlagsKnown

cast_flags (R	castFlags,
target_flags_known (RtargetFlagsKnown!
target_flags (RtargetFlags*
target_guid_known (RtargetGuidKnown
target_guid (R
targetGuid%
body_supported (RbodySupported
go_event (RgoEvent&
hit_count_known (RhitCountKnown
	hit_count (RhitCount&
first_hit_known (RfirstHitKnown$
first_hit_guid (RfirstHitGuid(
miss_count_known (RmissCountKnown

miss_count (R	missCount(
first_miss_known (RfirstMissKnown&
first_miss_guid (RfirstMissGuid*
first_miss_reason (RfirstMissReason7
first_miss_reflect_known  (RfirstMissReflectKnown9
first_miss_reflect_result! (RfirstMissReflectResult"Ù
RoadCastObserverState
known (Rknown
busy (Rbusy
poisoned (Rpoisoned!
initial_seen (RinitialSeen'
spellbook_known (RspellbookKnown#
outgoing_seen (RoutgoingSeen(
pending_spell_id (RpendingSpellId.
pending_target_guid (RpendingTargetGuidV
first_rejected	 (2/.warcraftbuddy.actions.v1.RoadCastObservedEventRfirstRejected
epoch_known
 (R
epochKnownQ
first_own_go (2/.warcraftbuddy.actions.v1.RoadCastObservedEventR
firstOwnGo"Í
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
practice_kills> (RpracticeKills*
known_shadow_bolt? (RknownShadowBolt9
shadow_bolt_damage_events@ (RshadowBoltDamageEvents7
shadow_bolt_damage_totalA (RshadowBoltDamageTotal/
interaction_harvestB (RinteractionHarvest.
harvest_guard_guidsC (RharvestGuardGuidsC
recoveryD (2'.warcraftbuddy.actions.v1.RecoveryStateRrecoveryO
	fargodeepE (21.warcraftbuddy.actions.v1.FargodeepEncounterStateR	fargodeep_
coldridge_levelingF (20.warcraftbuddy.actions.v1.ColdridgeLevelingStateRcoldridgeLevelingG

road_clearI (2(.warcraftbuddy.actions.v1.RoadClearStateR	roadClear]
road_cast_observerJ (2/.warcraftbuddy.actions.v1.RoadCastObserverStateRroadCastObserver2Í
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
TravelQuest..warcraftbuddy.actions.v1.ApproachQuestRequest.warcraftbuddy.actions.v1.State`
TravelTrainer..warcraftbuddy.actions.v1.ApproachQuestRequest.warcraftbuddy.actions.v1.State_
TravelVendor..warcraftbuddy.actions.v1.ApproachQuestRequest.warcraftbuddy.actions.v1.StateV
TravelTo).warcraftbuddy.actions.v1.TravelToRequest.warcraftbuddy.actions.v1.StateX
	ExitCrypt*.warcraftbuddy.actions.v1.ExitCryptRequest.warcraftbuddy.actions.v1.StateT
Practice'.warcraftbuddy.actions.v1.ToggleRequest.warcraftbuddy.actions.v1.Statej
FargodeepEncounter3.warcraftbuddy.actions.v1.FargodeepEncounterRequest.warcraftbuddy.actions.v1.Statem
StartColdridgeLeveling2.warcraftbuddy.actions.v1.ColdridgeLevelingRequest.warcraftbuddy.actions.v1.State]
StartRoadClear*.warcraftbuddy.actions.v1.RoadClearRequest.warcraftbuddy.actions.v1.StateT
Recovery'.warcraftbuddy.actions.v1.ToggleRequest.warcraftbuddy.actions.v1.StateC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.Empty>
BeforeLogout.google.protobuf.Empty.google.protobuf.EmptyBDZBgithub.com/agent-wow/agent-wow/modules/buddy-actions/api;actionsv1bproto3