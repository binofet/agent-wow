
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
Á#
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
school (Rschool"¢
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
required_city_rank (RrequiredCityRank"/
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