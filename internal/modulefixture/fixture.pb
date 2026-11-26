
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
Ä
api/module/v1/session.protoagentwow.module.v1google/protobuf/any.protogoogle/protobuf/empty.proto"Œ
WorldPacket
opcode (Ropcode
payload (RpayloadK
transport_scope (2".agentwow.module.v1.TransportScopeRtransportScope"’
SendPacketRequest
opcode (Ropcode
payload (RpayloadK
transport_scope (2".agentwow.module.v1.TransportScopeRtransportScope"Û
TransportScope#
connection_id (	RconnectionId
	self_guid (RselfGuid
visit_bound (R
visitBound%
visit_revision (RvisitRevision
map_id (RmapId'
motion_revision (RmotionRevision&
spline_id_known (RsplineIdKnown
	spline_id (RsplineId'
spline_revision	 (RsplineRevision4
spline_source_revision
 (RsplineSourceRevision#
spline_active (RsplineActive0
spline_source_opcode (RsplineSourceOpcodeP
world_transfer (2).agentwow.module.v1.ObservedWorldTransferRworldTransfer2
summon_offer_sequence (RsummonOfferSequence" 	
ObservedWorldTransfer
revision (Rrevision
phase (	Rphase
known (Rknown#
connection_id (	RconnectionId
	self_guid (RselfGuid.
from_visit_revision (RfromVisitRevision
from_map_id (R	fromMapId0
from_motion_revision (RfromMotionRevision/
from_spline_id_known	 (RfromSplineIdKnown$
from_spline_id
 (RfromSplineId0
from_spline_revision (RfromSplineRevision=
from_spline_source_revision (RfromSplineSourceRevision9
from_spline_source_opcode (RfromSplineSourceOpcode,
from_spline_active (RfromSplineActive6
pending_motion_revision (RpendingMotionRevision"
target_map_id (RtargetMapId*
to_visit_revision (RtoVisitRevision
	to_map_id (RtoMapId2
entry_motion_revision (RentryMotionRevisionA
worldport_ack_motion_revision (RworldportAckMotionRevision2
worldport_ack_written (RworldportAckWritten2
worldport_ack_unknown (RworldportAckUnknown:
completion_report_written (RcompletionReportWrittenI
!completion_report_motion_revision (RcompletionReportMotionRevision
reason (	Rreason(
target_map_known (RtargetMapKnown"5
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
¢
$internal/modulefixture/fixture.protoagentwow.fixture.v1api/module/v1/session.protogoogle/protobuf/empty.proto"ˆ
Request
number (Rnumber
payload (Rpayload
opcode (Ropcode
failure (	Rfailure
delay_ms (RdelayMs"z
Result
number (Rnumber
payload (Rpayload$
client_time_ms (RclientTimeMs
packets (Rpackets2Ô
FixtureD
Execute.agentwow.fixture.v1.Request.agentwow.fixture.v1.ResultC
OnPacket.agentwow.module.v1.WorldPacket.google.protobuf.Empty>
BeforeLogout.google.protobuf.Empty.google.protobuf.EmptyBEZCgithub.com/agent-wow/agent-wow/internal/modulefixture/api;fixturev1bproto3