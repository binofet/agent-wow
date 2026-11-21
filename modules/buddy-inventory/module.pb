
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
Çd
modules/buddy-world/world.protowarcraftbuddy.world.v1api/module/v1/session.protogoogle/protobuf/empty.proto"ö
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

aura_state< (R	auraState"Ž
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
has_enhancements (RhasEnhancements"È
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
state (Rstate"¤
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
activeCast"¨
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
permanent_bonus (RpermanentBonus"Å
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
visit_revision/ (RvisitRevision"q
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
gaps (2&.warcraftbuddy.world.v1.CombatEventGapRgaps2û

BuddyWorldi
CombatEvents+.warcraftbuddy.world.v1.CombatEventsRequest,.warcraftbuddy.world.v1.CombatEventsResponse]
Snapshot'.warcraftbuddy.world.v1.SnapshotRequest(.warcraftbuddy.world.v1.SnapshotResponse^
ObserveClientMovement-.warcraftbuddy.world.v1.ClientMovementRequest.google.protobuf.EmptyC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.EmptyB@Z>github.com/agent-wow/agent-wow/modules/buddy-world/api;worldv1bproto3
¯$
modules/buddy-trade/trade.protowarcraftbuddy.trade.v1api/module/v1/session.protogoogle/protobuf/empty.proto"ž
Item
slot (Rslot
entry (Rentry
count (Rcount
	item_guid (RitemGuid
name (	Rname%
metadata_known (RmetadataKnown"‚	
State

session_id (	R	sessionId
revision (Rrevision
phase (	Rphase!
partner_guid (RpartnerGuid)
partner_accepted (RpartnerAccepted8
accept_transport_written (RacceptTransportWrittenL
own_requested_items (2.warcraftbuddy.trade.v1.ItemRownRequestedItems0
own_requested_copper (RownRequestedCopperA
partner_items	 (2.warcraftbuddy.trade.v1.ItemRpartnerItems%
partner_copper
 (RpartnerCopper,
last_server_status (RlastServerStatus
reason (	Rreason)
completed_trades (RcompletedTrades)
transport_writes (RtransportWrites+
malformed_packets (RmalformedPackets%
backpack_known (RbackpackKnown+
backpack_revision (RbackpackRevision8
backpack (2.warcraftbuddy.trade.v1.ItemRbackpack

limitation (	R
limitation!
copper_known (RcopperKnown
copper (Rcopper'
copper_revision (RcopperRevision2
junk_disposal_enabled (RjunkDisposalEnabledR
last_junk_disposal (2$.warcraftbuddy.trade.v1.JunkDisposalRlastJunkDisposal2
junk_stacks_destroyed (RjunkStacksDestroyed
junk_reason (	R
junkReason"Õ
JunkDisposal#
backpack_slot (RbackpackSlot
	item_guid (RitemGuid
entry (Rentry
count (Rcount
name (	Rname
status (	Rstatus+
backpack_revision (RbackpackRevision"&

JunkToggle
enabled (Renabled"È
JunkRequest

session_id (	R	sessionId
revision (Rrevision+
backpack_revision (RbackpackRevision#
backpack_slot (RbackpackSlot,
expected_item_guid (RexpectedItemGuid"I
BoundRequest

session_id (	R	sessionId
revision (Rrevision"ç
ItemRequest

session_id (	R	sessionId
revision (Rrevision+
backpack_revision (RbackpackRevision#
backpack_slot (RbackpackSlot,
expected_item_guid (RexpectedItemGuid

trade_slot (R	tradeSlot"`
GoldRequest

session_id (	R	sessionId
revision (Rrevision
copper (Rcopper"4
ItemStat
type (Rtype
value (Rvalue"Z
WeaponDamage
minimum (Rminimum
maximum (Rmaximum
school (Rschool"	
ItemDetails
item_id (RitemId
	available (R	available
pending (Rpending
reason (	Rreason
name (	Rname

item_class (R	itemClass
subclass (Rsubclass%
inventory_type (RinventoryType
quality	 (Rquality

item_level
 (R	itemLevel%
required_level (RrequiredLevel
	buy_price (RbuyPrice

sell_price (R	sellPrice'
allowable_class (RallowableClass%
allowable_race (RallowableRace%
required_skill (RrequiredSkill.
required_skill_rank (RrequiredSkillRank%
required_spell (RrequiredSpell6
stats (2 .warcraftbuddy.trade.v1.ItemStatRstats
armor (Rarmor>
damages (2$.warcraftbuddy.trade.v1.WeaponDamageRdamages
delay_ms (RdelayMs
flags (Rflags
flags2 (Rflags2
	has_spell (RhasSpell>
required_reputation_faction (RrequiredReputationFaction8
required_reputation_rank (RrequiredReputationRank.
required_honor_rank (RrequiredHonorRank,
required_city_rank (RrequiredCityRank4
armor_comparison_known (RarmorComparisonKnown6
weapon_comparison_known (RweaponComparisonKnown"/
LookupItemsRequest
item_ids (RitemIds"o
LookupItemsResponse

session_id (	R	sessionId9
items (2#.warcraftbuddy.trade.v1.ItemDetailsRitems2 

BuddyTradeA
GetState.google.protobuf.Empty.warcraftbuddy.trade.v1.StateK
Open$.warcraftbuddy.trade.v1.BoundRequest.warcraftbuddy.trade.v1.StateO
	OfferItem#.warcraftbuddy.trade.v1.ItemRequest.warcraftbuddy.trade.v1.StateO
	OfferGold#.warcraftbuddy.trade.v1.GoldRequest.warcraftbuddy.trade.v1.StateM
Cancel$.warcraftbuddy.trade.v1.BoundRequest.warcraftbuddy.trade.v1.StateQ
JunkDisposal".warcraftbuddy.trade.v1.JunkToggle.warcraftbuddy.trade.v1.StateQ
DestroyJunk#.warcraftbuddy.trade.v1.JunkRequest.warcraftbuddy.trade.v1.Statef
LookupItems*.warcraftbuddy.trade.v1.LookupItemsRequest+.warcraftbuddy.trade.v1.LookupItemsResponseC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.Empty>
BeforeLogout.google.protobuf.Empty.google.protobuf.EmptyB@Z>github.com/agent-wow/agent-wow/modules/buddy-trade/api;tradev1bproto3
 =
'modules/buddy-inventory/inventory.protowarcraftbuddy.inventory.v1api/module/v1/session.protogoogle/protobuf/empty.protomodules/buddy-world/world.protomodules/buddy-trade/trade.proto"ƒ
ItemRef
bag (Rbag
slot (Rslot
	item_guid (RitemGuid
entry (Rentry
stack_count (R
stackCount"â
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
gear_blocked_reason (	RgearBlockedReason"‘

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
displaced_item_guid (RdisplacedItemGuid"ã
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
	equipment (2*.warcraftbuddy.world.v1.EquipmentSlotStateR	equipment"U
DiscoverRequest!
vendor_entry (RvendorEntry
vendor_guid (R
vendorGuid"²
VendorA
creature (2%.warcraftbuddy.world.v1.CreatureStateRcreature"
interactable (Rinteractable%
blocked_reason (	RblockedReason
distance (Rdistance"€
	Discovery7
state (2!.warcraftbuddy.inventory.v1.StateRstate:
nearby (2".warcraftbuddy.inventory.v1.VendorRnearby"Ž
MoveRequest

session_id (	R	sessionId

request_id (	R	requestId+
expected_revision (RexpectedRevision>
expected_inventory_revision (RexpectedInventoryRevision;
source (2#.warcraftbuddy.inventory.v1.ItemRefRsource'
destination_bag (RdestinationBag)
destination_slot (RdestinationSlotC
expected_destination_item_guid (RexpectedDestinationItemGuid"Õ
EquipRequest

session_id (	R	sessionId

request_id (	R	requestId+
expected_revision (RexpectedRevision>
expected_inventory_revision (RexpectedInventoryRevision;
source (2#.warcraftbuddy.inventory.v1.ItemRefRsource%
equipment_slot (RequipmentSlot6
expected_container_guid (RexpectedContainerGuid"–
SellRequest

session_id (	R	sessionId

request_id (	R	requestId+
expected_revision (RexpectedRevision>
expected_inventory_revision (RexpectedInventoryRevision;
source (2#.warcraftbuddy.inventory.v1.ItemRefRsource
vendor_guid (R
vendorGuidD
expected_unit_sell_price_copper (RexpectedUnitSellPriceCopper8
expected_copper_revision (RexpectedCopperRevision"â
EquipGearRequest

session_id (	R	sessionId

request_id (	R	requestId+
expected_revision (RexpectedRevision>
expected_inventory_revision (RexpectedInventoryRevision;
source (2#.warcraftbuddy.inventory.v1.ItemRefRsource%
equipment_slot (RequipmentSlot>
expected_equipment_revision (RexpectedEquipmentRevision=
expected_equipped_item_guid (RexpectedEquippedItemGuid@
require_conservative_upgrade	 (RrequireConservativeUpgrade"™
	RollOffer
	roll_guid (RrollGuid
revision (Rrevision
visit_id (RvisitId
map_id (RmapId
	loot_slot (RlootSlot

item_entry (R	itemEntry#
random_suffix (RrandomSuffix'
random_property (RrandomProperty

item_count	 (R	itemCount!
offered_mask
 (RofferedMask!
remaining_ms (RremainingMs
	available (R	available
phase (	Rphase"æ
RollReceipt

request_id (	R	requestId
	roll_guid (RrollGuid%
offer_revision (RofferRevision
visit_id (RvisitId
choice (	Rchoice/
transport_attempted (RtransportAttempted+
transport_written (RtransportWritten0
server_vote_observed (RserverVoteObserved
phase	 (	Rphase
	loot_slot
 (RlootSlot"ë
	RollState

session_id (	R	sessionId
	self_guid (RselfGuid
revision (Rrevision
visit_id (RvisitId=
offers (2%.warcraftbuddy.inventory.v1.RollOfferRoffersC
receipts (2'.warcraftbuddy.inventory.v1.RollReceiptRreceipts;
player (2#.warcraftbuddy.world.v1.PlayerStateRplayer'
equipment_known (RequipmentKnown-
equipment_revision	 (RequipmentRevisionH
	equipment
 (2*.warcraftbuddy.world.v1.EquipmentSlotStateR	equipment!
skills_known (RskillsKnown'
skills_revision (RskillsRevision'
skills_complete (RskillsComplete:
skills (2".warcraftbuddy.world.v1.SkillStateRskills?
metadata (2#.warcraftbuddy.trade.v1.ItemDetailsRmetadata
reason (	Rreason"
RollRequest

session_id (	R	sessionId

request_id (	R	requestId
	roll_guid (RrollGuid6
expected_offer_revision (RexpectedOfferRevision
choice (	Rchoice
visit_id (RvisitId,
expected_loot_slot (RexpectedLootSlot2¸
BuddyInventoryF
Rolls.google.protobuf.Empty%.warcraftbuddy.inventory.v1.RollStateV
Roll'.warcraftbuddy.inventory.v1.RollRequest%.warcraftbuddy.inventory.v1.RollStateE
GetState.google.protobuf.Empty!.warcraftbuddy.inventory.v1.State^
Discover+.warcraftbuddy.inventory.v1.DiscoverRequest%.warcraftbuddy.inventory.v1.DiscoveryR
Move'.warcraftbuddy.inventory.v1.MoveRequest!.warcraftbuddy.inventory.v1.StateT
Equip(.warcraftbuddy.inventory.v1.EquipRequest!.warcraftbuddy.inventory.v1.State\
	EquipGear,.warcraftbuddy.inventory.v1.EquipGearRequest!.warcraftbuddy.inventory.v1.StateR
Sell'.warcraftbuddy.inventory.v1.SellRequest!.warcraftbuddy.inventory.v1.StateC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.Empty>
BeforeLogout.google.protobuf.Empty.google.protobuf.EmptyBHZFgithub.com/agent-wow/agent-wow/modules/buddy-inventory/api;inventoryv1bproto3