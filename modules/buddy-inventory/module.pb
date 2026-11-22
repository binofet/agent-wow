
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
õ
api/module/v1/session.protoagentwow.module.v1google/protobuf/any.protogoogle/protobuf/empty.proto"å
WorldPacket
opcode (Ropcode
payload (RpayloadK
transport_scope (2".agentwow.module.v1.TransportScopeRtransportScope"í
SendPacketRequest
opcode (Ropcode
payload (RpayloadK
transport_scope (2".agentwow.module.v1.TransportScopeRtransportScope"’
TransportScope#
connection_id (	RconnectionId
	self_guid (RselfGuid
visit_bound (R
visitBound%
visit_revision (RvisitRevision
map_id (RmapId'
motion_revision (RmotionRevision&
spline_id_known (RsplineIdKnown
	spline_id (RsplineId'
spline_revision	 (RsplineRevision4
spline_source_revision
 (RsplineSourceRevision#
spline_active (RsplineActive0
spline_source_opcode (RsplineSourceOpcode"5
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
ﬂÜ
modules/buddy-world/world.protowarcraftbuddy.world.v1api/module/v1/session.protogoogle/protobuf/empty.proto"£
PlayerState
guid (Rguid
present (Rpresent
	map_known (RmapKnown
map_id (RmapId%
position_known (RpositionKnown
x (Rx
y (Ry
z (Rz 
orientation	 (Rorientation&
position_age_ms
 (RpositionAgeMs+
position_revision (RpositionRevision0
movement_flags_known (RmovementFlagsKnown%
movement_flags (RmovementFlags0
extra_movement_flags (RextraMovementFlags#
movement_time (RmovementTime!
health_known (RhealthKnown
health (Rhealth

max_health (R	maxHealth
power_known (R
powerKnown
power (Rpower
	max_power (RmaxPower

power_type (R	powerType(
unit_flags_known (RunitFlagsKnown

unit_flags (R	unitFlags
alive_known (R
aliveKnown
alive (Ralive"
vitals_age_ms (RvitalsAgeMs!
speeds_known (RspeedsKnown

walk_speed (R	walkSpeed
	run_speed (RrunSpeed,
observation_age_ms (RobservationAgeMs!
player_flags  (RplayerFlags#
dynamic_flags! (RdynamicFlags!
target_known" (RtargetKnown
target_guid# (R
targetGuid)
faction_template$ (RfactionTemplate 
unit_flags_2% (R
unitFlags2
class_known& (R
classKnown
class_id' (RclassId
level_known( (R
levelKnown
level) (Rlevel&
base_mana_known* (RbaseManaKnown
	base_mana+ (RbaseMana,
combat_reach_known, (RcombatReachKnown!
combat_reach- (RcombatReach3
base_attack_time_known. (RbaseAttackTimeKnown-
base_attack_time_ms/ (RbaseAttackTimeMsB
mainhand0 (2&.warcraftbuddy.world.v1.EquippedWeaponRmainhand0
channel_object_known1 (RchannelObjectKnown.
channel_object_guid2 (RchannelObjectGuid.
channel_spell_known3 (RchannelSpellKnown(
channel_spell_id4 (RchannelSpellId2
shapeshift_form_known5 (RshapeshiftFormKnown'
shapeshift_form6 (RshapeshiftForm@
offhand7 (2&.warcraftbuddy.world.v1.EquippedWeaponRoffhand)
offhand_revision8 (RoffhandRevision

race_known9 (R	raceKnown
race_id: (RraceId(
aura_state_known; (RauraStateKnown

aura_state< (R	auraState+
position_inferred= (RpositionInferred"◊
MovementTransportFacts
guid (Rguid
x (Rx
y (Ry
z (Rz 
orientation (Rorientation
time (Rtime
seat (Rseat
time2_known (R
time2Known
time2	 (Rtime2"Ñ
MovementJumpFacts
velocity (Rvelocity
	sin_angle (RsinAngle
	cos_angle (RcosAngle
xy_speed (RxySpeed"Ï
MovementInfoFacts
flags (Rflags
extra_flags (R
extraFlags
time (Rtime
x (Rx
y (Ry
z (Rz 
orientation (RorientationL
	transport (2..warcraftbuddy.world.v1.MovementTransportFactsR	transport
pitch_known	 (R
pitchKnown
pitch
 (Rpitch
	fall_time (RfallTime=
jump (2).warcraftbuddy.world.v1.MovementJumpFactsRjump4
spline_elevation_known (RsplineElevationKnown)
spline_elevation (RsplineElevation"÷
OwnMovementFacts
known (Rknown
	self_guid (RselfGuid%
visit_revision (RvisitRevision
revision (Rrevision#
source_opcode (RsourceOpcode
age_ms (RageMs
	map_known (RmapKnown
map_id (RmapId=
info	 (2).warcraftbuddy.world.v1.MovementInfoFactsRinfo

wire_image
 (R	wireImage"œ
OwnSplineFacts
known (Rknown
	self_guid (RselfGuid%
visit_revision (RvisitRevision
revision (Rrevision#
source_opcode (RsourceOpcode
age_ms (RageMs
id_known (RidKnown
	spline_id (RsplineId
flags	 (Rflags
duration_ms
 (R
durationMs

elapsed_ms (R	elapsedMs+
destination_known (RdestinationKnown#
destination_x (RdestinationX#
destination_y (RdestinationY#
destination_z (RdestinationZ-
transport_relative (RtransportRelative%
transport_guid (RtransportGuid%
transport_seat (RtransportSeat
stopped (Rstopped
cyclic (Rcyclic
	map_known (RmapKnown
map_id (RmapId"ì
OwnMotionAuthority
known (Rknown
	self_guid (RselfGuid%
visit_revision (RvisitRevision
revision (Rrevision+
movement_revision (RmovementRevision'
spline_revision (RsplineRevisionK
transport_scope (2".agentwow.module.v1.TransportScopeRtransportScope
reason (	Rreason2
duration_remaining_ms	 (RdurationRemainingMs(
unit_flags_known
 (RunitFlagsKnown

unit_flags (R	unitFlags.
unit_flags_revision (RunitFlagsRevision)
unit_flags_age_ms (RunitFlagsAgeMs7
unit_flags_source_opcode (RunitFlagsSourceOpcode;
unit_flags_motion_revision (RunitFlagsMotionRevision"⁄
CompleteSplineRequest
	self_guid (RselfGuid%
visit_revision (RvisitRevision-
authority_revision (RauthorityRevision+
movement_revision (RmovementRevision'
spline_revision (RsplineRevision+
expected_revision (RexpectedRevisionK
transport_scope (2".agentwow.module.v1.TransportScopeRtransportScope"√
SplineCompletionOperation
revision (Rrevision
	self_guid (RselfGuid%
visit_revision (RvisitRevision-
authority_revision (RauthorityRevision+
movement_revision (RmovementRevision'
spline_revision (RsplineRevisionK
transport_scope (2".agentwow.module.v1.TransportScopeRtransportScope
pending (Rpending+
transport_written	 (RtransportWritten+
transport_unknown
 (RtransportUnknown
phase (	Rphase
error (	Rerror6
movement_image_inferred (RmovementImageInferred#
arrival_known (RarrivalKnown
	spline_id (RsplineId&
spline_id_known (RsplineIdKnown2
movement_image_source (	RmovementImageSource"Œ
SplineCompletionObservation
	self_guid (RselfGuid%
visit_revision (RvisitRevision
revision (RrevisionO
	operation (21.warcraftbuddy.world.v1.SplineCompletionOperationR	operation"é
EquipmentSlotState
slot (Rslot

slot_known (R	slotKnown
	item_guid (RitemGuid%
metadata_known (RmetadataKnown
entry (Rentry
stack_count (R
stackCount)
durability_known (RdurabilityKnown

durability (R
durability%
max_durability	 (RmaxDurability-
enhancements_known
 (RenhancementsKnown)
has_enhancements (RhasEnhancements"»
CorpseState
guid (Rguid

owner_guid (R	ownerGuid
present (Rpresent
	map_known (RmapKnown
map_id (RmapId%
position_known (RpositionKnown
x (Rx
y (Ry
z	 (Rz 
orientation
 (Rorientation+
position_revision (RpositionRevision&
position_age_ms (RpositionAgeMs,
observation_age_ms (RobservationAgeMs
flags_known (R
flagsKnown
flags (Rflags.
dynamic_flags_known (RdynamicFlagsKnown#
dynamic_flags (RdynamicFlags

race_known (R	raceKnown
race_id (RraceId"˚
EquippedWeapon

slot_known (R	slotKnown
	item_guid (RitemGuid%
metadata_known (RmetadataKnown
entry (Rentry)
durability_known (RdurabilityKnown

durability (R
durability%
max_durability (RmaxDurability"Å
SnapshotRequest#
creature_guid (RcreatureGuid%
creature_entry (RcreatureEntry*
game_object_entry (RgameObjectEntry:
include_gathering_objects (RincludeGatheringObjects:
creature_faction_template (RcreatureFactionTemplate"É
ClientMovementRequest
	self_guid (RselfGuid
map_id (RmapId8
server_position_revision (RserverPositionRevision
sequence (Rsequence
valid (Rvalid
x (Rx
y (Ry
z (Rz 
orientation	 (Rorientation"ú
DiscoveryAnchor
	self_guid (RselfGuid
map_id (RmapId8
server_position_revision (RserverPositionRevision
sequence (Rsequence
x (Rx
y (Ry
z (Rz 
orientation (Rorientation
	estimated	 (R	estimated
age_ms
 (RageMs"›
GameObjectState
guid (Rguid
entry (Rentry
present (Rpresent
	map_known (RmapKnown
map_id (RmapId%
position_known (RpositionKnown
x (Rx
y (Ry
z	 (Rz 
orientation
 (Rorientation+
position_revision (RpositionRevision&
position_age_ms (RpositionAgeMs,
observation_age_ms (RobservationAgeMs

type_known (R	typeKnown(
game_object_type (RgameObjectType
flags_known (R
flagsKnown
flags (Rflags.
dynamic_flags_known (RdynamicFlagsKnown#
dynamic_flags (RdynamicFlags(
created_by_known (RcreatedByKnown&
created_by_guid (RcreatedByGuid
state_known (R
stateKnown
state (Rstate"§
CreatureState
guid (Rguid
entry (Rentry
	npc_flags (RnpcFlags
present (Rpresent
	map_known (RmapKnown
map_id (RmapId%
position_known (RpositionKnown
x (Rx
y	 (Ry
z
 (Rz 
orientation (Rorientation&
position_age_ms (RpositionAgeMs+
position_revision (RpositionRevision,
observation_age_ms (RobservationAgeMs!
health_known (RhealthKnown
health (Rhealth

max_health (R	maxHealth
alive_known (R
aliveKnown
alive (Ralive

unit_flags (R	unitFlags#
dynamic_flags (RdynamicFlagsD
distance_to_self_observed_known (RdistanceToSelfObservedKnown9
distance_to_self_observed (RdistanceToSelfObservedF
 distance_to_owner_observed_known (RdistanceToOwnerObservedKnown;
distance_to_owner_observed (RdistanceToOwnerObserved%
movement_flags (RmovementFlags)
faction_template (RfactionTemplate!
target_known (RtargetKnown
target_guid (R
targetGuid 
unit_flags_2 (R
unitFlags2.
attack_victim_known (RattackVictimKnown,
attack_victim_guid  (RattackVictimGuid"
attack_age_ms! (RattackAgeMsJ

trajectory" (2*.warcraftbuddy.world.v1.CreatureTrajectoryR
trajectory,
combat_reach_known# (RcombatReachKnown!
combat_reach$ (RcombatReach
level_known% (R
levelKnown
level& (Rlevel(
unit_flags_known' (RunitFlagsKnown.
dynamic_flags_known( (RdynamicFlagsKnownE
active_cast) (2$.warcraftbuddy.world.v1.CreatureCastR
activeCast"®
CreatureCast
spell_id (RspellId

cast_count (R	castCount!
remaining_ms (RremainingMs
target_guid (R
targetGuid
revision (Rrevision"√
CreatureTrajectory
known (Rknown
duration_ms (R
durationMs
age_ms (RageMs+
destination_known (RdestinationKnown#
destination_x (RdestinationX#
destination_y (RdestinationY#
destination_z (RdestinationZ
cyclic (Rcyclic-
transport_relative	 (RtransportRelative%
transport_guid
 (RtransportGuid%
transport_seat (RtransportSeat
stopped (Rstopped
flags (Rflags"‘
QuestLogEntry
slot (Rslot
quest_id (RquestId
state (Rstate
complete (Rcomplete
failed (Rfailed)
objective_counts (RobjectiveCounts
expire_time (R
expireTime"˚
BackpackSlot
bag (Rbag
slot (Rslot
	item_guid (RitemGuid
entry (Rentry
stack_count (R
stackCount%
metadata_known (RmetadataKnown)
durability_known (RdurabilityKnown

durability (R
durability%
max_durability	 (RmaxDurability-
enhancements_known
 (RenhancementsKnown)
has_enhancements (RhasEnhancements"•
InventoryBag
bag (Rbag

slot_known (R	slotKnown%
container_guid (RcontainerGuid%
metadata_known (RmetadataKnown
entry (Rentry%
contents_known (RcontentsKnown

slot_count (R	slotCount:
slots (2$.warcraftbuddy.world.v1.BackpackSlotRslots"‘

SkillState
slot (Rslot
skill_id (RskillId
step (Rstep
value (Rvalue
	max_value (RmaxValue'
temporary_bonus (RtemporaryBonus'
permanent_bonus (RpermanentBonus"µ
SnapshotResponse7
self (2#.warcraftbuddy.world.v1.PlayerStateRself9
owner (2#.warcraftbuddy.world.v1.PlayerStateRowner'
ignored_packets (RignoredPackets+
malformed_packets (RmalformedPackets
last_opcode (R
lastOpcode(
last_parse_error (	RlastParseError)
observed_packets (RobservedPacketsH
quest_givers (2%.warcraftbuddy.world.v1.CreatureStateRquestGivers/
self_quest_log_known	 (RselfQuestLogKnown5
self_quest_log_revision
 (RselfQuestLogRevisionK
self_quest_log (2%.warcraftbuddy.world.v1.QuestLogEntryRselfQuestLogP
lootable_corpses (2%.warcraftbuddy.world.v1.CreatureStateRlootableCorpses%
backpack_known (RbackpackKnown+
backpack_revision (RbackpackRevision@
backpack (2$.warcraftbuddy.world.v1.BackpackSlotRbackpackH
owner_target (2%.warcraftbuddy.world.v1.CreatureStateRownerTargetP
nearby_creatures (2%.warcraftbuddy.world.v1.CreatureStateRnearbyCreatures*
self_copper_known (RselfCopperKnown
self_copper (R
selfCopper0
self_copper_revision (RselfCopperRevisionE
	mailboxes (2'.warcraftbuddy.world.v1.GameObjectStateR	mailboxesL
quest_objects (2'.warcraftbuddy.world.v1.GameObjectStateRquestObjectsT
requested_creature (2%.warcraftbuddy.world.v1.CreatureStateRrequestedCreature*
self_skills_known (RselfSkillsKnown0
self_skills_revision (RselfSkillsRevisionC
self_skills (2".warcraftbuddy.world.v1.SkillStateR
selfSkills@
self_free_primary_slots_known (RselfFreePrimarySlotsKnown5
self_free_primary_slots (RselfFreePrimarySlotsF
 self_free_primary_slots_revision (RselfFreePrimarySlotsRevisionL
spell_focuses (2'.warcraftbuddy.world.v1.GameObjectStateRspellFocusesW
own_fishing_bobbers (2'.warcraftbuddy.world.v1.GameObjectStateRownFishingBobbersV
requested_creatures  (2%.warcraftbuddy.world.v1.CreatureStateRrequestedCreatures]
requested_game_objects! (2'.warcraftbuddy.world.v1.GameObjectStateRrequestedGameObjects'
inventory_known" (RinventoryKnown-
inventory_revision# (RinventoryRevisionK
inventory_bags$ (2$.warcraftbuddy.world.v1.InventoryBagRinventoryBagsR
skinnable_corpses% (2%.warcraftbuddy.world.v1.CreatureStateRskinnableCorpsesJ
nearby_players& (2#.warcraftbuddy.world.v1.PlayerStateRnearbyPlayersa
nearby_gathering_objects' (2'.warcraftbuddy.world.v1.GameObjectStateRnearbyGatheringObjectsR
trainer_creatures( (2%.warcraftbuddy.world.v1.CreatureStateRtrainerCreatures0
self_equipment_known) (RselfEquipmentKnown6
self_equipment_revision* (RselfEquipmentRevisionQ
self_equipment+ (2*.warcraftbuddy.world.v1.EquipmentSlotStateRselfEquipmentB

own_corpse, (2#.warcraftbuddy.world.v1.CorpseStateR	ownCorpseR
discovery_anchor- (2'.warcraftbuddy.world.v1.DiscoveryAnchorRdiscoveryAnchorJ
player_corpses. (2#.warcraftbuddy.world.v1.CorpseStateRplayerCorpses%
visit_revision/ (RvisitRevisionX
self_movement_facts0 (2(.warcraftbuddy.world.v1.OwnMovementFactsRselfMovementFactsR
self_spline_facts1 (2&.warcraftbuddy.world.v1.OwnSplineFactsRselfSplineFacts^
self_motion_authority2 (2*.warcraftbuddy.world.v1.OwnMotionAuthorityRselfMotionAuthority`
spline_completion3 (23.warcraftbuddy.world.v1.SplineCompletionObservationRsplineCompletion"q
CombatEventsRequest

session_id (	R	sessionId%
after_sequence (RafterSequence
limit (Rlimit"Æ
CombatEvent
sequence (Rsequence-
received_at_unix_ms (RreceivedAtUnixMs
opcode (Ropcode
kind (	Rkind
source_guid (R
sourceGuid
target_guid (R
targetGuid
spell_id (RspellId!
amount_known (RamountKnown
amount	 (Ramount!
result_known
 (RresultKnown
result (Rresult
raw_payload (R
rawPayload
	malformed (R	malformed
detail (	Rdetail"x
CombatEventGap#
from_sequence (RfromSequence)
through_sequence (RthroughSequence
reason (	Rreason"€
CombatEventsResponse

session_id (	R	sessionId
	self_guid (RselfGuid%
first_sequence (RfirstSequence'
latest_sequence (RlatestSequence#
next_sequence (RnextSequence
has_more (RhasMore;
events (2#.warcraftbuddy.world.v1.CombatEventRevents:
gaps (2&.warcraftbuddy.world.v1.CombatEventGapRgaps2Ò

BuddyWorldt
CompleteSpline-.warcraftbuddy.world.v1.CompleteSplineRequest3.warcraftbuddy.world.v1.SplineCompletionObservationi
CombatEvents+.warcraftbuddy.world.v1.CombatEventsRequest,.warcraftbuddy.world.v1.CombatEventsResponse]
Snapshot'.warcraftbuddy.world.v1.SnapshotRequest(.warcraftbuddy.world.v1.SnapshotResponse^
ObserveClientMovement-.warcraftbuddy.world.v1.ClientMovementRequest.google.protobuf.EmptyC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.EmptyB@Z>github.com/agent-wow/agent-wow/modules/buddy-world/api;worldv1bproto3
è
modules/buddy-party/party.protowarcraftbuddy.party.v1api/module/v1/session.protogoogle/protobuf/empty.proto"√
PendingInvite
sequence (Rsequence!
inviter_name (	RinviterNameF
decision (2*.warcraftbuddy.party.v1.InvitationDecisionRdecision+
transport_written (RtransportWritten"≈
Member
guid (Rguid
name (	Rname
self (Rself
online (Ronline
subgroup (Rsubgroup
flags (Rflags
roles (Rroles
wire_status (R
wireStatus"¯
Group
known (Rknown
in_group (RinGroup

group_guid (R	groupGuid
leader_guid (R
leaderGuid8
members (2.warcraftbuddy.party.v1.MemberRmembers

type_flags (R	typeFlags%
server_counter (RserverCounter"|
CommandResult
	operation (R	operation
member_name (	R
memberName
result (Rresult
value (Rvalue"b

RaidTarget
icon (Ricon
target_guid (R
targetGuid
setter_guid (R
setterGuid"«
RaidTargetState

group_guid (R	groupGuid
revision (Rrevision
complete (Rcomplete8
marks (2".warcraftbuddy.party.v1.RaidTargetRmarks#
list_revision (RlistRevision"û
StateResponse

session_id (	R	sessionId
revision (RrevisionL
pending_invite (2%.warcraftbuddy.party.v1.PendingInviteRpendingInvite3
group (2.warcraftbuddy.party.v1.GroupRgroupU
last_command_result (2%.warcraftbuddy.party.v1.CommandResultRlastCommandResult,
last_declined_name (	RlastDeclinedName(
action_in_flight (RactionInFlight+
malformed_packets (RmalformedPackets'
ignored_packets	 (RignoredPacketsJ
raid_targets
 (2'.warcraftbuddy.party.v1.RaidTargetStateRraidTargets"Ç
InviteDecisionRequest

session_id (	R	sessionId'
invite_sequence (RinviteSequence!
inviter_name (	RinviterName"#
InviteRequest
name (	Rname"]
LeaveRequest

session_id (	R	sessionId.
expected_group_guid (RexpectedGroupGuid"f
RaidTargetListRequest

session_id (	R	sessionId.
expected_group_guid (RexpectedGroupGuid"=
ActionResponse+
transport_written (RtransportWritten*N
InvitationDecision
PENDING 
ACCEPT_REQUESTED
DECLINE_REQUESTED2˘

BuddyPartyF
State.google.protobuf.Empty%.warcraftbuddy.party.v1.StateResponse_
Accept-.warcraftbuddy.party.v1.InviteDecisionRequest&.warcraftbuddy.party.v1.ActionResponse`
Decline-.warcraftbuddy.party.v1.InviteDecisionRequest&.warcraftbuddy.party.v1.ActionResponseW
Invite%.warcraftbuddy.party.v1.InviteRequest&.warcraftbuddy.party.v1.ActionResponseU
Leave$.warcraftbuddy.party.v1.LeaveRequest&.warcraftbuddy.party.v1.ActionResponsek
RefreshRaidTargets-.warcraftbuddy.party.v1.RaidTargetListRequest&.warcraftbuddy.party.v1.ActionResponseC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.EmptyB@Z>github.com/agent-wow/agent-wow/modules/buddy-party/api;partyv1bproto3
ûﬂ
#modules/buddy-actions/actions.protowarcraftbuddy.actions.v1api/module/v1/session.protomodules/buddy-party/party.protomodules/buddy-world/world.protogoogle/protobuf/empty.proto"Ú
SpellCastTargets
mask (Rmask
object_guid (R
objectGuid
	item_guid (RitemGuidC
source (2+.warcraftbuddy.actions.v1.SpellCastLocationRsourceM
destination (2+.warcraftbuddy.actions.v1.SpellCastLocationRdestination#
string_target (	RstringTarget%
trade_revision (RtradeRevision,
trade_partner_guid (RtradePartnerGuid"d
SpellCastLocation%
transport_guid (RtransportGuid
x (Rx
y (Ry
z (Rz"é
SpellCastTrajectory
	elevation (R	elevation
speed (Rspeed'
movement_opcode (RmovementOpcode
movement (Rmovement"ô
CastSpellRequest

session_id (	R	sessionId
	self_guid (RselfGuid
visit_id (RvisitId
spell_id (RspellIdD
targets (2*.warcraftbuddy.actions.v1.SpellCastTargetsRtargetsM

trajectory (2-.warcraftbuddy.actions.v1.SpellCastTrajectoryR
trajectory"‡
CancelSpellRequest

session_id (	R	sessionId
	self_guid (RselfGuid
spell_id (RspellId>
expected_operation_revision (RexpectedOperationRevision
visit_id (RvisitId
channel (Rchannel")
ToggleRequest
enabled (Renabled"ô
MovementOwnerRequest

session_id (	R	sessionId
	self_guid (RselfGuid
owner (	Rowner
token (	Rtoken
lease_ms (RleaseMs"~
MovementOwnerRelease

session_id (	R	sessionId
	self_guid (RselfGuid
owner (	Rowner
token (	Rtoken"Å
MovementOwnerState
active (Ractive
owner (	Rowner!
remaining_ms (RremainingMs
revision (Rrevision"a
MovementOwnerLease
token (	Rtoken5
state (2.warcraftbuddy.actions.v1.StateRstate"r
SelectTargetRequest

session_id (	R	sessionId
	self_guid (RselfGuid
target_guid (R
targetGuid"À
SelectTargetResponse

session_id (	R	sessionId
	self_guid (RselfGuid
target_guid (R
targetGuid+
transport_written (RtransportWritten-
selection_observed (RselectionObserved2
observed_target_known (RobservedTargetKnown0
observed_target_guid (RobservedTargetGuid
phase (	Rphase"M
EngageNPCRequest
enabled (Renabled
target_guid (R
targetGuid"ç
EngageNPCState
active (Ractive
target_guid (R
targetGuid
phase (	Rphase,
lease_remaining_ms (RleaseRemainingMs"π
BehaviorReloadRequest

session_id (	R	sessionId
	self_guid (RselfGuid+
expected_revision (	RexpectedRevision
source (	Rsource
use_builtin (R
useBuiltin"‹
BehaviorStatus

session_id (	R	sessionId
	self_guid (RselfGuid
revision (	Rrevision,
last_good_revision (	RlastGoodRevision1
quarantined_revision (	RquarantinedRevision

last_error (	R	lastError 
evaluations (Revaluations
failures (Rfailures4
last_rejected_decision	 (	RlastRejectedDecision"◊
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
remaining_ms (RremainingMs"˜
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
harvest (Rharvest"∂
IdleInteractionRequest

session_id (	R	sessionId
	self_guid (RselfGuid
visit_id (RvisitId
owner (	Rowner
token (	Rtoken
lease_ms (RleaseMs"ó
InteractionRelease
owner (	Rowner
token (	Rtoken

session_id (	R	sessionId
	self_guid (RselfGuid
visit_id (RvisitId"D
InteractionObservation
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
arrival_distance (RarrivalDistance"–
PlanRouteLegRequest

session_id (	R	sessionId
	self_guid (RselfGuid<
expected_position_revision (RexpectedPositionRevision
map_id (RmapId
x (Rx
y (Ry
z (Rz"Ç
RouteLeg

session_id (	R	sessionId
	self_guid (RselfGuid
map_id (RmapId>
start (2(.warcraftbuddy.actions.v1.ClientPositionRstartD
endpoint (2(.warcraftbuddy.actions.v1.ClientPositionRendpoint
path_length (R
pathLength
point_count (R
pointCount!
goal_reached (RgoalReached8
server_position_revision	 (RserverPositionRevision"(
ExitCryptRequest
token (	Rtoken"_
InteractionLease
token (	Rtoken5
state (2.warcraftbuddy.actions.v1.StateRstate"ï

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
verified_legs (RverifiedLegs0
corpse_query_pending (RcorpseQueryPendingG

own_corpse (2(.warcraftbuddy.actions.v1.RecoveryCorpseR	ownCorpse8
ghost_movement_supported (RghostMovementSupported2
corpse_query_revision (RcorpseQueryRevision%
motion_pending (RmotionPendingZ
resurrection_offer (2+.warcraftbuddy.actions.v1.ResurrectionOfferRresurrectionOffer"Ñ
ResurrectionOffer
caster_guid (R
casterGuid
revision (Rrevision
death_epoch (R
deathEpoch
age_ms (RageMs-
response_attempted (RresponseAttempted)
response_written (RresponseWritten'
outcome_unknown (RoutcomeUnknown3
resurrection_sickness (RresurrectionSickness-
reclaim_delay_ms	 (H RreclaimDelayMsàB
_reclaim_delay_ms"⁄
RecoveryCorpse
guid (Rguid

owner_guid (R	ownerGuid
present (RpresentD
position (2(.warcraftbuddy.actions.v1.ClientPositionRposition
flags_known (R
flagsKnown
flags (Rflags"õ
CorpseApproachRequest

session_id (	R	sessionId
	self_guid (RselfGuid
death_epoch (R
deathEpoch<
expected_position_revision (RexpectedPositionRevision2
corpse_query_revision (RcorpseQueryRevision
range_yards (R
rangeYards
stop (Rstop"å
ReclaimCorpseRequest

session_id (	R	sessionId
	self_guid (RselfGuid
death_epoch (R
deathEpoch<
expected_position_revision (RexpectedPositionRevision
corpse_guid (R
corpseGuid8
expected_corpse_revision (RexpectedCorpseRevision"q
DeathActionRequest

session_id (	R	sessionId
	self_guid (RselfGuid
death_epoch (R
deathEpoch"˜

KnownSpell
spell_id (RspellId%
metadata_known (RmetadataKnown
name (	Rname
	family_id (RfamilyId
rank (Rrank!
mana_percent (RmanaPercent
range_yards (R
rangeYards
	self_only (RselfOnly8
automatic_buff_supported	 (RautomaticBuffSupported

capability
 (	R
capability
target_rule (	R
targetRule%
blocked_reason (	RblockedReason 
cast_time_ms (R
castTimeMs
	flat_cost (RflatCost

power_type (R	powerType.
required_item_class (RrequiredItemClass4
required_subclass_mask (RrequiredSubclassMask)
requires_stealth (RrequiresStealth'
requires_behind (RrequiresBehind,
created_item_entry (RcreatedItemEntry.
self_cast_supported (RselfCastSupported
	channeled (R	channeled4
generic_cast_supported (RgenericCastSupported3
classification_reason (	RclassificationReasonF
	mechanics (2(.warcraftbuddy.actions.v1.SpellMechanicsR	mechanics
passive (Rpassive"‡

SpellMechanics

attributes (R
attributes
stances (Rstances
stances_not (R
stancesNot
target_mask (R
targetMask
spell_focus (R
spellFocus
facing (Rfacing

conditions (R
conditions

family_set (R	familySet!
family_flags	 (RfamilyFlags$
cost_per_level
 (RcostPerLevel(
power_per_second (RpowerPerSecond
cast_min_ms (R	castMinMs$
cast_per_level (RcastPerLevel!
timing_known (RtimingKnown
range_known (R
rangeKnown*
min_hostile_yards (RminHostileYards,
min_friendly_yards (RminFriendlyYards*
max_hostile_yards (RmaxHostileYards,
max_friendly_yards (RmaxFriendlyYards
cooldown_ms (R
cooldownMs
category (Rcategory0
category_cooldown_ms (RcategoryCooldownMs
gcd_ms (RgcdMs%
duration_index (RdurationIndex6
required_inventory_mask (RrequiredInventoryMask
	rune_cost (RruneCostB
reagents (2&.warcraftbuddy.actions.v1.SpellReagentRreagents
totems (Rtotems?
effects (2%.warcraftbuddy.actions.v1.SpellEffectReffects2
requires_combo_points (RrequiresComboPoints-
interrupts_casting (RinterruptsCasting'
restores_health  (RrestoresHealth8
provides_water_breathing! (RprovidesWaterBreathing#
learned_level" (RlearnedLevel
dispel_type# (R
dispelType"=
SpellReagent
item_id (RitemId
count (Rcount"≤
SpellEffect
kind (Rkind*
target_selector_a (RtargetSelectorA*
target_selector_b (RtargetSelectorB
aura (Raura,
triggered_spell_id (RtriggeredSpellId
	period_ms (RperiodMs#
chain_targets (RchainTargets!
radius_index (RradiusIndex&
created_item_id	 (RcreatedItemId
points
 (Rpoints
dice (Rdice
misc (Rmisc(
points_per_level (RpointsPerLevel"û
SpellOperation
spell_id (RspellId
target_guid (R
targetGuid

capability (	R
capability
phase (	Rphase+
transport_written (RtransportWritten$
server_cast_go (RserverCastGo'
effect_observed (ReffectObserved,
created_item_entry (RcreatedItemEntry,
created_item_count	 (RcreatedItemCount,
item_push_observed
 (RitemPushObserved>
baseline_inventory_revision (RbaselineInventoryRevision>
observed_inventory_revision (RobservedInventoryRevision/
inventory_confirmed (RinventoryConfirmed'
channel_started (RchannelStarted#
channel_ended (RchannelEnded0
channel_remaining_ms (RchannelRemainingMs-
operation_revision (RoperationRevision
visit_id (RvisitId)
cancel_attempted (RcancelAttempted8
cancel_transport_written (RcancelTransportWritten4
cancel_outcome_unknown (RcancelOutcomeUnknown"„
ObservedAura
slot (Rslot
spell_id (RspellId
caster_guid (R
casterGuid
positive (Rpositive%
duration_known (RdurationKnown!
remaining_ms (RremainingMs
revision (Rrevision
negative (Rnegative%
metadata_known	 (RmetadataKnown
dispel_type
 (R
dispelType

aura_types (R	auraTypes"§
PlayerAuras
target_guid (R
targetGuid
complete (Rcomplete
revision (Rrevision<
auras (2&.warcraftbuddy.actions.v1.ObservedAuraRauras"ú
BuffOperation
sequence (Rsequence
spell_id (RspellId
target_guid (R
targetGuid
phase (	Rphase+
transport_written (RtransportWritten$
server_cast_go (RserverCastGo%
aura_confirmed (RauraConfirmed#
aura_revision (RauraRevision"Ω
SpellToolkitState'
spellbook_known (RspellbookKnown-
spellbook_revision (RspellbookRevision<
spells (2$.warcraftbuddy.actions.v1.KnownSpellRspellsH
player_auras (2%.warcraftbuddy.actions.v1.PlayerAurasRplayerAurasD
	last_buff (2'.warcraftbuddy.actions.v1.BuffOperationRlastBuff

session_id (	R	sessionId
	self_guid (RselfGuidF
targeted_resurrection_supported (RtargetedResurrectionSupported"$
State
	following (R	following
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
follow_lease_remaining_ms (RfollowLeaseRemainingMs

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
	fargodeepE (21.warcraftbuddy.actions.v1.FargodeepEncounterStateR	fargodeepQ
%client_movement_observation_confirmedG (R"clientMovementObservationConfirmedO
$client_movement_observation_failuresH (R!clientMovementObservationFailures
buffingd (Rbuffing5
buff_lease_remaining_mse (RbuffLeaseRemainingMs
buff_reasonf (	R
buffReason#
buff_attemptsg (RbuffAttempts'
buffs_confirmedh (RbuffsConfirmedD
	last_buffi (2'.warcraftbuddy.actions.v1.BuffOperationRlastBuff

session_idj (	R	sessionId
	self_guidk (RselfGuidZ
last_spell_operationl (2(.warcraftbuddy.actions.v1.SpellOperationRlastSpellOperationG

engage_npcm (2(.warcraftbuddy.actions.v1.EngageNPCStateR	engageNpcV
protected_targetsn (2).warcraftbuddy.actions.v1.ProtectedTargetRprotectedTargets:
protected_target_revisiono (RprotectedTargetRevision<
target_protection_overflowp (RtargetProtectionOverflowG
 crowd_control_approach_supportedr (RcrowdControlApproachSupportedP
crowd_controls (2+.warcraftbuddy.actions.v1.CrowdControlStateRcrowdControlZ
last_melee_operationt (2(.warcraftbuddy.actions.v1.MeleeOperationRlastMeleeOperationV
target_movementu (2-.warcraftbuddy.actions.v1.TargetMovementStateRtargetMovementP
body_movementv (2+.warcraftbuddy.actions.v1.BodyMovementStateRbodyMovementS
movement_ownerw (2,.warcraftbuddy.actions.v1.MovementOwnerStateRmovementOwner`
last_transport_failurex (2*.warcraftbuddy.actions.v1.TransportFailureRlastTransportFailurel
last_follow_policy_failurey (2/.warcraftbuddy.actions.v1.PolicyDecisionFailureRlastFollowPolicyFailureJJJFGJIdJqrRhealingRhealing_lease_remaining_ms"Ö
TransportFailure
sequence (Rsequence
opcode (Ropcode
error (	Rerror'
outcome_unknown (RoutcomeUnknown"à
ProtectedTarget
target_guid (R
targetGuid
caster_guid (R
casterGuid
spell_id (RspellId%
duration_known (RdurationKnown!
remaining_ms (RremainingMs
revision (Rrevision
slot (Rslot

capability (	R
capability"´
CrowdControlState
active (Ractive
target_guid (R
targetGuid
spell_id (RspellId
phase (	Rphase,
lease_remaining_ms (RleaseRemainingMs"°
PolymorphRequest

session_id (	R	sessionId
spell_id (RspellId
target_guid (R
targetGuid
approach (Rapproach
cancel (Rcancel"õ

SapRequest

session_id (	R	sessionId
spell_id (RspellId
target_guid (R
targetGuid
approach (Rapproach
cancel (Rcancel"j
CreateItemRequest

session_id (	R	sessionId
	self_guid (RselfGuid
spell_id (RspellId"h
CastSelfRequest

session_id (	R	sessionId
	self_guid (RselfGuid
spell_id (RspellId"â
CastHealRequest

session_id (	R	sessionId
	self_guid (RselfGuid
target_guid (R
targetGuid
spell_id (RspellId"å
CastOffenseRequest

session_id (	R	sessionId
	self_guid (RselfGuid
target_guid (R
targetGuid
spell_id (RspellId"ö
MeleeRequest

session_id (	R	sessionId
	self_guid (RselfGuid
target_guid (R
targetGuid
stop (Rstop
visit_id (RvisitId"∆
MeleeOperation
	self_guid (RselfGuid
target_guid (R
targetGuid
enabled (Renabled
phase (	Rphase+
transport_written (RtransportWritten
visit_id (RvisitId"–
HealingRank
spell_id (RspellId
rank (H Rrankà
cost (HRcostà
amount (Ramount
cast_ms (HRcastMsà
level (HRlevelà 
	min_range (HRminRangeà 
	max_range (HRmaxRangeà
	self_only	 (RselfOnlyB
_rankB
_costB

_cast_msB
_levelB

_min_rangeB

_max_range"«
HealingPatient
guid (Rguid
health (Rhealth

max_health (R	maxHealth
distance (H Rdistanceà
is_owner (RisOwner!
under_attack (RunderAttackB
	_distance"[
HealingCaster
guid (Rguid
ready (Rready
mana (H RmanaàB
_mana"¥
HealingOptionsResponse

session_id (	R	sessionId?
caster (2'.warcraftbuddy.actions.v1.HealingCasterRcaster

host_owned (R	hostOwnedD
patients (2(.warcraftbuddy.actions.v1.HealingPatientRpatients=
spells (2%.warcraftbuddy.actions.v1.HealingRankRspells
reason (	Rreason"©
SelfManaCaster
guid (Rguid
present (Rpresent
alive_known (R
aliveKnown
alive (Ralive
	map_known (RmapKnown%
position_known (RpositionKnown
class_known (R
classKnown
class_id (RclassId
power_known	 (R
powerKnown"

power_type
 (H R	powerTypeà
power (HRpowerà 
	max_power (HRmaxPowerà!
combat_known (RcombatKnown 
	in_combat (HRinCombatà%
movement_known (RmovementKnown
moving (HRmovingà#
channel_known (RchannelKnown-
channel_spell_id (HRchannelSpellIdàB
_power_typeB
_powerB

_max_powerB

_in_combatB	
_movingB
_channel_spell_id"J
SelfManaActivity
known (Rknown
idle (H RidleàB
_idle"ß
SelfManaThreat
known (Rknown*
attacking_self (H RattackingSelfà,
attacking_party (HRattackingPartyàB
_attacking_selfB
_attacking_party"§
SelfManaSpell
spell_id (RspellId
	family_id (RfamilyId
rank (H Rrankà%
metadata_known (RmetadataKnown.
self_cast_supported (RselfCastSupported
	channeled (R	channeled
ready_known (R
readyKnown
ready (HRreadyàB
_rankB
_ready"Æ
SelfManaObservationResponse

session_id (	R	sessionId
	self_guid (RselfGuid'
spellbook_known (RspellbookKnown@
caster (2(.warcraftbuddy.actions.v1.SelfManaCasterRcasterF
activity (2*.warcraftbuddy.actions.v1.SelfManaActivityRactivity@
threat (2(.warcraftbuddy.actions.v1.SelfManaThreatRthreat?
spells (2'.warcraftbuddy.actions.v1.SelfManaSpellRspells

host_owned (R	hostOwned"ﬁ
EncounterObservationResponse

session_id (	R	sessionId
	self_guid (RselfGuid
visit_id (RvisitId
map_id (H RmapIdà"

host_owned (HR	hostOwnedà
reason (	Rreason>
party (2(.warcraftbuddy.actions.v1.EncounterPartyRparty]
players (2C.warcraftbuddy.actions.v1.EncounterObservationResponse.PlayersEntryRplayers]
targets	 (2C.warcraftbuddy.actions.v1.EncounterObservationResponse.TargetsEntryRtargetsf

engagement
 (2F.warcraftbuddy.actions.v1.EncounterObservationResponse.EngagementEntryR
engagementR
protected_auras (2).warcraftbuddy.actions.v1.ProtectedTargetRprotectedAuras<
target_protection_overflow (RtargetProtectionOverflowZ
spells (2B.warcraftbuddy.actions.v1.EncounterObservationResponse.SpellsEntryRspellsg
control_capabilities (24.warcraftbuddy.actions.v1.EncounterControlCapabilityRcontrolCapabilities0
observed_owner_focus (RobservedOwnerFocusJ
healing (20.warcraftbuddy.actions.v1.HealingOptionsResponseRhealing<
world_observation_revision (RworldObservationRevision-
spellbook_revision (RspellbookRevision'
spellbook_known (RspellbookKnown*
host_requested (HRhostRequestedà
melee_known (R
meleeKnown7
active_melee_target_guid (RactiveMeleeTargetGuidV
target_movement (2-.warcraftbuddy.actions.v1.TargetMovementStateRtargetMovementD
support (2*.warcraftbuddy.actions.v1.EncounterSupportRsupportO

navigation (2/.warcraftbuddy.actions.v1.NavigationObservationR
navigation`
last_transport_failure (2*.warcraftbuddy.actions.v1.TransportFailureRlastTransportFailureC
recovery (2'.warcraftbuddy.actions.v1.RecoveryStateRrecoveryR
interaction (20.warcraftbuddy.actions.v1.InteractionObservationRinteractione
PlayersEntry
key (	Rkey?
value (2).warcraftbuddy.actions.v1.EncounterPlayerRvalue:8g
TargetsEntry
key (	RkeyA
value (2+.warcraftbuddy.actions.v1.EncounterCreatureRvalue:8l
EngagementEntry
key (	RkeyC
value (2-.warcraftbuddy.actions.v1.EncounterEngagementRvalue:8c
SpellsEntry
key (	Rkey>
value (2(.warcraftbuddy.actions.v1.EncounterSpellRvalue:8B	
_map_idB
_host_ownedB
_host_requested"Î
BodyMovementRequest

session_id (	R	sessionId
	self_guid (RselfGuid
visit_id (RvisitId
sequence_id (R
sequenceId'
bearing_radians (RbearingRadians
duration_ms (R
durationMs
jump (Rjump"É
BodyMovementState
sequence_id (R
sequenceId
visit_id (RvisitId
phase (	Rphase+
transport_written (RtransportWritten-
position_estimated (RpositionEstimated
x (Rx
y (Ry
z (Rz
detail	 (	Rdetail"«
FollowFormationRequest

session_id (	R	sessionId
	self_guid (RselfGuid
visit_id (RvisitId

owner_guid (R	ownerGuid6
owner_position_revision (RownerPositionRevision
x (Rx
y (Ry
z (Rz 
orientation	 (Rorientation%
start_distance
 (RstartDistance#
stop_distance (RstopDistance
ready (Rready
hold (Rhold'
follow_revision (RfollowRevision
phase (	Rphase"Ñ
NavigationObservation6
body_movement_supported (RbodyMovementSupported
	following (R	following

owner_guid (R	ownerGuid
phase (	Rphase
detail (	Rdetail'
route_available (RrouteAvailable-
motion_unconfirmed (RmotionUnconfirmed

goal_known (R	goalKnown
goal_x	 (RgoalX
goal_y
 (RgoalY
goal_z (RgoalZP
body_movement (2+.warcraftbuddy.actions.v1.BodyMovementStateRbodyMovementS
movement_owner (2,.warcraftbuddy.actions.v1.MovementOwnerStateRmovementOwner<
follow_formation_supported (RfollowFormationSupported#
owner_arrival (RownerArrivalL
follow_owner (2).warcraftbuddy.actions.v1.EncounterPlayerRfollowOwner'
follow_revision (RfollowRevision@
ground_navigation_configured (RgroundNavigationConfiguredI
portal (21.warcraftbuddy.actions.v1.PortalFollowObservationRportal"7
PortalPoint
x (Rx
y (Ry
z (Rz"ƒ
PortalFollowObservation)
witness_revision (RwitnessRevision&
source_visit_id (RsourceVisitId'
follow_revision (RfollowRevision

owner_guid (R	ownerGuid"
source_map_id (RsourceMapId,
destination_map_id (RdestinationMapId

trigger_id (R	triggerId=
ground (2%.warcraftbuddy.actions.v1.PortalPointRground>
start	 (2(.warcraftbuddy.actions.v1.ClientPositionRstart
catching_up
 (R
catchingUp
notified (Rnotified!
owner_absent (RownerAbsent,
sighting_age_known (RsightingAgeKnown&
sighting_age_ms (RsightingAgeMs6
disappearance_age_known (RdisappearanceAgeKnown0
disappearance_age_ms (RdisappearanceAgeMs"Á
PortalCatchupRequest

session_id (	R	sessionId
	self_guid (RselfGuid
visit_id (RvisitId

owner_guid (R	ownerGuid'
follow_revision (RfollowRevision)
witness_revision (RwitnessRevision>
start (2(.warcraftbuddy.actions.v1.ClientPositionRstartE

candidates (2%.warcraftbuddy.actions.v1.PortalPointR
candidates"≠
EncounterParty3
group (2.warcraftbuddy.party.v1.GroupRgroupJ
raid_targets (2'.warcraftbuddy.party.v1.RaidTargetStateRraidTargets
revision (Rrevision"û
EncounterPlayer
guid (Rguid
present (Rpresent
	map_known (RmapKnown
map_id (H RmapIdà
alive_known (R
aliveKnown
alive (HRaliveà!
health_known (RhealthKnown
health (Rhealth

max_health	 (R	maxHealth)
faction_template
 (RfactionTemplate%
position_known (RpositionKnown+
position_revision (RpositionRevision
x (Rx
y (Ry
z (Rz 
orientation (Rorientation
class_known (R
classKnown
class_id (RclassId

race_known (R	raceKnown
race_id (RraceId
level_known (R
levelKnown
level (Rlevel
power_known (R
powerKnown

power_type (R	powerType
power (Rpower
	max_power (RmaxPower%
movement_known (RmovementKnown
moving (Rmoving!
combat_known (RcombatKnown
	in_combat (RinCombat
combo_known (R
comboKnown*
combo_target_guid  (RcomboTargetGuid!
combo_points! (RcomboPoints!
player_flags" (RplayerFlagsB	
_map_idB
_alive"≠
EncounterCreature
guid (Rguid
entry (Rentry
present (Rpresent
	map_known (RmapKnown
map_id (H RmapIdà
alive_known (R
aliveKnown
alive (HRaliveà!
health_known (RhealthKnown
health	 (Rhealth

max_health
 (R	maxHealth

attackable (R
attackable
engaged (BRengaged
	protected (R	protected%
position_known (RpositionKnown+
position_revision (RpositionRevision
x (Rx
y (Ry
z (RzJ

trajectory (2*.warcraftbuddy.world.v1.CreatureTrajectoryR
trajectory(
unit_flags_known (RunitFlagsKnown

unit_flags (R	unitFlags)
faction_template (RfactionTemplate
melee_ready (R
meleeReady.
own_damage_families (RownDamageFamilies 
orientation (Rorientationc
attack_observation (24.warcraftbuddy.actions.v1.EncounterAttackObservationRattackObservationB	
_map_idB
_alive"ª
EncounterAttackObservation!
victim_known (RvictimKnown
victim_guid (R
victimGuid
age_ms (RageMs!
target_known (RtargetKnown
target_guid (R
targetGuid"M
EncounterEngagement
victim_guid (R
victimGuid
age_ms (RageMs"ö
EncounterSpell
known (Rknown
ready (Rready

capability (	R
capability%
metadata_known (RmetadataKnown
	family_id (RfamilyId
rank (Rrank
level (Rlevel
name (	Rname 
cast_time_ms	 (R
castTimeMs
cost
 (Rcost

power_type (R	powerType
	min_range (RminRange
	max_range (RmaxRange#
direct_damage (RdirectDamageZ
periodic_damage (21.warcraftbuddy.actions.v1.EncounterPeriodicDamageRperiodicDamage
cooldown_ms (R
cooldownMs0
category_cooldown_ms (RcategoryCooldownMs,
global_cooldown_ms (RglobalCooldownMs
	available (R	available.
self_cast_supported (RselfCastSupported&
self_aura_known (RselfAuraKnown(
self_aura_active (RselfAuraActive#
weapon_strike (RweaponStrike'
requires_behind (RrequiresBehind)
requires_stealth (RrequiresStealth2
requires_combo_points (RrequiresComboPoints4
combo_points_generated (RcomboPointsGenerated8
weapon_damage_multiplier (RweaponDamageMultiplier.
weapon_bonus_damage (RweaponBonusDamage,
damage_model_known (RdamageModelKnown'
requires_facing (RrequiresFacing0
friendly_dispel_mask  (RfriendlyDispelMask
target_rule! (	R
targetRule

family_set" (R	familySet!
family_flags# (RfamilyFlagsP
aura_effects$ (2-.warcraftbuddy.actions.v1.EncounterAuraEffectRauraEffects"m
EncounterAuraEffect
kind (Rkind
a (Ra
b (Rb
aura (Raura
misc (Rmisc"^
EncounterPeriodicDamage&
damage_per_tick (RdamagePerTick
	period_ms (RperiodMs"˝
ApproachTargetRequest

session_id (	R	sessionId
	self_guid (RselfGuid
target_guid (R
targetGuid
visit_id (RvisitId
range_yards (R
rangeYards
stop (Rstop
require_los (R
requireLos
behind (Rbehind"◊
TargetMovementState
target_guid (R
targetGuid
visit_id (RvisitId
range_yards (R
rangeYards
require_los (R
requireLos
behind (Rbehind
phase (	Rphase
ready (Rready"Ã
EncounterSupport(
caster_in_combat (RcasterInCombatR
resurrections (2,.warcraftbuddy.actions.v1.ResurrectionOptionRresurrectionsI

interrupts (2).warcraftbuddy.actions.v1.InterruptOptionR
interruptsH
player_auras (2%.warcraftbuddy.actions.v1.PlayerAurasRplayerAuras[
aura_spells (2:.warcraftbuddy.actions.v1.EncounterSupport.AuraSpellsEntryR
auraSpells_
control_recovery (24.warcraftbuddy.actions.v1.ControlRecoveryObservationRcontrolRecoveryg
AuraSpellsEntry
key (Rkey>
value (2(.warcraftbuddy.actions.v1.EncounterSpellRvalue:8"§
ControlRecoveryObservation#
fleeing_known (RfleeingKnown
fleeing (RfleeingE

fear_auras (2&.warcraftbuddy.actions.v1.ObservedAuraR	fearAurasF
options (2,.warcraftbuddy.actions.v1.ControlBreakOptionRoptions8
attempted_aura_revisions (RattemptedAuraRevisions"Ê
ControlBreakOption
spell_id (RspellId
	item_guid (RitemGuid
	item_slot (RitemSlot
ready (Rready
cooldown_ms (R
cooldownMs0
category_cooldown_ms (RcategoryCooldownMs
rank (Rrank"å
BreakControlRequest

session_id (	R	sessionId
	self_guid (RselfGuid
spell_id (RspellId
	item_guid (RitemGuid
	item_slot (RitemSlot
	aura_slot (RauraSlot#
aura_revision (RauraRevision"
aura_spell_id (RauraSpellId"÷
ResurrectionOption!
patient_guid (RpatientGuid(
cast_target_guid (RcastTargetGuid
spell_id (RspellId
ready (Rready!
can_approach (RcanApproach
range_yards (R
rangeYards"Ø
InterruptOption
target_guid (R
targetGuid
spell_id (RspellId$
enemy_spell_id (RenemySpellId#
cast_revision (RcastRevision!
remaining_ms (RremainingMs
healing (Rhealing
ready (Rready!
can_approach (RcanApproach
range_yards	 (R
rangeYards"≥
InterruptCastRequest

session_id (	R	sessionId
	self_guid (RselfGuid
target_guid (R
targetGuid
spell_id (RspellId#
cast_revision (RcastRevision"â
CastBuffRequest

session_id (	R	sessionId
	self_guid (RselfGuid
target_guid (R
targetGuid
spell_id (RspellId"Ú
CastCleanseRequest

session_id (	R	sessionId
	self_guid (RselfGuid
target_guid (R
targetGuid
spell_id (RspellId
	aura_slot (RauraSlot#
aura_revision (RauraRevision"
aura_spell_id (RauraSpellId"d
EncounterControlCapability
guid (Rguid
	technique (	R	technique
ready (Rready"é
ResurrectOnceRequest

session_id (	R	sessionId
	self_guid (RselfGuid
target_guid (R
targetGuid
spell_id (RspellId"—
AcceptResurrectionRequest

session_id (	R	sessionId
	self_guid (RselfGuid
caster_guid (R
casterGuid
death_epoch (R
deathEpoch6
expected_offer_revision (RexpectedOfferRevision"Q
PickLockRequest

session_id (	R	sessionId
target_guid (R
targetGuid"Ä
PolicyDecisionFailure
mode (	Rmode
stage (	Rstage'
source_revision (	RsourceRevision
error (	Rerror"·
FlightpathQueryRequest

session_id (	R	sessionId
	self_guid (RselfGuid
visit_id (RvisitId
token (	Rtoken
npc_guid (RnpcGuid
mode (	Rmode+
expected_revision (RexpectedRevision"Œ
FlightpathOperation
revision (Rrevision

session_id (	R	sessionId
	self_guid (RselfGuid
visit_id (RvisitId
npc_guid (RnpcGuid
mode (	Rmode
pending (Rpending+
transport_written (RtransportWritten+
transport_unknown	 (RtransportUnknown!
status_known
 (RstatusKnown

node_known (R	nodeKnown8
learning_notice_observed (RlearningNoticeObserved
learned (Rlearned

menu_known (R	menuKnown
source_node (R
sourceNode
known_nodes (R
knownNodes
phase (	Rphase
error (	Rerror"ä
FlightpathObservationResponse

session_id (	R	sessionId
	self_guid (RselfGuid
visit_id (RvisitId
revision (Rrevision
visit_bound (R
visitBoundK
	operation (2-.warcraftbuddy.actions.v1.FlightpathOperationR	operation/
activation_revision (RactivationRevisionW

activation (27.warcraftbuddy.actions.v1.FlightpathActivationOperationR
activation"÷
FlightpathActivationRequest

session_id (	R	sessionId
	self_guid (RselfGuid
visit_id (RvisitId
token (	Rtoken
npc_guid (RnpcGuid
source_node (R
sourceNode)
destination_node (RdestinationNode#
menu_revision (RmenuRevision+
expected_revision	 (RexpectedRevision8
expected_copper_revision
 (RexpectedCopperRevision'
expected_copper (RexpectedCopper.
accept_unknown_fare (RacceptUnknownFare"Í
FlightpathActivationOperation
revision (Rrevision

session_id (	R	sessionId
	self_guid (RselfGuid
visit_id (RvisitId
npc_guid (RnpcGuid#
menu_revision (RmenuRevision
source_node (R
sourceNode)
destination_node (RdestinationNode
pending	 (Rpending+
transport_written
 (RtransportWritten+
transport_unknown (RtransportUnknown
phase (	Rphase
error (	Rerror
reply_known (R
replyKnown

reply_code (R	replyCode/
activation_accepted (RactivationAccepted,
money_before_known (RmoneyBeforeKnown.
money_before_copper (RmoneyBeforeCopper2
money_before_revision (RmoneyBeforeRevision&
live_fare_known (RliveFareKnown(
live_route_known (RliveRouteKnown0
flight_started_known (RflightStartedKnown#
arrival_known (RarrivalKnown"5
FlightpathReferenceRequest
node_id (RnodeId"∑
FlightpathReferenceNode
node_id (RnodeId
map_id (RmapId
x (Rx
y (Ry
z (Rz
name (	Rname.
map_reference_known (RmapReferenceKnown"è
FlightpathReferencePath
path_id (RpathId
source_node (R
sourceNode)
destination_node (RdestinationNode2
reference_base_copper (RreferenceBaseCopper2
reference_price_known (RreferencePriceKnown'
endpoints_known (RendpointsKnown"û
FlightpathReferenceInfo
format (Rformat#
claimed_build (RclaimedBuild
name_locale (	R
nameLocale

repository (	R
repository'
upstream_commit (	RupstreamCommit!
nodes_sha256 (	RnodesSha256!
paths_sha256 (	RpathsSha256

map_sha256 (	R	mapSha256
node_url	 (	RnodeUrl
path_url
 (	RpathUrl

map_source (	R	mapSource

node_count (R	nodeCount

path_count (R	pathCount#
unnamed_nodes (RunnamedNodes%
unmapped_nodes (RunmappedNodes4
invalid_endpoint_paths (RinvalidEndpointPaths8
duplicate_endpoint_pairs (RduplicateEndpointPairs(
zero_price_paths (RzeroPricePaths"ù
FlightpathReferenceResponse%
reference_only (RreferenceOnly

node_found (R	nodeFoundE
node (21.warcraftbuddy.actions.v1.FlightpathReferenceNodeRnodeX
outgoing_paths (21.warcraftbuddy.actions.v1.FlightpathReferencePathRoutgoingPathsE
info (21.warcraftbuddy.actions.v1.FlightpathReferenceInfoRinfo&
live_fare_known (RliveFareKnown(
live_route_known (RliveRouteKnown2˚+
BuddyActionsX
	CastSpell*.warcraftbuddy.actions.v1.CastSpellRequest.warcraftbuddy.actions.v1.State\
CancelSpell,.warcraftbuddy.actions.v1.CancelSpellRequest.warcraftbuddy.actions.v1.StateÇ
FlightpathReference4.warcraftbuddy.actions.v1.FlightpathReferenceRequest5.warcraftbuddy.actions.v1.FlightpathReferenceResponseh
FlightpathObservation.google.protobuf.Empty7.warcraftbuddy.actions.v1.FlightpathObservationResponse|
QueryFlightpath0.warcraftbuddy.actions.v1.FlightpathQueryRequest7.warcraftbuddy.actions.v1.FlightpathObservationResponseÑ
ActivateFlightpath5.warcraftbuddy.actions.v1.FlightpathActivationRequest7.warcraftbuddy.actions.v1.FlightpathObservationResponsem
SelectTarget-.warcraftbuddy.actions.v1.SelectTargetRequest..warcraftbuddy.actions.v1.SelectTargetResponseb
ApproachTarget/.warcraftbuddy.actions.v1.ApproachTargetRequest.warcraftbuddy.actions.v1.StateZ
MoveBody-.warcraftbuddy.actions.v1.BodyMovementRequest.warcraftbuddy.actions.v1.State`
InterruptCast..warcraftbuddy.actions.v1.InterruptCastRequest.warcraftbuddy.actions.v1.State^
BreakControl-.warcraftbuddy.actions.v1.BreakControlRequest.warcraftbuddy.actions.v1.State^
SetFollowFormation0.warcraftbuddy.actions.v1.FollowFormationRequest.google.protobuf.EmptyZ
SetPortalCatchup..warcraftbuddy.actions.v1.PortalCatchupRequest.google.protobuf.EmptyR
Follow'.warcraftbuddy.actions.v1.ToggleRequest.warcraftbuddy.actions.v1.StateR
Assist'.warcraftbuddy.actions.v1.ToggleRequest.warcraftbuddy.actions.v1.StateX
	EngageNPC*.warcraftbuddy.actions.v1.EngageNPCRequest.warcraftbuddy.actions.v1.StateC
HealOnce.google.protobuf.Empty.warcraftbuddy.actions.v1.StateA
Status.google.protobuf.Empty.warcraftbuddy.actions.v1.StateQ
Buffs'.warcraftbuddy.actions.v1.ToggleRequest.warcraftbuddy.actions.v1.StateS
SpellToolkit.google.protobuf.Empty+.warcraftbuddy.actions.v1.SpellToolkitStatek
ReloadBehavior/.warcraftbuddy.actions.v1.BehaviorReloadRequest(.warcraftbuddy.actions.v1.BehaviorStatusL
Behavior.google.protobuf.Empty(.warcraftbuddy.actions.v1.BehaviorStatuso
AcquireMovement..warcraftbuddy.actions.v1.MovementOwnerRequest,.warcraftbuddy.actions.v1.MovementOwnerLeaseb
ReleaseMovement..warcraftbuddy.actions.v1.MovementOwnerRelease.warcraftbuddy.actions.v1.Staten
AcquireInteraction,.warcraftbuddy.actions.v1.InteractionRequest*.warcraftbuddy.actions.v1.InteractionLeasev
AcquireIdleInteraction0.warcraftbuddy.actions.v1.IdleInteractionRequest*.warcraftbuddy.actions.v1.InteractionLeasec
ReleaseInteraction,.warcraftbuddy.actions.v1.InteractionRelease.warcraftbuddy.actions.v1.State^
ApproachLoot-.warcraftbuddy.actions.v1.ApproachLootRequest.warcraftbuddy.actions.v1.State`
ApproachQuest..warcraftbuddy.actions.v1.ApproachQuestRequest.warcraftbuddy.actions.v1.State^
TravelQuest..warcraftbuddy.actions.v1.ApproachQuestRequest.warcraftbuddy.actions.v1.State`
TravelTrainer..warcraftbuddy.actions.v1.ApproachQuestRequest.warcraftbuddy.actions.v1.State_
TravelVendor..warcraftbuddy.actions.v1.ApproachQuestRequest.warcraftbuddy.actions.v1.StateV
TravelTo).warcraftbuddy.actions.v1.TravelToRequest.warcraftbuddy.actions.v1.Statea
PlanRouteLeg-.warcraftbuddy.actions.v1.PlanRouteLegRequest".warcraftbuddy.actions.v1.RouteLegX
	ExitCrypt*.warcraftbuddy.actions.v1.ExitCryptRequest.warcraftbuddy.actions.v1.StateT
Practice'.warcraftbuddy.actions.v1.ToggleRequest.warcraftbuddy.actions.v1.Statej
FargodeepEncounter3.warcraftbuddy.actions.v1.FargodeepEncounterRequest.warcraftbuddy.actions.v1.StateT
Recovery'.warcraftbuddy.actions.v1.ToggleRequest.warcraftbuddy.actions.v1.State^
ReleaseSpirit,.warcraftbuddy.actions.v1.DeathActionRequest.warcraftbuddy.actions.v1.State\
QueryCorpse,.warcraftbuddy.actions.v1.DeathActionRequest.warcraftbuddy.actions.v1.Stateb
ApproachCorpse/.warcraftbuddy.actions.v1.CorpseApproachRequest.warcraftbuddy.actions.v1.State`
ReclaimCorpse..warcraftbuddy.actions.v1.ReclaimCorpseRequest.warcraftbuddy.actions.v1.StateC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.Empty>
BeforeLogout.google.protobuf.Empty.google.protobuf.Empty`
ResurrectOnce..warcraftbuddy.actions.v1.ResurrectOnceRequest.warcraftbuddy.actions.v1.Statej
AcceptResurrection3.warcraftbuddy.actions.v1.AcceptResurrectionRequest.warcraftbuddy.actions.v1.StateV
PickLock).warcraftbuddy.actions.v1.PickLockRequest.warcraftbuddy.actions.v1.StateZ

CreateItem+.warcraftbuddy.actions.v1.CreateItemRequest.warcraftbuddy.actions.v1.StateX
	Polymorph*.warcraftbuddy.actions.v1.PolymorphRequest.warcraftbuddy.actions.v1.StateL
Sap$.warcraftbuddy.actions.v1.SapRequest.warcraftbuddy.actions.v1.StateV
CastSelf).warcraftbuddy.actions.v1.CastSelfRequest.warcraftbuddy.actions.v1.StateZ
HealingOptions.google.protobuf.Empty0.warcraftbuddy.actions.v1.HealingOptionsResponsef
EncounterObservation.google.protobuf.Empty6.warcraftbuddy.actions.v1.EncounterObservationResponseV
CastHeal).warcraftbuddy.actions.v1.CastHealRequest.warcraftbuddy.actions.v1.State\
CastOffense,.warcraftbuddy.actions.v1.CastOffenseRequest.warcraftbuddy.actions.v1.State\
CastCleanse,.warcraftbuddy.actions.v1.CastCleanseRequest.warcraftbuddy.actions.v1.StateV
CastBuff).warcraftbuddy.actions.v1.CastBuffRequest.warcraftbuddy.actions.v1.StateP
Melee&.warcraftbuddy.actions.v1.MeleeRequest.warcraftbuddy.actions.v1.Stated
SelfManaObservation.google.protobuf.Empty5.warcraftbuddy.actions.v1.SelfManaObservationResponseBDZBgithub.com/agent-wow/agent-wow/modules/buddy-actions/api;actionsv1bproto3
Ä]
'modules/buddy-inventory/inventory.protowarcraftbuddy.inventory.v1api/module/v1/session.protogoogle/protobuf/empty.protomodules/buddy-world/world.proto#modules/buddy-actions/actions.proto"É
ItemRef
bag (Rbag
slot (Rslot
	item_guid (RitemGuid
entry (Rentry
stack_count (R
stackCount"¸
ItemEligibility7
item (2#.warcraftbuddy.inventory.v1.ItemRefRitem%
metadata_known (RmetadataKnown5
safe_junk_sale_eligible (RsafeJunkSaleEligible3
unit_sell_price_copper (RunitSellPriceCopper

normal_bag (R	normalBag'
container_slots (RcontainerSlots%
blocked_reason (	RblockedReason
name (	Rname.
gear_equip_eligible	 (RgearEquipEligible.
gear_equipment_slot
 (RgearEquipmentSlot=
expected_equipped_item_guid (RexpectedEquippedItemGuid1
conservative_upgrade (RconservativeUpgrade.
gear_blocked_reason (	RgearBlockedReasonN

consumable (2..warcraftbuddy.inventory.v1.ConsumableMetadataR
consumableH

gear_facts (2).warcraftbuddy.inventory.v1.GearItemFactsR	gearFacts"ë

	Operation

request_id (	R	requestId
kind (	Rkind
status (	Rstatus
reason (	Rreason;
source (2#.warcraftbuddy.inventory.v1.ItemRefRsource'
destination_bag (RdestinationBag)
destination_slot (RdestinationSlot%
equipment_slot (RequipmentSlot
vendor_guid	 (R
vendorGuid3
unit_sell_price_copper
 (RunitSellPriceCopper4
expected_credit_copper (RexpectedCreditCopper/
transport_attempted (RtransportAttempted+
transport_written (RtransportWritten/
inventory_confirmed (RinventoryConfirmed/
equipment_confirmed (RequipmentConfirmed)
copper_confirmed (RcopperConfirmed2
server_error_observed (RserverErrorObserved!
server_error (RserverError%
lease_released (RleaseReleased>
baseline_inventory_revision (RbaselineInventoryRevision>
observed_inventory_revision (RobservedInventoryRevision8
baseline_copper_revision (RbaselineCopperRevision8
observed_copper_revision (RobservedCopperRevision'
baseline_copper (RbaselineCopper'
observed_copper (RobservedCopper>
baseline_equipment_revision (RbaselineEquipmentRevision>
observed_equipment_revision (RobservedEquipmentRevision.
displaced_item_guid (RdisplacedItemGuid"ë
State

session_id (	R	sessionId
	self_guid (RselfGuid
revision (Rrevision'
inventory_known (RinventoryKnown-
inventory_revision (RinventoryRevisionK
inventory_bags (2$.warcraftbuddy.world.v1.InventoryBagRinventoryBags!
copper_known (RcopperKnown
copper (Rcopper'
copper_revision	 (RcopperRevision
active
 (RactiveC
	operation (2%.warcraftbuddy.inventory.v1.OperationR	operationA
items (2+.warcraftbuddy.inventory.v1.ItemEligibilityRitems%
blocked_reason (	RblockedReason)
transport_writes (RtransportWrites+
malformed_packets (RmalformedPackets

limitation (	R
limitation
closing (Rclosing'
equipment_known (RequipmentKnown-
equipment_revision (RequipmentRevisionH
	equipment (2*.warcraftbuddy.world.v1.EquipmentSlotStateR	equipment-
drinking_supported (RdrinkingSupportedT
last_group_roll (2,.warcraftbuddy.inventory.v1.GroupRollReceiptRlastGroupRoll.
food_buff_supported (RfoodBuffSupported.
buff_food_available (RbuffFoodAvailable-
mana_drink_count (H RmanaDrinkCountàY
last_consumable_use( (2).warcraftbuddy.inventory.v1.ConsumableUseRlastConsumableUse\
consumable_context) (2-.warcraftbuddy.inventory.v1.ConsumableContextRconsumableContextJ
gear_context* (2'.warcraftbuddy.inventory.v1.GearContextRgearContextB
_mana_drink_count"4
GearStat
type (Rtype
value (Rvalue"X

GearDamage
minimum (Rminimum
maximum (Rmaximum
school (Rschool"¶
GearTemplate
item_id (RitemId
	available (R	available
name (	Rname

item_class (R	itemClass
subclass (Rsubclass%
inventory_type (RinventoryType
quality (Rquality
armor (Rarmor:
stats	 (2$.warcraftbuddy.inventory.v1.GearStatRstats@
damages
 (2&.warcraftbuddy.inventory.v1.GearDamageRdamages
delay_ms (RdelayMs 
resistances (Rresistances4
armor_comparison_known (RarmorComparisonKnown
flags (Rflags
flags2 (Rflags2
	has_spell (RhasSpell1
scaling_distribution (RscalingDistribution#
scaling_value (RscalingValue%
required_level (RrequiredLevel"∫
GearDestination%
equipment_slot (RequipmentSlot=
expected_equipped_item_guid (RexpectedEquippedItemGuid
eligible (Religible%
blocked_reason (	RblockedReason"¨
GearItemFacts-
enhancements_known (RenhancementsKnown)
has_enhancements (RhasEnhancements)
durability_known (RdurabilityKnown

durability (R
durability%
max_durability (RmaxDurabilityO
destinations (2+.warcraftbuddy.inventory.v1.GearDestinationRdestinations"¬
GearContext
class_known (R
classKnown
class_id (RclassId

race_known (R	raceKnown
race_id (RraceId
level_known (R
levelKnown
level (RlevelT
	templates (26.warcraftbuddy.inventory.v1.GearContext.TemplatesEntryR	templates'
policy_revision (	RpolicyRevision!
policy_error	 (	RpolicyErrorf
TemplatesEntry
key (	Rkey>
value (2(.warcraftbuddy.inventory.v1.GearTemplateRvalue:8"≥
GroupRollReceipt
	roll_guid (RrollGuid

item_entry (R	itemEntry
	loot_slot (RlootSlot!
offered_mask (RofferedMask
choice (	Rchoice
phase (	Rphase
reason (	Rreason+
transport_written (RtransportWritten0
server_vote_observed	 (RserverVoteObserved"~
ConsumableAuraEffect
	aura_type (RauraType

misc_value (R	miscValue
points (Rpoints
dice (Rdice"⁄
ConsumableSpell
spell_id (RspellId
name (	Rname"
mana_per_tick (RmanaPerTick+
food_aura_spell_id (RfoodAuraSpellId"
food_delay_ms (RfoodDelayMsS
food_effects (20.warcraftbuddy.inventory.v1.ConsumableAuraEffectRfoodEffects'
drink_supported (RdrinkSupported%
food_supported (RfoodSupported"ò
ConsumableMetadata
usable (Rusable%
blocked_reason (	RblockedReasonC
spells (2+.warcraftbuddy.inventory.v1.ConsumableSpellRspells"Ê
ConsumableContext

body_ready (R	bodyReady
dungeon (RdungeonD

self_auras (2%.warcraftbuddy.actions.v1.PlayerAurasR	selfAuras*
food_buff_present (RfoodBuffPresent&
food_buff_known (RfoodBuffKnown"ä
ConsumableUse

request_id (	R	requestId
kind (	Rkind;
source (2#.warcraftbuddy.inventory.v1.ItemRefRsource
spell_id (RspellId"
aura_spell_id (RauraSpellId>
baseline_inventory_revision (RbaselineInventoryRevision>
observed_inventory_revision (RobservedInventoryRevision/
transport_attempted (RtransportAttempted+
transport_written	 (RtransportWritten/
inventory_confirmed
 (RinventoryConfirmed#
aura_observed (RauraObserved'
server_rejected (RserverRejected!
server_error (RserverError
phase (	Rphase4
baseline_aura_revision (RbaselineAuraRevision"U
DiscoverRequest!
vendor_entry (RvendorEntry
vendor_guid (R
vendorGuid"≤
VendorA
creature (2%.warcraftbuddy.world.v1.CreatureStateRcreature"
interactable (Rinteractable%
blocked_reason (	RblockedReason
distance (Rdistance"Ä
	Discovery7
state (2!.warcraftbuddy.inventory.v1.StateRstate:
nearby (2".warcraftbuddy.inventory.v1.VendorRnearby"é
MoveRequest

session_id (	R	sessionId

request_id (	R	requestId+
expected_revision (RexpectedRevision>
expected_inventory_revision (RexpectedInventoryRevision;
source (2#.warcraftbuddy.inventory.v1.ItemRefRsource'
destination_bag (RdestinationBag)
destination_slot (RdestinationSlotC
expected_destination_item_guid (RexpectedDestinationItemGuid"’
EquipRequest

session_id (	R	sessionId

request_id (	R	requestId+
expected_revision (RexpectedRevision>
expected_inventory_revision (RexpectedInventoryRevision;
source (2#.warcraftbuddy.inventory.v1.ItemRefRsource%
equipment_slot (RequipmentSlot6
expected_container_guid (RexpectedContainerGuid"ñ
SellRequest

session_id (	R	sessionId

request_id (	R	requestId+
expected_revision (RexpectedRevision>
expected_inventory_revision (RexpectedInventoryRevision;
source (2#.warcraftbuddy.inventory.v1.ItemRefRsource
vendor_guid (R
vendorGuidD
expected_unit_sell_price_copper (RexpectedUnitSellPriceCopper8
expected_copper_revision (RexpectedCopperRevision"‚
EquipGearRequest

session_id (	R	sessionId

request_id (	R	requestId+
expected_revision (RexpectedRevision>
expected_inventory_revision (RexpectedInventoryRevision;
source (2#.warcraftbuddy.inventory.v1.ItemRefRsource%
equipment_slot (RequipmentSlot>
expected_equipment_revision (RexpectedEquipmentRevision=
expected_equipped_item_guid (RexpectedEquippedItemGuid@
require_conservative_upgrade	 (RrequireConservativeUpgrade"Å
DrinkRequest

session_id (	R	sessionId
	self_guid (RselfGuid;
source (2#.warcraftbuddy.inventory.v1.ItemRefRsource
spell_id (RspellId>
expected_inventory_revision (RexpectedInventoryRevision

request_id (	R	requestId"·
DrinkReceipt

session_id (	R	sessionId
	self_guid (RselfGuid
	item_guid (RitemGuid

item_entry (R	itemEntry
spell_id (RspellId,
stack_count_before (RstackCountBefore+
transport_written (RtransportWritten
reason (	Rreason#
baseline_mana	 (RbaselineMana>
baseline_inventory_revision
 (RbaselineInventoryRevision

request_id (	R	requestIdG
	operation (2).warcraftbuddy.inventory.v1.ConsumableUseR	operation"á
EatBuffFoodRequest

session_id (	R	sessionId
	self_guid (RselfGuid;
source (2#.warcraftbuddy.inventory.v1.ItemRefRsource
spell_id (RspellId>
expected_inventory_revision (RexpectedInventoryRevision

request_id (	R	requestId"ë
EatBuffFoodReceipt

session_id (	R	sessionId
	self_guid (RselfGuid

item_entry (R	itemEntry
spell_id (RspellId+
food_aura_spell_id (RfoodAuraSpellId+
transport_written (RtransportWritten+
time_until_buff_ms (RtimeUntilBuffMs

request_id (	R	requestIdG
	operation	 (2).warcraftbuddy.inventory.v1.ConsumableUseR	operation
reason
 (	Rreason2Ø
BuddyInventoryE
GetState.google.protobuf.Empty!.warcraftbuddy.inventory.v1.StateI
RawGearFacts.google.protobuf.Empty!.warcraftbuddy.inventory.v1.State^
Discover+.warcraftbuddy.inventory.v1.DiscoverRequest%.warcraftbuddy.inventory.v1.DiscoveryR
Move'.warcraftbuddy.inventory.v1.MoveRequest!.warcraftbuddy.inventory.v1.StateT
Equip(.warcraftbuddy.inventory.v1.EquipRequest!.warcraftbuddy.inventory.v1.State\
	EquipGear,.warcraftbuddy.inventory.v1.EquipGearRequest!.warcraftbuddy.inventory.v1.StateR
Sell'.warcraftbuddy.inventory.v1.SellRequest!.warcraftbuddy.inventory.v1.State[
Drink(.warcraftbuddy.inventory.v1.DrinkRequest(.warcraftbuddy.inventory.v1.DrinkReceiptm
EatBuffFood..warcraftbuddy.inventory.v1.EatBuffFoodRequest..warcraftbuddy.inventory.v1.EatBuffFoodReceiptC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.Empty>
BeforeLogout.google.protobuf.Empty.google.protobuf.EmptyBHZFgithub.com/agent-wow/agent-wow/modules/buddy-inventory/api;inventoryv1bproto3