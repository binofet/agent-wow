
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
ùu
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
map_id (RmapId"é
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
permanent_bonus (RpermanentBonus"Û
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
self_spline_facts1 (2&.warcraftbuddy.world.v1.OwnSplineFactsRselfSplineFacts"q
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
gaps (2&.warcraftbuddy.world.v1.CombatEventGapRgaps2˚

BuddyWorldi
CombatEvents+.warcraftbuddy.world.v1.CombatEventsRequest,.warcraftbuddy.world.v1.CombatEventsResponse]
Snapshot'.warcraftbuddy.world.v1.SnapshotRequest(.warcraftbuddy.world.v1.SnapshotResponse^
ObserveClientMovement-.warcraftbuddy.world.v1.ClientMovementRequest.google.protobuf.EmptyC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.EmptyB@Z>github.com/agent-wow/agent-wow/modules/buddy-world/api;worldv1bproto3
Ã!
%modules/buddy-supplies/supplies.protowarcraftbuddy.supplies.v1api/module/v1/session.protogoogle/protobuf/empty.protomodules/buddy-world/world.proto"ƒ

VendorItem
slot (Rslot
entry (Rentry'
available_count (RavailableCount!
price_copper (RpriceCopper
	buy_count (RbuyCount#
extended_cost (RextendedCost"ü
Offer
revision (Rrevision
npc_guid (RnpcGuid;
items (2%.warcraftbuddy.supplies.v1.VendorItemRitems"
expires_in_ms (RexpiresInMs"î
	Operation

request_id (	R	requestId
kind (	Rkind
status (	Rstatus
reason (	Rreason
npc_guid (RnpcGuid
item_id (RitemId
count (Rcount
cost_copper (R
costCopper
	item_guid	 (RitemGuid0
original_weapon_guid
 (RoriginalWeaponGuid2
original_weapon_entry (RoriginalWeaponEntry+
transport_written (RtransportWritten6
server_success_observed (RserverSuccessObserved/
inventory_confirmed (RinventoryConfirmed)
copper_confirmed (RcopperConfirmed/
equipment_confirmed (RequipmentConfirmed%
lease_released (RleaseReleased<
baseline_backpack_revision (RbaselineBackpackRevision8
baseline_copper_revision (RbaselineCopperRevision%
server_failure (RserverFailure"€
State

session_id (	R	sessionId
	self_guid (RselfGuid
revision (Rrevision
active (Ractive6
offer (2 .warcraftbuddy.supplies.v1.OfferRofferB
	operation (2$.warcraftbuddy.supplies.v1.OperationR	operation!
copper_known (RcopperKnown
copper (Rcopper'
copper_revision	 (RcopperRevision%
backpack_known
 (RbackpackKnown+
backpack_revision (RbackpackRevision@
backpack (2$.warcraftbuddy.world.v1.BackpackSlotRbackpack!
skills_known (RskillsKnown'
skills_revision (RskillsRevisionB
mainhand (2&.warcraftbuddy.world.v1.EquippedWeaponRmainhand%
blocked_reason (	RblockedReason)
transport_writes (RtransportWrites+
malformed_packets (RmalformedPackets

limitation (	R
limitation0
original_weapon_guid (RoriginalWeaponGuid2
original_weapon_entry (RoriginalWeaponEntry,
equipped_pole_guid (RequippedPoleGuid)
restore_required (RrestoreRequired"≤
VendorA
creature (2%.warcraftbuddy.world.v1.CreatureStateRcreature"
interactable (Rinteractable%
blocked_reason (	RblockedReason
distance (Rdistance"~
	Discovery6
state (2 .warcraftbuddy.supplies.v1.StateRstate9
nearby (2!.warcraftbuddy.supplies.v1.VendorRnearby"G
ListRequest

session_id (	R	sessionId
npc_guid (RnpcGuid"ô

BuyRequest

session_id (	R	sessionId

request_id (	R	requestId+
expected_revision (RexpectedRevision
npc_guid (RnpcGuid%
offer_revision (RofferRevision
vendor_slot (R
vendorSlot
item_id (RitemId
count (Rcount2
expected_price_copper	 (RexpectedPriceCopper(
max_spend_copper
 (RmaxSpendCopper<
expected_backpack_revision (RexpectedBackpackRevision8
expected_copper_revision (RexpectedCopperRevision8
expected_skills_revision (RexpectedSkillsRevision"È
EquipmentRequest

session_id (	R	sessionId

request_id (	R	requestId+
expected_revision (RexpectedRevision<
expected_backpack_revision (RexpectedBackpackRevision8
expected_skills_revision (RexpectedSkillsRevision#
backpack_slot (RbackpackSlot
	item_guid (RitemGuid0
original_weapon_guid (RoriginalWeaponGuid2¯
BuddySuppliesD
GetState.google.protobuf.Empty .warcraftbuddy.supplies.v1.StateH
Discover.google.protobuf.Empty$.warcraftbuddy.supplies.v1.DiscoveryP
List&.warcraftbuddy.supplies.v1.ListRequest .warcraftbuddy.supplies.v1.StateN
Buy%.warcraftbuddy.supplies.v1.BuyRequest .warcraftbuddy.supplies.v1.StateV
Equip+.warcraftbuddy.supplies.v1.EquipmentRequest .warcraftbuddy.supplies.v1.StateX
Restore+.warcraftbuddy.supplies.v1.EquipmentRequest .warcraftbuddy.supplies.v1.StateC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.Empty>
BeforeLogout.google.protobuf.Empty.google.protobuf.EmptyBFZDgithub.com/agent-wow/agent-wow/modules/buddy-supplies/api;suppliesv1bproto3