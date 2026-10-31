package buddyactions

import (
	"crypto/sha256"
	"fmt"
	"os"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// This is the actual descriptor from c42e0b54e15d1c0bbd9df222b1abbbc62266ad0f,
// whose loot/inventory binaries decode actions responses using tags73..75.
func legacyActionDescriptor(t *testing.T, name string) protoreflect.MessageDescriptor {
	t.Helper()
	b, err := os.ReadFile("testdata/c42-actions.module.pb")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(b)); got != "2195d47792dc0580726a65f75c8f98f80d68fe8fa4095b4cfaf844581d8adeea" {
		t.Fatalf("legacy fixture changed: %s", got)
	}
	set := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(b, set); err != nil {
		t.Fatal(err)
	}
	files, err := protodesc.NewFiles(set)
	if err != nil {
		t.Fatal(err)
	}
	d, err := files.FindDescriptorByName(protoreflect.FullName("warcraftbuddy.actions.v1." + name))
	if err != nil {
		t.Fatal(err)
	}
	return d.(protoreflect.MessageDescriptor)
}

func populatedBuffState() *actionsv1.State {
	return &actionsv1.State{
		Following: true, Healing: true, Moving: true, CastPending: true,
		Reason: "legacy fields remain observations", TransportWrites: 123,
		Position:   &actionsv1.ClientPosition{Known: true, MapKnown: true, X: -6423, Y: 533, Z: 385, Revision: 86},
		ClassKnown: true, ClassId: 9, ClassName: "warlock", KnownShadowBolt: true,
		InteractionOwner: "loot", InteractionLeaseRemainingMs: 3000,
		Practicing: true, PracticePhase: "selected", PracticeTargetGuid: 1234,
		HarvestGuardGuids: []uint64{3, 8}, ClientMovementObservationConfirmed: true,
		ClientMovementObservationFailures: 2,
		Buffing:                           true, BuffLeaseRemainingMs: 29999, BuffReason: "enabled_waiting_for_safe_known_spell",
		BuffAttempts: 7, BuffsConfirmed: 6,
		LastBuff:  &actionsv1.BuffOperation{Sequence: 7, SpellId: 687, TargetGuid: 8, Phase: "confirmed", TransportWritten: true, ServerCastGo: true, AuraConfirmed: true, AuraRevision: 9},
		SessionId: "current-body-session", SelfGuid: 8,
	}
}

func TestBuffStateWireCompatibleWithC42Consumers(t *testing.T) {
	stateDesc := legacyActionDescriptor(t, "State")
	leaseDesc := stateDesc.ParentFile().Messages().ByName("InteractionLease")
	for _, reason := range []string{"enabled_waiting_for_safe_known_spell", "explicitly_disabled"} {
		for _, envelope := range []string{"status", "interaction_lease"} {
			t.Run(reason+"/"+envelope, func(t *testing.T) {
				state := populatedBuffState()
				state.BuffReason = reason
				state.Buffing = reason != "explicitly_disabled"
				var wire proto.Message = state
				old := dynamicpb.NewMessage(stateDesc)
				if envelope == "interaction_lease" {
					wire = &actionsv1.InteractionLease{Token: "actual-lease-token", State: state}
					old = dynamicpb.NewMessage(leaseDesc)
				}
				b, err := proto.Marshal(wire)
				if err != nil {
					t.Fatal(err)
				}
				if err := proto.Unmarshal(b, old); err != nil {
					t.Fatalf("actual c42 consumer cannot decode new response: %v", err)
				}
				var got protoreflect.Message = old
				if envelope == "interaction_lease" {
					if old.Get(leaseDesc.Fields().ByName("token")).String() != "actual-lease-token" {
						t.Fatal("legacy interaction envelope changed")
					}
					got = old.Get(leaseDesc.Fields().ByName("state")).Message()
				}
				for _, number := range []protoreflect.FieldNumber{70, 73, 74, 75} {
					if got.Has(stateDesc.Fields().ByNumber(number)) {
						t.Fatalf("buff metadata fabricated legacy goal at tag%d", number)
					}
				}
				// Remove only new fields to form the same legacy-known observation.
				baseline := proto.Clone(state).(*actionsv1.State)
				baseline.ProtoReflect().Range(func(f protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
					if f.Number() >= 73 {
						baseline.ProtoReflect().Clear(f)
					}
					return true
				})
				b, _ = proto.Marshal(baseline)
				want := dynamicpb.NewMessage(stateDesc)
				if err := proto.Unmarshal(b, want); err != nil {
					t.Fatal(err)
				}
				got.SetUnknown(nil)
				want.SetUnknown(nil)
				if !proto.Equal(got.Interface(), want) {
					t.Fatal("legacy-known status fields changed")
				}
			})
		}
	}
}

func TestBuffStateDescriptorUsesUnoccupiedRangeAndRoundTrips(t *testing.T) {
	s := populatedBuffState()
	d := s.ProtoReflect().Descriptor()
	legacy := legacyActionDescriptor(t, "State")
	for i := 0; i < legacy.Fields().Len(); i++ {
		old := legacy.Fields().Get(i)
		if old.Number() >= 70 {
			continue
		}
		current := d.Fields().ByNumber(old.Number())
		if current == nil || !proto.Equal(protodesc.ToFieldDescriptorProto(old), protodesc.ToFieldDescriptorProto(current)) {
			t.Errorf("legacy field%d changed", old.Number())
		}
	}
	published, err := os.ReadFile("module.pb")
	if err != nil {
		t.Fatal(err)
	}
	set := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(published, set); err != nil {
		t.Fatal(err)
	}
	files, err := protodesc.NewFiles(set)
	if err != nil {
		t.Fatal(err)
	}
	publishedState, err := files.FindDescriptorByName(d.FullName())
	if err != nil || !proto.Equal(protodesc.ToDescriptorProto(d), protodesc.ToDescriptorProto(publishedState.(protoreflect.MessageDescriptor))) {
		t.Fatalf("published module descriptor differs from generated API: %v", err)
	}
	for i, name := range []protoreflect.Name{"buffing", "buff_lease_remaining_ms", "buff_reason", "buff_attempts", "buffs_confirmed", "last_buff", "session_id", "self_guid"} {
		f := d.Fields().ByName(name)
		if f == nil || f.Number() != protoreflect.FieldNumber(100+i) {
			t.Errorf("%s must use tag%d, got %v", name, 100+i, f)
		}
	}
	for n := protoreflect.FieldNumber(73); n <= 99; n++ {
		if d.Fields().ByNumber(n) != nil || !d.ReservedRanges().Has(n) {
			t.Errorf("legacy tag%d must remain reserved", n)
		}
	}
	if !d.ReservedRanges().Has(70) {
		t.Error("legacy Coldridge tag70 must remain reserved")
	}
	b, err := proto.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	out := &actionsv1.State{}
	if err := proto.Unmarshal(b, out); err != nil || !proto.Equal(s, out) {
		t.Fatalf("new buff status lost metadata: %v", err)
	}
}
