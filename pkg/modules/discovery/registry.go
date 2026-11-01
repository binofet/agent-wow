package moddisc

import (
	"maps"
	"slices"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func (r *Registry) Order() []string { return slices.Clone(r.order) }

func (r *Registry) Lookup(name string) (*Definition, bool) {
	d, ok := r.definitions[name]
	return d, ok
}

func (d *Definition) Manifest() Manifest {
	m := d.manifest
	m.Requires = slices.Clone(m.Requires)
	m.RPC = maps.Clone(m.RPC)
	m.ScriptAPI = maps.Clone(m.ScriptAPI)
	m.Packets = maps.Clone(m.Packets)
	return m
}

func (d *Definition) Requires(name string) bool { return slices.Contains(d.manifest.Requires, name) }

func (d *Definition) RPC(alias string) (Method, bool) {
	md, ok := d.rpc[alias]
	return md, ok
}

func (d *Definition) Packet(opcode uint16) (Method, bool) {
	md, ok := d.packets[opcode]
	return md, ok
}

func (d *Definition) BeforeLogout() (Method, bool) {
	if d.beforeLogout == nil {
		return Method{}, false
	}
	return *d.beforeLogout, true
}

func (d *Definition) Types() *dynamicpb.Types { return d.types }

func (m Method) Path() string { return m.path }

func (m Method) Descriptor() protoreflect.MethodDescriptor { return m.descriptor }
