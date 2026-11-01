package moddisc

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	EffectRead        = "read"
	EffectQuery       = "query"
	EffectMutation    = "mutation"
	EffectUnspecified = "unspecified"
)

// ScriptMethod is explicit task-admission metadata, not an inference from an
// RPC name. Query may send game queries; read has no game mutation. Mutation
// still requires the target module's normal identity, lease and receipt guards.
type ScriptMethod struct {
	Effect      string `yaml:"effect" json:"effect"`
	Description string `yaml:"description" json:"description"`
}

func validEffect(effect string) bool {
	return effect == EffectRead || effect == EffectQuery || effect == EffectMutation || effect == EffectUnspecified
}

// Catalog is a detached snapshot of enabled PUBLIC module contracts. Revision
// hashes the deterministic contracts only; session/body identity is excluded.
type Catalog struct {
	Revision string                    `json:"revision"`
	Methods  []CatalogMethod           `json:"methods"`
	Messages map[string]CatalogMessage `json:"messages"`
	Enums    map[string]CatalogEnum    `json:"enums"`
}

// ForMethod returns a detached projection containing one public method and its
// transitive schemas. Revision still identifies the complete catalog contract,
// so a caller can pin the same API revision across separate method lookups.
func (c Catalog) ForMethod(name string) (Catalog, error) {
	var method CatalogMethod
	found := false
	for _, candidate := range c.Methods {
		if candidate.Name == name {
			method, found = candidate, true
			break
		}
	}
	if !found {
		return Catalog{}, fmt.Errorf("unknown public method %q", name)
	}
	result := Catalog{Revision: c.Revision, Methods: []CatalogMethod{method}, Messages: map[string]CatalogMessage{}, Enums: map[string]CatalogEnum{}}
	var visit func(string) error
	visit = func(key string) error {
		if _, seen := result.Messages[key]; seen {
			return nil
		}
		message, ok := c.Messages[key]
		if !ok {
			return fmt.Errorf("missing public message schema %q", key)
		}
		message.Fields = append([]CatalogField{}, message.Fields...)
		result.Messages[key] = message // Mark before following recursive/map edges.
		for _, field := range message.Fields {
			switch field.Kind {
			case "message", "group":
				if err := visit(field.Type); err != nil {
					return err
				}
			case "enum":
				enum, ok := c.Enums[field.Type]
				if !ok {
					return fmt.Errorf("missing public enum schema %q", field.Type)
				}
				enum.Values = append([]CatalogEnumValue{}, enum.Values...)
				result.Enums[field.Type] = enum
			}
		}
		return nil
	}
	for _, key := range []string{method.InputType, method.OutputType} {
		if err := visit(key); err != nil {
			return Catalog{}, err
		}
	}
	return result, nil
}

type CatalogMethod struct {
	Name        string `json:"name"`
	Module      string `json:"module"`
	Path        string `json:"path"`
	InputType   string `json:"inputType"`
	OutputType  string `json:"outputType"`
	Effect      string `json:"effect"`
	Description string `json:"description"`
}

// Callable is false for both unspecified and unrecognized effects. Discovery
// itself grants no authority to execute a method.
func (m CatalogMethod) Callable() bool {
	return m.Effect == EffectRead || m.Effect == EffectQuery || m.Effect == EffectMutation
}

type CatalogMessage struct {
	ProtobufName string         `json:"protobufName"`
	Syntax       string         `json:"syntax"`
	Fields       []CatalogField `json:"fields"`
}

type CatalogField struct {
	Name       string `json:"name"` // protobuf JSONName, not Go/CLR field name
	Number     int32  `json:"number"`
	Kind       string `json:"kind"`
	Type       string `json:"type"` // scalar kind or module-namespaced message/enum key
	Repeated   bool   `json:"repeated"`
	Map        bool   `json:"map"`
	Optional   bool   `json:"optional"` // has presence and is not required
	Required   bool   `json:"required"`
	Oneof      string `json:"oneof,omitempty"`
	HasDefault bool   `json:"hasDefault,omitempty"`
	Default    string `json:"default,omitempty"`
}

type CatalogEnum struct {
	ProtobufName string             `json:"protobufName"`
	Closed       bool               `json:"closed"`
	Values       []CatalogEnumValue `json:"values"`
}

type CatalogEnumValue struct {
	Name   string `json:"name"`
	Number int32  `json:"number"`
}

// SchemaKey identifies a type in its module's isolated protobuf resolver.
// Modules may legitimately load different versions of the same protobuf name.
func SchemaKey(module, protobufName string) string { return module + "::" + protobufName }

