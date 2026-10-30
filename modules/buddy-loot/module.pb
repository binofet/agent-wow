
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
³

modules/buddy-loot/loot.protowarcraftbuddy.loot.v1api/module/v1/session.protogoogle/protobuf/empty.proto")
EnableRequest
enabled (Renabled"Y
ItemReceipt
item_id (RitemId
count (Rcount
	loot_slot (RlootSlot"Ï
State
enabled (Renabled
phase (	Rphase
reason (	Rreason.
pending_corpse_guid (RpendingCorpseGuid(
last_corpse_guid (RlastCorpseGuid)
transport_writes (RtransportWrites2
loot_windows_observed (RlootWindowsObserved4
items_stored_confirmed (RitemsStoredConfirmed,
loot_slots_removed	 (RlootSlotsRemoved4
money_awarded_notified
 (RmoneyAwardedNotified+
releases_observed (RreleasesObserved&
last_loot_error (RlastLootError0
last_inventory_error (RlastInventoryError+
malformed_packets (RmalformedPacketsE
recent_items (2".warcraftbuddy.loot.v1.ItemReceiptRrecentItems0
enabled_remaining_ms (RenabledRemainingMs2 
	BuddyLootL
Enable$.warcraftbuddy.loot.v1.EnableRequest.warcraftbuddy.loot.v1.State@
GetState.google.protobuf.Empty.warcraftbuddy.loot.v1.StateC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.Empty>
BeforeLogout.google.protobuf.Empty.google.protobuf.EmptyB>Z<github.com/agent-wow/agent-wow/modules/buddy-loot/api;lootv1bproto3