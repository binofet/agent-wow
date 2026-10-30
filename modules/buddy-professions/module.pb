
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
ýC
modules/buddy-world/world.protowarcraftbuddy.world.v1api/module/v1/session.protogoogle/protobuf/empty.proto"õ
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
offhand_revision8 (RoffhandRevision"û
EquippedWeapon

slot_known (R	slotKnown
	item_guid (RitemGuid%
metadata_known (RmetadataKnown
entry (Rentry)
durability_known (RdurabilityKnown

durability (R
durability%
max_durability (RmaxDurability"‰
SnapshotRequest#
creature_guid (RcreatureGuid%
creature_entry (RcreatureEntry*
game_object_entry (RgameObjectEntry"Ý
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
state (Rstate"Ý
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
dynamic_flags_known( (RdynamicFlagsKnown"Ã
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
expireTime"¯
BackpackSlot
bag (Rbag
slot (Rslot
	item_guid (RitemGuid
entry (Rentry
stack_count (R
stackCount%
metadata_known (RmetadataKnown"¥
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
permanent_bonus (RpermanentBonus"ú
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
skinnable_corpses% (2%.warcraftbuddy.world.v1.CreatureStateRskinnableCorpses2°

BuddyWorld]
Snapshot'.warcraftbuddy.world.v1.SnapshotRequest(.warcraftbuddy.world.v1.SnapshotResponseC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.EmptyB@Z>github.com/agent-wow/agent-wow/modules/buddy-world/api;worldv1bproto3
ß2
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
input_consumed_observed. (RinputConsumedObserved"Ü
SkinningTargetA
creature (2%.warcraftbuddy.world.v1.CreatureStateRcreature2
fully_looted_observed (RfullyLootedObserved
ready (Rready
reason (	Rreason%
required_skill (RrequiredSkill"»

HerbTarget?
object (2'.warcraftbuddy.world.v1.GameObjectStateRobject
ready (Rready
reason (	Rreason%
required_skill (RrequiredSkill
lock_id (RlockId"T
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