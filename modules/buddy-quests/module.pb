
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
¾W
modules/buddy-world/world.protowarcraftbuddy.world.v1api/module/v1/session.protogoogle/protobuf/empty.proto"­
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
race_id: (RraceId"Ž
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
has_enhancements (RhasEnhancements"
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
dynamic_flags (RdynamicFlags"û
EquippedWeapon

slot_known (R	slotKnown
	item_guid (RitemGuid%
metadata_known (RmetadataKnown
entry (Rentry)
durability_known (RdurabilityKnown

durability (R
durability%
max_durability (RmaxDurability"Å
SnapshotRequest#
creature_guid (RcreatureGuid%
creature_entry (RcreatureEntry*
game_object_entry (RgameObjectEntry:
include_gathering_objects (RincludeGatheringObjects"ƒ
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
 (RageMs"Ý
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
expireTime"û
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
has_enhancements (RhasEnhancements"¥
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
permanent_bonus (RpermanentBonus"Ò
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
discovery_anchor- (2'.warcraftbuddy.world.v1.DiscoveryAnchorRdiscoveryAnchor2

BuddyWorld]
Snapshot'.warcraftbuddy.world.v1.SnapshotRequest(.warcraftbuddy.world.v1.SnapshotResponse^
ObserveClientMovement-.warcraftbuddy.world.v1.ClientMovementRequest.google.protobuf.EmptyC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.EmptyB@Z>github.com/agent-wow/agent-wow/modules/buddy-world/api;worldv1bproto3
ÝI
!modules/buddy-quests/quests.protowarcraftbuddy.quests.v1api/module/v1/session.protomodules/buddy-world/world.protogoogle/protobuf/empty.proto"›
Quest
quest_id (RquestId
title (	Rtitle
details (	Rdetails

objectives (	R
objectives
level (Rlevel
flags (Rflags
icon (Ricon
	available (R	available

repeatable	 (R
repeatable)
accept_requested
 (RacceptRequested"Î
Offer
revision (Rrevision
npc_guid (RnpcGuid6
quests (2.warcraftbuddy.quests.v1.QuestRquests
greeting (	Rgreeting
source (	Rsource"
expires_in_ms (RexpiresInMs"§
NearbyGiverA
creature (2%.warcraftbuddy.world.v1.CreatureStateRcreature%
distance_known (RdistanceKnown
distance (Rdistance!
can_interact (RcanInteract%
blocked_reason (	RblockedReasonH
game_object (2'.warcraftbuddy.world.v1.GameObjectStateR
gameObject"‚

Acceptance
npc_guid (RnpcGuid
quest_id (RquestId%
offer_revision (RofferRevision+
transport_written (RtransportWrittenA
status (2).warcraftbuddy.quests.v1.AcceptanceStatusRstatus2
baseline_log_revision (RbaselineLogRevision4
confirmed_log_revision (RconfirmedLogRevision
failure (	Rfailure#
server_reason	 (RserverReason"@
GiverStatus
npc_guid (RnpcGuid
status (Rstatus"Ä
StateResponse

session_id (	R	sessionId
revision (Rrevision4
offer (2.warcraftbuddy.quests.v1.OfferRoffer<
nearby (2$.warcraftbuddy.quests.v1.NearbyGiverRnearbyL
last_acceptance (2#.warcraftbuddy.quests.v1.AcceptanceRlastAcceptanceG
giver_status (2$.warcraftbuddy.quests.v1.GiverStatusRgiverStatus(
action_in_flight (RactionInFlight&
quest_log_known (RquestLogKnown,
quest_log_revision	 (RquestLogRevisionB
	quest_log
 (2%.warcraftbuddy.world.v1.QuestLogEntryRquestLog+
malformed_packets (RmalformedPackets'
ignored_packets (RignoredPacketsH
turn_in_offer (2$.warcraftbuddy.quests.v1.TurnInOfferRturnInOfferG
last_reward (2&.warcraftbuddy.quests.v1.RewardReceiptR
lastRewardN
pending_share (2).warcraftbuddy.quests.v1.SharedQuestOfferRpendingShared
last_shared_acceptance (2..warcraftbuddy.quests.v1.SharedQuestAcceptanceRlastSharedAcceptance2
shared_quests_enabled (RsharedQuestsEnabledV
last_exploration (2+.warcraftbuddy.quests.v1.ExplorationReceiptRlastExploration"—
ExploreRequest

session_id (	R	sessionId

request_id (	R	requestId
quest_id (RquestId,
quest_log_revision (RquestLogRevision"‰
ExplorationReceipt

request_id (	R	requestId
quest_id (RquestId

trigger_id (R	triggerIdB
status (2*.warcraftbuddy.quests.v1.ExplorationStatusRstatus+
transport_written (RtransportWritten2
baseline_log_revision (RbaselineLogRevision4
confirmed_log_revision (RconfirmedLogRevision%
lease_released (RleaseReleased
failure	 (	Rfailure"’
SharedQuestOffer
sequence (Rsequence
sharer_guid (R
sharerGuid4
quest (2.warcraftbuddy.quests.v1.QuestRquest<
kind (2(.warcraftbuddy.quests.v1.SharedQuestKindRkind"
expires_in_ms (RexpiresInMs)
accept_requested (RacceptRequested"Ñ
SharedQuestAcceptance%
share_sequence (RshareSequence
sharer_guid (R
sharerGuid
quest_id (RquestId<
kind (2(.warcraftbuddy.quests.v1.SharedQuestKindRkind+
transport_written (RtransportWrittenA
status (2).warcraftbuddy.quests.v1.AcceptanceStatusRstatus2
baseline_log_revision (RbaselineLogRevision4
confirmed_log_revision (RconfirmedLogRevision
failure	 (	Rfailure#
server_reason
 (RserverReason"–
SharedQuestRequest

session_id (	R	sessionId%
share_sequence (RshareSequence
sharer_guid (R
sharerGuid
quest_id (RquestId"Ü
QuestObjective
kind (	Rkind
index (Rindex
	target_id (RtargetId%
required_count (RrequiredCount
text (	Rtext#
current_known (RcurrentKnown#
current_count (RcurrentCount"­
QuestJournalEntry
quest_id (RquestId
slot (Rslot
title (	Rtitle
details (	Rdetails

objectives (	R
objectives)
area_description (	RareaDescription%
template_known (RtemplateKnown
failure (	RfailureA
targets	 (2'.warcraftbuddy.quests.v1.QuestObjectiveRtargets&
log_state_known
 (RlogStateKnown
	log_state (RlogState
complete (Rcomplete
failed (Rfailed)
objective_counts (RobjectiveCounts
expire_time (R
expireTime8
observed_questgiver_guid (RobservedQuestgiverGuid7
observed_reward_npc_guid (RobservedRewardNpcGuid2
item_protection_known (RitemProtectionKnown"Z
JournalRequest
refresh (Rrefresh.
requested_quest_ids (RrequestedQuestIds"
JournalResponse

session_id (	R	sessionId,
quest_log_revision (RquestLogRevisionD
entries (2*.warcraftbuddy.quests.v1.QuestJournalEntryRentries[
requested_templates (2*.warcraftbuddy.quests.v1.QuestJournalEntryRrequestedTemplates"p

RewardItem
index (Rindex
item_id (RitemId
count (Rcount

display_id (R	displayId"Ú
TurnInOffer
revision (Rrevision
npc_guid (RnpcGuid
quest_id (RquestId
title (	Rtitle
text (	Rtext
source (	Rsource!
can_complete (RcanComplete"
expires_in_ms (RexpiresInMs=
choices	 (2#.warcraftbuddy.quests.v1.RewardItemRchoices=
rewards
 (2#.warcraftbuddy.quests.v1.RewardItemRrewardsJ
required_items (2#.warcraftbuddy.quests.v1.RewardItemRrequiredItems%
required_money (RrequiredMoney!
reward_money (RrewardMoney
	reward_xp (RrewardXp
flags (Rflags)
reward_requested (RrewardRequested"Ë
RewardReceipt
npc_guid (RnpcGuid
quest_id (RquestId%
offer_revision (RofferRevision=
status (2%.warcraftbuddy.quests.v1.RewardStatusRstatus+
transport_written (RtransportWritten4
server_reward_observed (RserverRewardObserved2
log_removal_confirmed (RlogRemovalConfirmed2
baseline_log_revision (RbaselineLogRevision4
confirmed_log_revision	 (RconfirmedLogRevision
	reward_xp
 (RrewardXp!
reward_money (RrewardMoney!
reward_honor (RrewardHonor%
reward_talents (RrewardTalents!
reward_arena (RrewardArena
failure (	Rfailure#
server_reason (RserverReason6
backpack_evidence_known (RbackpackEvidenceKnownH
!reward_items_observed_in_backpack (RrewardItemsObservedInBackpack<
baseline_backpack_revision (RbaselineBackpackRevision>
confirmed_backpack_revision (RconfirmedBackpackRevision1
inventory_limitation (	RinventoryLimitation"’
TurnInRequest

session_id (	R	sessionId
npc_guid (RnpcGuid
quest_id (RquestId,
quest_log_revision (RquestLogRevision"õ
RewardRequest

session_id (	R	sessionId%
offer_revision (RofferRevision
npc_guid (RnpcGuid
quest_id (RquestId!
choice_index (RchoiceIndex(
expected_item_id (RexpectedItemId
	no_choice (RnoChoice"H
OfferRequest

session_id (	R	sessionId
npc_guid (RnpcGuid"Š
QuestRequest

session_id (	R	sessionId%
offer_revision (RofferRevision
npc_guid (RnpcGuid
quest_id (RquestId"¶
OperationResult+
transport_written (RtransportWritten8
server_response_received (RserverResponseReceived<
state (2&.warcraftbuddy.quests.v1.StateResponseRstate*Þ
AcceptanceStatus
NONE 
	REQUESTED
CONFIRMED_IN_LOG
UNCONFIRMED_TIMEOUT
SERVER_REJECTED
WRITE_UNCONFIRMED
ALREADY_IN_LOG
SHARE_EXPIRED
SHARE_LOG_FULL
SHARE_ALREADY_REQUESTED	*Á
ExplorationStatus
EXPLORATION_NONE 
EXPLORATION_REQUESTED
EXPLORATION_CONFIRMED
EXPLORATION_UNCONFIRMED!
EXPLORATION_WRITE_UNCONFIRMED 
EXPLORATION_ALREADY_COMPLETE*g
SharedQuestKind
SHARED_KIND_NONE 
SHARED_DETAILS
PARTY_CONFIRM
SHARED_AUTOCOMPLETE*–
RewardStatus
REWARD_NONE 
REWARD_REQUESTED
REWARD_CONFIRMED
REWARD_UNCONFIRMED
REWARD_REJECTED
REWARD_WRITE_UNCONFIRMED2Ø
BuddyQuestsJ
Discover.google.protobuf.Empty&.warcraftbuddy.quests.v1.StateResponseG
State.google.protobuf.Empty&.warcraftbuddy.quests.v1.StateResponseX
Offer%.warcraftbuddy.quests.v1.OfferRequest(.warcraftbuddy.quests.v1.OperationResultZ
Details%.warcraftbuddy.quests.v1.QuestRequest(.warcraftbuddy.quests.v1.OperationResultY
Accept%.warcraftbuddy.quests.v1.QuestRequest(.warcraftbuddy.quests.v1.OperationResulte
AcceptShared+.warcraftbuddy.quests.v1.SharedQuestRequest(.warcraftbuddy.quests.v1.OperationResult\
Journal'.warcraftbuddy.quests.v1.JournalRequest(.warcraftbuddy.quests.v1.JournalResponseZ
TurnIn&.warcraftbuddy.quests.v1.TurnInRequest(.warcraftbuddy.quests.v1.OperationResult_
ClaimReward&.warcraftbuddy.quests.v1.RewardRequest(.warcraftbuddy.quests.v1.OperationResult\
Explore'.warcraftbuddy.quests.v1.ExploreRequest(.warcraftbuddy.quests.v1.OperationResultC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.EmptyBBZ@github.com/agent-wow/agent-wow/modules/buddy-quests/api;questsv1bproto3