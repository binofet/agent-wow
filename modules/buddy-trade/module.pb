
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
Î
modules/buddy-trade/trade.protowarcraftbuddy.trade.v1api/module/v1/session.protogoogle/protobuf/empty.proto"ž
Item
slot (Rslot
entry (Rentry
count (Rcount
	item_guid (RitemGuid
name (	Rname%
metadata_known (RmetadataKnown"Á
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
limitation"I
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
copper (Rcopper2’

BuddyTradeA
GetState.google.protobuf.Empty.warcraftbuddy.trade.v1.StateK
Open$.warcraftbuddy.trade.v1.BoundRequest.warcraftbuddy.trade.v1.StateO
	OfferItem#.warcraftbuddy.trade.v1.ItemRequest.warcraftbuddy.trade.v1.StateO
	OfferGold#.warcraftbuddy.trade.v1.GoldRequest.warcraftbuddy.trade.v1.StateM
Cancel$.warcraftbuddy.trade.v1.BoundRequest.warcraftbuddy.trade.v1.StateC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.Empty>
BeforeLogout.google.protobuf.Empty.google.protobuf.EmptyB@Z>github.com/agent-wow/agent-wow/modules/buddy-trade/api;tradev1bproto3