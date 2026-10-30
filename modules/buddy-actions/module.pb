
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
¾

#modules/buddy-actions/actions.protowarcraftbuddy.actions.v1api/module/v1/session.protogoogle/protobuf/empty.proto")
ToggleRequest
enabled (Renabled"‚
State
	following (R	following
healing (Rhealing
moving (Rmoving*
known_lesser_heal (RknownLesserHeal!
cast_pending (RcastPending
reason (	Rreason)
transport_writes (RtransportWrites#
heal_attempts (RhealAttempts,
server_cast_starts	 (RserverCastStarts$
server_cast_go
 (RserverCastGo*
last_cast_failure (RlastCastFailure+
malformed_packets (RmalformedPackets9
follow_lease_remaining_ms (RfollowLeaseRemainingMs;
healing_lease_remaining_ms (RhealingLeaseRemainingMs

limitation (	R
limitation-
motion_unconfirmed (RmotionUnconfirmed2Ä
BuddyActionsR
Follow'.warcraftbuddy.actions.v1.ToggleRequest.warcraftbuddy.actions.v1.StateS
Healing'.warcraftbuddy.actions.v1.ToggleRequest.warcraftbuddy.actions.v1.StateC
HealOnce.google.protobuf.Empty.warcraftbuddy.actions.v1.StateA
Status.google.protobuf.Empty.warcraftbuddy.actions.v1.StateC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.Empty>
BeforeLogout.google.protobuf.Empty.google.protobuf.EmptyBDZBgithub.com/agent-wow/agent-wow/modules/buddy-actions/api;actionsv1bproto3