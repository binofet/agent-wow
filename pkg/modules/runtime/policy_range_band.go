package modrt

import (
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// This is host attestation of the installed typed protocol AND this host's
// argument-preserving bridge. Native observation data cannot grant the flag.
func (m *Manager) targetRangeBandSupported() bool {
	_, md, err := m.lookup(actionsModule, "approach_target", true)
	return err == nil && md.Path() == actionsv1.BuddyActions_ApproachTarget_FullMethodName && targetRangeBandDescriptor(md.Descriptor())
}
func targetRangeBandDescriptor(method protoreflect.MethodDescriptor) bool {
	if method == nil || method.Input().FullName() != ("warcraftbuddy.actions.v1.ApproachTargetRequest") || method.Output().FullName() != ("warcraftbuddy.actions.v1.State") {
		return false
	}
	check := func(message protoreflect.MessageDescriptor, name protoreflect.Name, number protoreflect.FieldNumber, kind protoreflect.Kind, list bool) bool {
		f := message.Fields().ByName(name)
		return f != nil && f.Number() == number && f.Kind() == kind && f.IsList() == list && !f.IsMap()
	}
	if !check(method.Input(), "min_range_yards", 9, protoreflect.FloatKind, false) || !check(method.Input(), "clearance_target_guids", 10, protoreflect.Uint64Kind, true) || !check(method.Input(), "clearance_yards", 11, protoreflect.FloatKind, false) {
		return false
	}
	output := method.Output().Fields().ByName("target_movement")
	return output != nil && output.Kind() == protoreflect.MessageKind && check(output.Message(), "min_range_yards", 8, protoreflect.FloatKind, false) && check(output.Message(), "clearance_target_guids", 9, protoreflect.Uint64Kind, true) && check(output.Message(), "clearance_yards", 10, protoreflect.FloatKind, false)
}
