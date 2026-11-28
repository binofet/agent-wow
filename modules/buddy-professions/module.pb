
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
¾
api/module/v1/session.protoagentwow.module.v1google/protobuf/any.protogoogle/protobuf/empty.proto"Œ
WorldPacket
opcode (Ropcode
payload (RpayloadK
transport_scope (2".agentwow.module.v1.TransportScopeRtransportScope"’
SendPacketRequest
opcode (Ropcode
payload (RpayloadK
transport_scope (2".agentwow.module.v1.TransportScopeRtransportScope"Õ
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
spline_source_opcode (RsplineSourceOpcodeP
world_transfer (2).agentwow.module.v1.ObservedWorldTransferRworldTransfer2
summon_offer_sequence (RsummonOfferSequence9
meeting_stone_target_guid (RmeetingStoneTargetGuid=
taxi_arrival_query_revision (RtaxiArrivalQueryRevision" 	
ObservedWorldTransfer
revision (Rrevision
phase (	Rphase
known (Rknown#
connection_id (	RconnectionId
	self_guid (RselfGuid.
from_visit_revision (RfromVisitRevision
from_map_id (R	fromMapId0
from_motion_revision (RfromMotionRevision/
from_spline_id_known	 (RfromSplineIdKnown$
from_spline_id
 (RfromSplineId0
from_spline_revision (RfromSplineRevision=
from_spline_source_revision (RfromSplineSourceRevision9
from_spline_source_opcode (RfromSplineSourceOpcode,
from_spline_active (RfromSplineActive6
pending_motion_revision (RpendingMotionRevision"
target_map_id (RtargetMapId*
to_visit_revision (RtoVisitRevision
	to_map_id (RtoMapId2
entry_motion_revision (RentryMotionRevisionA
worldport_ack_motion_revision (RworldportAckMotionRevision2
worldport_ack_written (RworldportAckWritten2
worldport_ack_unknown (RworldportAckUnknown:
completion_report_written (RcompletionReportWrittenI
!completion_report_motion_revision (RcompletionReportMotionRevision
reason (	Rreason(
target_map_known (RtargetMapKnown"5
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
ÛË
modules/buddy-world/world.protowarcraftbuddy.world.v1api/module/v1/session.protogoogle/protobuf/empty.proto"‚
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
position_inferred= (RpositionInferred]
passenger_movement> (2..warcraftbuddy.world.v1.PassengerMovementFactsRpassengerMovement"×
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
time2	 (Rtime2"„
MovementJumpFacts
velocity (Rvelocity
	sin_angle (RsinAngle
	cos_angle (RcosAngle
xy_speed (RxySpeed"ì
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
spline_elevation (RsplineElevation"Ö
PassengerMovementFacts=
info (2).warcraftbuddy.world.v1.MovementInfoFactsRinfo%
visit_revision (RvisitRevision
revision (Rrevision
age_ms (RageMs#
source_opcode (RsourceOpcode"£
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
 (R	wireImageK
transport_scope (2".agentwow.module.v1.TransportScopeRtransportScope"Ï
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
map_id (RmapId"î
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
unit_flags_motion_revision (RunitFlagsMotionRevisionA
natural_taxi_landing_revision (RnaturalTaxiLandingRevision5
taxi_on_motion_revision (RtaxiOnMotionRevision_
natural_taxi_endpoint (2+.warcraftbuddy.world.v1.NaturalTaxiEndpointRnaturalTaxiEndpoint"?
NaturalTaxiEndpoint
x (Rx
y (Ry
z (Rz"Ú
CompleteSplineRequest
	self_guid (RselfGuid%
visit_revision (RvisitRevision-
authority_revision (RauthorityRevision+
movement_revision (RmovementRevision'
spline_revision (RsplineRevision+
expected_revision (RexpectedRevisionK
transport_scope (2".agentwow.module.v1.TransportScopeRtransportScope"Ø
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
movement_image_source (	RmovementImageSource
retired (Rretired/
retirement_revision (RretirementRevision:
retired_movement_revision (RretiredMovementRevision=
retired_unit_flags_revision (RretiredUnitFlagsRevisionM
retirement_scope (2".agentwow.module.v1.TransportScopeRretirementScope"é
RetireSplineCompletionRequest
	self_guid (RselfGuid%
visit_revision (RvisitRevision+
expected_revision (RexpectedRevision-
authority_revision (RauthorityRevision+
movement_revision (RmovementRevision.
unit_flags_revision (RunitFlagsRevisionK
transport_scope (2".agentwow.module.v1.TransportScopeRtransportScope"É
SplineCompletionObservation
	self_guid (RselfGuid%
visit_revision (RvisitRevision
revision (RrevisionO
	operation (21.warcraftbuddy.world.v1.SplineCompletionOperationR	operation-
retirement_current (RretirementCurrent1
retirement_available (RretirementAvailable[
transfer_adoption (2..warcraftbuddy.world.v1.SplineTransferAdoptionRtransferAdoption:
transfer_adoption_current (RtransferAdoptionCurrent"•	
WorldTransferObservation
revision (Rrevision
known (Rknown
reason (	Rreason
	self_guid (RselfGuid.
from_visit_revision (RfromVisitRevision*
to_visit_revision (RtoVisitRevision=
core (2).agentwow.module.v1.ObservedWorldTransferRcoreh
predecessor_completion (21.warcraftbuddy.world.v1.SplineCompletionOperationRpredecessorCompletion_
predecessor_authority	 (2*.warcraftbuddy.world.v1.OwnMotionAuthorityRpredecessorAuthority3
predecessor_qualified
 (RpredecessorQualified-
continuation_known (RcontinuationKnownG
current_scope (2".agentwow.module.v1.TransportScopeRcurrentScope<
current_authority_revision (RcurrentAuthorityRevision:
current_movement_revision (RcurrentMovementRevision6
current_spline_revision (RcurrentSplineRevision=
current_unit_flags_revision (RcurrentUnitFlagsRevisionJ
"current_unit_flags_motion_revision (RcurrentUnitFlagsMotionRevision7
current_unit_flags_known (RcurrentUnitFlagsKnown,
current_unit_flags (RcurrentUnitFlags#
arrival_known (RarrivalKnown6
booking_causality_known (RbookingCausalityKnown"ò
AdoptSplineTransferRequest
	self_guid (RselfGuid%
visit_revision (RvisitRevision+
expected_revision (RexpectedRevision+
transfer_revision (RtransferRevision4
core_transfer_revision (RcoreTransferRevision-
authority_revision (RauthorityRevision+
movement_revision (RmovementRevision'
spline_revision (RsplineRevision.
unit_flags_revision	 (RunitFlagsRevisionK
transport_scope
 (2".agentwow.module.v1.TransportScopeRtransportScope"û
SplineTransferAdoption
revision (Rrevision
	self_guid (RselfGuid.
from_visit_revision (RfromVisitRevision*
to_visit_revision (RtoVisitRevisionL
transfer (20.warcraftbuddy.world.v1.WorldTransferObservationRtransfer"v
ItemEnchantmentSlot
slot (Rslot
id (Rid!
duration_raw (RdurationRaw
charges (Rcharges"¥
ItemEnhancementState"
id_known_mask (RidKnownMask.
duration_known_mask (RdurationKnownMask,
charges_known_mask (RchargesKnownMaskO
enchantments (2+.warcraftbuddy.world.v1.ItemEnchantmentSlotRenchantments.
property_seed_known (RpropertySeedKnown#
property_seed (RpropertySeed7
random_property_id_known (RrandomPropertyIdKnown,
random_property_id (RrandomPropertyId"í
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
has_enhancements (RhasEnhancements]
enhancement_details (2,.warcraftbuddy.world.v1.ItemEnhancementStateRenhancementDetails"È
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
race_id (RraceId"û
EquippedWeapon

slot_known (R	slotKnown
	item_guid (RitemGuid%
metadata_known (RmetadataKnown
entry (Rentry)
durability_known (RdurabilityKnown

durability (R
durability%
max_durability (RmaxDurability"
SnapshotRequest#
creature_guid (RcreatureGuid%
creature_entry (RcreatureEntry*
game_object_entry (RgameObjectEntry:
include_gathering_objects (RincludeGatheringObjects:
creature_faction_template (RcreatureFactionTemplate"ƒ
ClientMovementRequest
	self_guid (RselfGuid
map_id (RmapId8
server_position_revision (RserverPositionRevision
sequence (Rsequence
valid (Rvalid
x (Rx
y (Ry
z (Rz 
orientation	 (Rorientation"œ
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
 (RageMs"ã
SpellGroundEffect
guid (Rguid
caster_guid (R
casterGuid
spell_id (RspellId!
radius_yards (RradiusYards
x (Rx
y (Ry
z (Rz8
first_observed_at_unix_ms (RfirstObservedAtUnixMs2
server_cast_time_ms	 (H RserverCastTimeMsˆ+
position_revision
 (RpositionRevisionB
_server_cast_time_ms"
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
state (RstateD
creator_selected_target_guid (H RcreatorSelectedTargetGuidˆW
moving_transport (2,.warcraftbuddy.world.v1.MovingTransportFactsRmovingTransportB
_creator_selected_target_guid"Õ
MovingTransportFacts"

display_id (H R	displayIdˆ
scale (HRscaleˆ 
	period_ms (HRperiodMsˆ-
path_progress_ms (HRpathProgressMsˆ,
packed_rotation (HRpackedRotationˆ+
movement_revision (RmovementRevision&
movement_age_ms (RmovementAgeMs#
source_opcode (RsourceOpcode+
position_revision	 (RpositionRevision(
path_fraction
 (HRpathFractionˆ4
path_fraction_revision (RpathFractionRevision/
path_fraction_age_ms (RpathFractionAgeMs=
path_fraction_source_opcode (RpathFractionSourceOpcodeK
template (2/.warcraftbuddy.world.v1.MovingTransportTemplateRtemplate'
object_revision (RobjectRevision0
template_query_phase (	RtemplateQueryPhaseB
_display_idB
_scaleB

_period_msB
_path_progress_msB
_packed_rotationB
_path_fraction"Ç
MovingTransportTemplate 
taxi_path_id (R
taxiPathId

move_speed (R	moveSpeed+
acceleration_rate (RaccelerationRate

display_id (R	displayId
entry (Rentry
guid (Rguid
	self_guid (RselfGuid%
visit_revision (RvisitRevision
revision	 (Rrevision
age_ms
 (RageMs"Û
#QueryMovingTransportTemplateRequest

session_id (	R	sessionId
guid (Rguid
entry (Rentry
	self_guid (RselfGuid%
visit_revision (RvisitRevision'
object_revision (RobjectRevision"¶
$QueryMovingTransportTemplateResponse
phase (	Rphase+
transport_written (RtransportWrittenK
template (2/.warcraftbuddy.world.v1.MovingTransportTemplateRtemplate"ð
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
activeCast:
stationary_position_known* (RstationaryPositionKnown&
npc_flags_known+ (RnpcFlagsKnown4
faction_template_known, (RfactionTemplateKnown+
unit_flags_2_known- (RunitFlags2Known0
movement_flags_known. (RmovementFlagsKnownQ
visible_auras/ (2,.warcraftbuddy.world.v1.VisibleCreatureAurasRvisibleAuras"ò
VisibleCreatureAuras
complete (Rcomplete
revision (RrevisionA
slots (2+.warcraftbuddy.world.v1.VisibleCreatureAuraRslots#
cleared_slots (RclearedSlots(
full_revision (H RfullRevisionˆB
_full_revision"‘
VisibleCreatureAura
slot (Rslot
spell_id (RspellId
flags (Rflags!
caster_level (RcasterLevel*
stacks_or_charges (RstacksOrCharges$
caster_guid (H R
casterGuidˆ+
max_duration_ms (HRmaxDurationMsˆ&
remaining_ms (HRremainingMsˆ
positive	 (Rpositive
negative
 (RnegativeB
_caster_guidB
_max_duration_msB
_remaining_ms"¨
CreatureCast
spell_id (RspellId

cast_count (R	castCount!
remaining_ms (RremainingMs
target_guid (R
targetGuid
revision (Rrevision"Ã
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
flags (Rflags"Ô
QuestLogEntry
slot (Rslot
quest_id (RquestId
state (Rstate
complete (Rcomplete
failed (Rfailed)
objective_counts (RobjectiveCounts
expire_time (R
expireTime"Ú
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
has_enhancements (RhasEnhancements]
enhancement_details (2,.warcraftbuddy.world.v1.ItemEnhancementStateRenhancementDetails"¥
InventoryBag
bag (Rbag

slot_known (R	slotKnown%
container_guid (RcontainerGuid%
metadata_known (RmetadataKnown
entry (Rentry%
contents_known (RcontentsKnown

slot_count (R	slotCount:
slots (2$.warcraftbuddy.world.v1.BackpackSlotRslots"Ô

SkillState
slot (Rslot
skill_id (RskillId
step (Rstep
value (Rvalue
	max_value (RmaxValue'
temporary_bonus (RtemporaryBonus'
permanent_bonus (RpermanentBonus"Ô"
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
spline_completion3 (23.warcraftbuddy.world.v1.SplineCompletionObservationRsplineCompletionW
world_transfer4 (20.warcraftbuddy.world.v1.WorldTransferObservationRworldTransfer&
self_ammo_known5 (RselfAmmoKnown&
self_ammo_entry6 (RselfAmmoEntry,
self_ammo_revision7 (RselfAmmoRevision"
self_xp_known8 (RselfXpKnown
self_xp9 (RselfXp6
self_next_level_xp_known: (RselfNextLevelXpKnown+
self_next_level_xp; (RselfNextLevelXp(
self_xp_revision< (RselfXpRevisionP
ground_effects= (2).warcraftbuddy.world.v1.SpellGroundEffectRgroundEffects8
ground_effects_supported> (RgroundEffectsSupported6
ground_effects_overflow? (RgroundEffectsOverflow:
nearby_creatures_complete@ (RnearbyCreaturesCompleteT
summoning_objectsA (2'.warcraftbuddy.world.v1.GameObjectStateRsummoningObjects^
,requested_creature_stationary_position_knownB (R(requestedCreatureStationaryPositionKnownn
 selected_creature_outside_nearbyC (2%.warcraftbuddy.world.v1.CreatureStateRselectedCreatureOutsideNearbyT
moving_transportsD (2'.warcraftbuddy.world.v1.GameObjectStateRmovingTransports"q
CombatEventsRequest

session_id (	R	sessionId%
after_sequence (RafterSequence
limit (Rlimit"®
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
reason (	Rreason"Û
CombatEventsResponse

session_id (	R	sessionId
	self_guid (RselfGuid%
first_sequence (RfirstSequence'
latest_sequence (RlatestSequence#
next_sequence (RnextSequence
has_more (RhasMore;
events (2#.warcraftbuddy.world.v1.CombatEventRevents:
gaps (2&.warcraftbuddy.world.v1.CombatEventGapRgaps2”

BuddyWorld™
QueryMovingTransportTemplate;.warcraftbuddy.world.v1.QueryMovingTransportTemplateRequest<.warcraftbuddy.world.v1.QueryMovingTransportTemplateResponse„
RetireSplineCompletion5.warcraftbuddy.world.v1.RetireSplineCompletionRequest3.warcraftbuddy.world.v1.SplineCompletionObservation~
AdoptSplineTransfer2.warcraftbuddy.world.v1.AdoptSplineTransferRequest3.warcraftbuddy.world.v1.SplineCompletionObservationt
CompleteSpline-.warcraftbuddy.world.v1.CompleteSplineRequest3.warcraftbuddy.world.v1.SplineCompletionObservationi
CombatEvents+.warcraftbuddy.world.v1.CombatEventsRequest,.warcraftbuddy.world.v1.CombatEventsResponse]
Snapshot'.warcraftbuddy.world.v1.SnapshotRequest(.warcraftbuddy.world.v1.SnapshotResponse^
ObserveClientMovement-.warcraftbuddy.world.v1.ClientMovementRequest.google.protobuf.EmptyC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.EmptyB@Z>github.com/agent-wow/agent-wow/modules/buddy-world/api;worldv1bproto3
ù3
+modules/buddy-professions/professions.protowarcraftbuddy.professions.v1api/module/v1/session.protogoogle/protobuf/empty.protomodules/buddy-world/world.proto"P
Skill
skill_id (RskillId
rank (Rrank
maximum (Rmaximum"ù
Recipe
skill_id (RskillId
spell_id (RspellId
name (	Rname
known (Rknown3
requirements_verified (RrequirementsVerified#
reagent_entry (RreagentEntry#
reagent_count (RreagentCount!
output_entry (RoutputEntry!
output_count	 (RoutputCount
focus_id
 (RfocusId 
cast_time_ms (R
castTimeMs.
channel_duration_ms (RchannelDurationMs
ready (Rready
reason (	Rreason/
metadata_provenance (	RmetadataProvenance"¸
	Operation!
operation_id (	RoperationId

request_id (	R	requestId
skill_id (RskillId
spell_id (RspellId

cast_count (R	castCountC
status (2+.warcraftbuddy.professions.v1.AttemptStatusRstatus
reason (	Rreason4
cast_transport_written (RcastTransportWritten,
own_start_observed	 (RownStartObserved&
own_go_observed
 (RownGoObserved!
failure_code (RfailureCode8
baseline_skills_revision (RbaselineSkillsRevision8
observed_skills_revision (RobservedSkillsRevision
before_rank (R
beforeRank

after_rank (R	afterRank'
skill_increased (RskillIncreased<
baseline_backpack_revision (RbaselineBackpackRevision<
observed_backpack_revision (RobservedBackpackRevision<
reagents_consumed_observed (RreagentsConsumedObserved:
output_inventory_observed (RoutputInventoryObserved;
own_created_items_observed (RownCreatedItemsObserved9
own_looted_items_observed (RownLootedItemsObserved
bobber_guid (R
bobberGuid'
bobber_revision (RbobberRevision.
own_bobber_observed (RownBobberObserved'
splash_observed (RsplashObserved4
channel_start_observed (RchannelStartObserved2
channel_stop_observed (RchannelStopObserved0
loot_window_observed (RlootWindowObserved,
loot_slots_removed (RlootSlotsRemoved2
loot_release_observed (RlootReleaseObserved)
cancel_requested  (RcancelRequested8
cancel_transport_written! (RcancelTransportWritten/
cleanup_unconfirmed" (RcleanupUnconfirmed%
lease_released# (RleaseReleased
age_ms$ (RageMs

limitation% (	R
limitation
target_guid& (R
targetGuid8
target_position_revision' (RtargetPositionRevision>
baseline_inventory_revision( (RbaselineInventoryRevision>
observed_inventory_revision) (RobservedInventoryRevision2
fully_looted_observed* (RfullyLootedObserved!
target_entry+ (RtargetEntry

source_bag, (R	sourceBag
source_slot- (R
sourceSlot6
input_consumed_observed. (RinputConsumedObserved"©
SkinningTargetA
creature (2%.warcraftbuddy.world.v1.CreatureStateRcreature2
fully_looted_observed (RfullyLootedObserved
ready (Rready
reason (	Rreason%
required_skill (RrequiredSkill"
approachable (Rapproachable'
approach_reason (	RapproachReason"ˆ

HerbTarget?
object (2'.warcraftbuddy.world.v1.GameObjectStateRobject
ready (Rready
reason (	Rreason%
required_skill (RrequiredSkill
lock_id (RlockId"
approachable (Rapproachable'
approach_reason (	RapproachReason"T
HerbInspectRequest

session_id (	R	sessionId
target_guid (R
targetGuid"ß
DisenchantItem
bag (Rbag
slot (Rslot
	item_guid (RitemGuid
entry (Rentry
stack_count (R
stackCount%
template_known (RtemplateKnown
quality (Rquality

item_class (R	itemClass%
required_skill	 (RrequiredSkill
ready
 (Rready
reason (	Rreason

limitation (	R
limitation"Z
DisenchantInspectRequest

session_id (	R	sessionId
target_guid (R
targetGuid"I
StopRequest

session_id (	R	sessionId
	self_guid (RselfGuid"Š
State

session_id (	R	sessionId
revision (Rrevision
	self_guid (RselfGuid!
skills_known (RskillsKnown'
skills_revision (RskillsRevision;
skills (2#.warcraftbuddy.professions.v1.SkillRskills!
spells_known (RspellsKnown>
recipes (2$.warcraftbuddy.professions.v1.RecipeRrecipes%
backpack_known	 (RbackpackKnown+
backpack_revision
 (RbackpackRevision
active (RactiveE
	operation (2'.warcraftbuddy.professions.v1.OperationR	operation)
transport_writes (RtransportWrites+
malformed_packets (RmalformedPackets
closing (Rclosing

limitation (	R
limitation'
inventory_known (RinventoryKnown-
inventory_revision (RinventoryRevisionW
skinning_targets (2,.warcraftbuddy.professions.v1.SkinningTargetRskinningTargetsK
herb_targets (2(.warcraftbuddy.professions.v1.HerbTargetRherbTargetsU
disenchant_item (2,.warcraftbuddy.professions.v1.DisenchantItemRdisenchantItem#
stop_prepared (RstopPrepared"Ä
AttemptRequest

session_id (	R	sessionId

request_id (	R	requestId+
expected_revision (RexpectedRevision
skill_id (RskillId
spell_id (RspellId8
expected_skills_revision (RexpectedSkillsRevision<
expected_backpack_revision (RexpectedBackpackRevision>
expected_inventory_revision (RexpectedInventoryRevision
target_guid	 (R
targetGuid8
target_position_revision
 (RtargetPositionRevision"Q
CancelRequest

session_id (	R	sessionId!
operation_id (	RoperationId*à
AttemptStatus
STATUS_UNSPECIFIED 
RESERVED
WAITING_CAST
WAITING_BOBBER
WAITING_LOOT
WAITING_OBSERVATIONS
	COMPLETED
BLOCKED
SERVER_REJECTED
	CANCELLED	
OUTCOME_UNCONFIRMED
2Ñ
BuddyProfessionsG
GetState.google.protobuf.Empty#.warcraftbuddy.professions.v1.Stated
InspectHerb0.warcraftbuddy.professions.v1.HerbInspectRequest#.warcraftbuddy.professions.v1.Statep
InspectDisenchant6.warcraftbuddy.professions.v1.DisenchantInspectRequest#.warcraftbuddy.professions.v1.State]
PrepareStop).warcraftbuddy.professions.v1.StopRequest#.warcraftbuddy.professions.v1.State\
Attempt,.warcraftbuddy.professions.v1.AttemptRequest#.warcraftbuddy.professions.v1.StateZ
Cancel+.warcraftbuddy.professions.v1.CancelRequest#.warcraftbuddy.professions.v1.StateC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.Empty>
BeforeLogout.google.protobuf.Empty.google.protobuf.EmptyBLZJgithub.com/agent-wow/agent-wow/modules/buddy-professions/api;professionsv1bproto3