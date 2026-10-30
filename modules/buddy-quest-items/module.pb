
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
ÏB
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
state (Rstate"ƒ
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
level& (Rlevel"Ã
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
permanent_bonus (RpermanentBonus"¦
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
inventory_bags$ (2$.warcraftbuddy.world.v1.InventoryBagRinventoryBags2°

BuddyWorld]
Snapshot'.warcraftbuddy.world.v1.SnapshotRequest(.warcraftbuddy.world.v1.SnapshotResponseC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.EmptyB@Z>github.com/agent-wow/agent-wow/modules/buddy-world/api;worldv1bproto3
÷
+modules/buddy-quest-items/quest_items.protowarcraftbuddy.questitems.v1api/module/v1/session.protogoogle/protobuf/empty.protomodules/buddy-world/world.proto"|
ItemBinding
bag (Rbag
slot (Rslot
	item_guid (RitemGuid
entry (Rentry
count (Rcount"Ô
	Operation!
operation_id (	RoperationId

request_id (	R	requestId;
action (2#.warcraftbuddy.questitems.v1.ActionRaction;
status (2#.warcraftbuddy.questitems.v1.StatusRstatus
reason (	Rreason
quest_id (RquestId,
source_object_guid (RsourceObjectGuid0
target_creature_guid (RtargetCreatureGuid<
item	 (2(.warcraftbuddy.questitems.v1.ItemBindingRitem
spell_id
 (RspellId

cast_count (R	castCount+
transport_written (RtransportWritten+
write_unconfirmed (RwriteUnconfirmed5
own_cast_start_observed (RownCastStartObserved/
own_cast_go_observed (RownCastGoObserved3
own_item_push_observed (RownItemPushObserved8
inventory_added_observed (RinventoryAddedObserved>
inventory_consumed_observed (RinventoryConsumedObserved2
quest_credit_observed (RquestCreditObserved6
quest_complete_observed (RquestCompleteObserved4
before_objective_count (RbeforeObjectiveCount2
after_objective_count (RafterObjectiveCount>
baseline_inventory_revision (RbaselineInventoryRevision>
observed_inventory_revision (RobservedInventoryRevision=
baseline_quest_log_revision (RbaselineQuestLogRevision=
observed_quest_log_revision (RobservedQuestLogRevision!
failure_code (RfailureCode)
cancel_requested (RcancelRequested%
lease_released (RleaseReleased/
cleanup_unconfirmed (RcleanupUnconfirmed
age_ms (RageMs"Š
State

session_id (	R	sessionId
revision (Rrevision
	self_guid (RselfGuid
active (Ractive&
quest_log_known (RquestLogKnown,
quest_log_revision (RquestLogRevision'
inventory_known (RinventoryKnown-
inventory_revision (RinventoryRevision
quest_id	 (RquestId!
quest_active
 (RquestActive'
objective_count (RobjectiveCount%
required_count (RrequiredCount%
quest_complete (RquestCompleteA
barrels (2'.warcraftbuddy.world.v1.GameObjectStateRbarrels;
fires (2%.warcraftbuddy.world.v1.CreatureStateRfires@
bucket (2(.warcraftbuddy.questitems.v1.ItemBindingRbucket2
barrel_template_known (RbarrelTemplateKnown2
bucket_template_known (RbucketTemplateKnown
ready (Rready
reason (	RreasonD
	operation (2&.warcraftbuddy.questitems.v1.OperationR	operation)
transport_writes (RtransportWrites+
malformed_packets (RmalformedPackets
closing (Rclosing

limitation (	R
limitation"³
AttemptRequest

session_id (	R	sessionId

request_id (	R	requestId
quest_id (RquestId;
action (2#.warcraftbuddy.questitems.v1.ActionRaction,
source_object_guid (RsourceObjectGuid0
target_creature_guid (RtargetCreatureGuid=
expected_quest_log_revision (RexpectedQuestLogRevision>
expected_inventory_revision (RexpectedInventoryRevision,
expected_item_guid	 (RexpectedItemGuid"Q
CancelRequest

session_id (	R	sessionId!
operation_id (	RoperationId*C
Action
ACTION_UNSPECIFIED 
FILL_BUCKET
THROW_BUCKET*³
Status
STATUS_UNSPECIFIED 
RESERVED
WAITING_SERVER
	COMPLETED
SERVER_REJECTED
	CANCELLED
OUTCOME_UNCONFIRMED
BLOCKED
NO_CREDIT_OBSERVED2”
BuddyQuestItemsF
GetState.google.protobuf.Empty".warcraftbuddy.questitems.v1.StateZ
Attempt+.warcraftbuddy.questitems.v1.AttemptRequest".warcraftbuddy.questitems.v1.StateX
Cancel*.warcraftbuddy.questitems.v1.CancelRequest".warcraftbuddy.questitems.v1.StateC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.Empty>
BeforeLogout.google.protobuf.Empty.google.protobuf.EmptyBKZIgithub.com/agent-wow/agent-wow/modules/buddy-quest-items/api;questitemsv1bproto3