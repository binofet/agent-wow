
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
ü,
modules/buddy-world/world.protowarcraftbuddy.world.v1api/module/v1/session.protogoogle/protobuf/empty.proto"Ô
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
mainhand0 (2&.warcraftbuddy.world.v1.EquippedWeaponRmainhand"˚
EquippedWeapon

slot_known (R	slotKnown
	item_guid (RitemGuid%
metadata_known (RmetadataKnown
entry (Rentry)
durability_known (RdurabilityKnown

durability (R
durability%
max_durability (RmaxDurability"
SnapshotRequest"É
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
level& (Rlevel"√
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
expireTime"Ø
BackpackSlot
bag (Rbag
slot (Rslot
	item_guid (RitemGuid
entry (Rentry
stack_count (R
stackCount%
metadata_known (RmetadataKnown"‘
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
self_copper_revision (RselfCopperRevision2∞

BuddyWorld]
Snapshot'.warcraftbuddy.world.v1.SnapshotRequest(.warcraftbuddy.world.v1.SnapshotResponseC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.EmptyB@Z>github.com/agent-wow/agent-wow/modules/buddy-world/api;worldv1bproto3
ö
%modules/buddy-training/training.protowarcraftbuddy.training.v1api/module/v1/session.protogoogle/protobuf/empty.protomodules/buddy-world/world.proto"ß
Spell
spell_id (RspellId
usable (Rusable
cost_copper (R
costCopper

point_cost (R	pointCost%
required_level (RrequiredLevel%
required_skill (RrequiredSkill.
required_skill_rank (RrequiredSkillRank-
required_abilities (RrequiredAbilities"€
Offer
revision (Rrevision
npc_guid (RnpcGuid!
trainer_type (RtrainerType8
spells (2 .warcraftbuddy.training.v1.SpellRspells
greeting (	Rgreeting"
expires_in_ms (RexpiresInMs"ã
Purchase
id (Rid
npc_guid (RnpcGuid
spell_id (RspellId
cost_copper (R
costCopperA
status (2).warcraftbuddy.training.v1.PurchaseStatusRstatus+
transport_written (RtransportWritten6
server_success_observed (RserverSuccessObserved4
learned_spell_observed (RlearnedSpellObserved6
refreshed_trainer_knows	 (RrefreshedTrainerKnows*
learned_spell_ids
 (RlearnedSpellIds%
server_failure (RserverFailure
failure (	Rfailure'
baseline_copper (RbaselineCopper8
baseline_copper_revision (RbaselineCopperRevision2
copper_debit_observed (RcopperDebitObserved"Î
State

session_id (	R	sessionId
revision (Rrevision(
action_in_flight (RactionInFlight6
offer (2 .warcraftbuddy.training.v1.OfferRofferH
last_purchase (2#.warcraftbuddy.training.v1.PurchaseRlastPurchase!
copper_known (RcopperKnown
copper (Rcopper'
copper_revision (RcopperRevision+
malformed_packets	 (RmalformedPackets'
ignored_packets
 (RignoredPackets)
transport_writes (RtransportWrites
reason (	Rreason"⁄
TrainerA
creature (2%.warcraftbuddy.world.v1.CreatureStateRcreature%
distance_known (RdistanceKnown
distance (Rdistance"
interactable (Rinteractable%
blocked_reason (	RblockedReason"
	Discovery6
state (2 .warcraftbuddy.training.v1.StateRstate:
nearby (2".warcraftbuddy.training.v1.TrainerRnearby"G
ListRequest

session_id (	R	sessionId
npc_guid (RnpcGuid"‰

BuyRequest

session_id (	R	sessionId
npc_guid (RnpcGuid%
offer_revision (RofferRevision
spell_id (RspellId0
expected_cost_copper (RexpectedCostCopper(
max_spend_copper (RmaxSpendCopper*è
PurchaseStatus
PURCHASE_NONE 
	REQUESTED
TRAINER_SUCCEEDED
TRAINING_CONFIRMED
SERVER_REJECTED
UNCONFIRMED_TIMEOUT2Ü
BuddyTrainingH
Discover.google.protobuf.Empty$.warcraftbuddy.training.v1.DiscoveryD
GetState.google.protobuf.Empty .warcraftbuddy.training.v1.StateP
List&.warcraftbuddy.training.v1.ListRequest .warcraftbuddy.training.v1.StateN
Buy%.warcraftbuddy.training.v1.BuyRequest .warcraftbuddy.training.v1.StateC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.EmptyBFZDgithub.com/agent-wow/agent-wow/modules/buddy-training/api;trainingv1bproto3