// Catalog returns fresh slices/maps: callers cannot change the registry or a
// later snapshot. All message/enum references are module-namespaced; scalar
// references remain scalar kind names. ProtobufName retains each real wire name.
func (r *Registry) Catalog() Catalog {
	catalog, err := r.catalog()
	if err != nil {
		// Only package-internal mutation after validated Load could reach this.
		panic(err)
	}
	return catalog
}

func (r *Registry) catalog() (Catalog, error) {
	c := Catalog{Methods: []CatalogMethod{}, Messages: map[string]CatalogMessage{}, Enums: map[string]CatalogEnum{}}
	var visit func(string, protoreflect.MessageDescriptor, map[protoreflect.FullName]bool) error
	visit = func(module string, md protoreflect.MessageDescriptor, seen map[protoreflect.FullName]bool) error {
		if seen[md.FullName()] {
			return nil
		}
		seen[md.FullName()] = true
		message := CatalogMessage{ProtobufName: string(md.FullName()), Syntax: md.Syntax().String(), Fields: []CatalogField{}}
		for i := 0; i < md.Fields().Len(); i++ {
			fd := md.Fields().Get(i)
			field := CatalogField{Name: fd.JSONName(), Number: int32(fd.Number()), Kind: fd.Kind().String(), Type: fd.Kind().String(), Repeated: fd.IsList(), Map: fd.IsMap(), Optional: fd.HasPresence() && fd.Cardinality() != protoreflect.Required, Required: fd.Cardinality() == protoreflect.Required, HasDefault: fd.HasDefault()}
			if fd.IsMap() {
				field.Repeated = true
			}
			if oneof := fd.ContainingOneof(); oneof != nil && !oneof.IsSynthetic() {
				field.Oneof = string(oneof.Name())
			}
			if fd.HasDefault() {
				field.Default = fd.Default().String()
			}
			if nested := fd.Message(); nested != nil {
				field.Type = SchemaKey(module, string(nested.FullName()))
				if err := visit(module, nested, seen); err != nil {
					return err
				}
			}
			if ed := fd.Enum(); ed != nil {
				field.Type = SchemaKey(module, string(ed.FullName()))
				enum := CatalogEnum{ProtobufName: string(ed.FullName()), Closed: ed.IsClosed(), Values: []CatalogEnumValue{}}
				for j := 0; j < ed.Values().Len(); j++ {
					v := ed.Values().Get(j)
					enum.Values = append(enum.Values, CatalogEnumValue{Name: string(v.Name()), Number: int32(v.Number())})
				}
				if prior, ok := c.Enums[field.Type]; ok && !reflect.DeepEqual(prior, enum) {
					return fmt.Errorf("conflicting public enum schema %s", field.Type)
				}
				c.Enums[field.Type] = enum
			}
			message.Fields = append(message.Fields, field)
		}
		sort.Slice(message.Fields, func(i, j int) bool { return message.Fields[i].Number < message.Fields[j].Number })
		name := SchemaKey(module, string(md.FullName()))
		if prior, ok := c.Messages[name]; ok && !reflect.DeepEqual(prior, message) {
			return fmt.Errorf("conflicting public message schema %s", name)
		}
		c.Messages[name] = message
		return nil
	}
	// Definitions and aliases are maps; sort the published array explicitly.
	for name, d := range r.definitions {
		for alias, method := range d.rpc {
			meta, ok := d.manifest.ScriptAPI[alias]
			if !ok {
				meta.Effect = EffectUnspecified
			}
			md := method.Descriptor()
			c.Methods = append(c.Methods, CatalogMethod{Name: name + "." + alias, Module: name, Path: method.Path(), InputType: SchemaKey(name, string(md.Input().FullName())), OutputType: SchemaKey(name, string(md.Output().FullName())), Effect: meta.Effect, Description: meta.Description})
			// Each module resolver keeps its own schemas. A per-method visited
			// set terminates recursive graphs without hiding other public types.
			seen := map[protoreflect.FullName]bool{}
			if err := visit(name, md.Input(), seen); err != nil {
				return Catalog{}, err
			}
			if err := visit(name, md.Output(), seen); err != nil {
				return Catalog{}, err
			}
		}
	}
	sort.Slice(c.Methods, func(i, j int) bool { return c.Methods[i].Name < c.Methods[j].Name })
	contracts := struct {
		Methods  []CatalogMethod           `json:"methods"`
		Messages map[string]CatalogMessage `json:"messages"`
		Enums    map[string]CatalogEnum    `json:"enums"`
	}{c.Methods, c.Messages, c.Enums}
	data, err := json.Marshal(contracts)
	if err != nil {
		return Catalog{}, err
	}
	c.Revision = fmt.Sprintf("%x", sha256.Sum256(data))
	return c, nil
}
