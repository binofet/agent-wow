package moddisc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	modtest "github.com/agent-wow/agent-wow/pkg/modules/internal/testutil"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
)

func catalogDescriptor(t *testing.T, kind descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FileDescriptorProto {
	t.Helper()
	field := func(name string, number int32, typ descriptorpb.FieldDescriptorProto_Type, target string) *descriptorpb.FieldDescriptorProto {
		f := &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(number), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: typ.Enum()}
		if target != "" {
			f.TypeName = proto.String(target)
		}
		return f
	}
	child := field("child_node", 1, kind, "")
	if kind == descriptorpb.FieldDescriptorProto_TYPE_MESSAGE {
		child.TypeName = proto.String(".catalog.Node")
	}
	attributes := field("attributes", 2, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, ".catalog.Node.AttributesEntry")
	attributes.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	choice := field("text", 4, descriptorpb.FieldDescriptorProto_TYPE_STRING, "")
	choice.OneofIndex = proto.Int32(0)
	enabled := field("enabled", 5, descriptorpb.FieldDescriptorProto_TYPE_BOOL, "")
	enabled.OneofIndex, enabled.Proto3Optional = proto.Int32(1), proto.Bool(true)
	return &descriptorpb.FileDescriptorProto{Name: proto.String("catalog.proto"), Package: proto.String("catalog"), Syntax: proto.String("proto3"),
		EnumType: []*descriptorpb.EnumDescriptorProto{{Name: proto.String("Mode"), Value: []*descriptorpb.EnumValueDescriptorProto{{Name: proto.String("NONE"), Number: proto.Int32(0)}, {Name: proto.String("ACTIVE"), Number: proto.Int32(1)}}}},
		MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("Node"), OneofDecl: []*descriptorpb.OneofDescriptorProto{{Name: proto.String("choice")}, {Name: proto.String("_enabled")}},
			Field:      []*descriptorpb.FieldDescriptorProto{child, attributes, field("mode", 3, descriptorpb.FieldDescriptorProto_TYPE_ENUM, ".catalog.Mode"), choice, enabled},
			NestedType: []*descriptorpb.DescriptorProto{{Name: proto.String("AttributesEntry"), Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)}, Field: []*descriptorpb.FieldDescriptorProto{field("key", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING, ""), field("value", 2, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, ".catalog.Node")}}}}},
		Service: []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("Public"), Method: []*descriptorpb.MethodDescriptorProto{{Name: proto.String("Get"), InputType: proto.String(".catalog.Node"), OutputType: proto.String(".catalog.Node")}}}},
	}
}

func writeCatalogModule(t *testing.T, root, name, effect string, kind descriptorpb.FieldDescriptorProto_Type) {
	t.Helper()
	dir := modtest.WriteModule(t, root, name, "")
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{catalogDescriptor(t, kind)}}
	data, err := proto.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	modtest.WriteFile(t, filepath.Join(dir, "module.pb"), data)
	manifest := "api_version: 1\nenabled: true\ncompose: {file: compose.yaml, service: module}\ngrpc: {descriptor_set: module.pb}\nrpc: {get: /catalog.Public/Get}\n"
	if effect != "" {
		manifest += "script_api:\n  get: {effect: " + effect + ", description: Explicit method contract}\n"
	}
	modtest.WriteFile(t, filepath.Join(dir, "module.yaml"), []byte(manifest))
}

func loadCatalog(t *testing.T, root string) (*Registry, Catalog) {
	t.Helper()
	r, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return r, r.Catalog()
}

func TestCatalogRecursiveMapsEnumsJSONNamesAndPresence(t *testing.T) {
	root := t.TempDir()
	writeCatalogModule(t, root, "example", EffectRead, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
	_, c := loadCatalog(t, root)
	if len(c.Methods) != 1 || !c.Methods[0].Callable() || c.Methods[0].Name != "example.get" || c.Methods[0].InputType != "example::catalog.Node" || len(c.Messages) != 2 {
		t.Fatal(c)
	}
	f := c.Messages["example::catalog.Node"].Fields
	if c.Messages["example::catalog.Node"].ProtobufName != "catalog.Node" || f[0].Name != "childNode" || f[0].Type != "example::catalog.Node" || !f[0].Optional || f[0].Repeated || !f[1].Map || !f[1].Repeated || f[1].Type != "example::catalog.Node.AttributesEntry" || f[2].Kind != "enum" || f[2].Type != "example::catalog.Mode" || f[3].Oneof != "choice" || !f[4].Optional || f[4].Oneof != "" {
		t.Fatal(f)
	}
	if got := c.Enums["example::catalog.Mode"]; got.ProtobufName != "catalog.Mode" || len(got.Values) != 2 || got.Values[1].Name != "ACTIVE" || got.Values[1].Number != 1 || got.Closed {
		t.Fatal(got)
	}
}

func TestCatalogDeterministicDetachedAndContractSensitive(t *testing.T) {
	root := t.TempDir()
	writeCatalogModule(t, root, "zeta", EffectRead, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
	writeCatalogModule(t, root, "alpha", "", descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
	r, c := loadCatalog(t, root)
	if c.Methods[0].Name != "alpha.get" || c.Methods[0].Effect != EffectUnspecified || c.Methods[0].Callable() || (CatalogMethod{Effect: "unknown"}).Callable() {
		t.Fatal(c.Methods)
	}
	for i := 0; i < 5; i++ {
		if !reflect.DeepEqual(c, r.Catalog()) {
			t.Fatal("non-deterministic catalog")
		}
	}
	revision := c.Revision
	d, _ := r.Lookup("zeta")
	manifest := d.Manifest()
	manifest.ScriptAPI["get"] = ScriptMethod{Effect: EffectMutation, Description: "caller forgery"}
	if r.Catalog().Revision != revision {
		t.Fatal("caller changed script admission metadata through manifest copy")
	}
	c.Methods[0].Effect = EffectMutation
	c.Messages["alpha::catalog.Node"].Fields[0].Name = "forged"
	c.Enums["alpha::catalog.Mode"].Values[0].Name = "forged"
	delete(c.Messages, "alpha::catalog.Node.AttributesEntry")
	if fresh := r.Catalog(); fresh.Revision != revision || fresh.Methods[0].Effect != EffectUnspecified || fresh.Messages["alpha::catalog.Node"].Fields[0].Name != "childNode" || fresh.Enums["alpha::catalog.Mode"].Values[0].Name != "NONE" {
		t.Fatal("caller mutated validated catalog")
	}
	modtest.EditFile(t, filepath.Join(root, "zeta", "module.yaml"), "effect: read", "effect: query")
	_, changed := loadCatalog(t, root)
	if changed.Revision == revision {
		t.Fatal("effect drift did not change revision")
	}
	other := t.TempDir()
	writeCatalogModule(t, other, "zeta", EffectRead, descriptorpb.FieldDescriptorProto_TYPE_STRING)
	writeCatalogModule(t, other, "alpha", "", descriptorpb.FieldDescriptorProto_TYPE_STRING)
	_, schemaChanged := loadCatalog(t, other)
	if schemaChanged.Revision == revision {
		t.Fatal("schema drift did not change revision")
	}
}

func TestCatalogOnlyEnabledPublicAliasesAndStrictMetadata(t *testing.T) {
	root := t.TempDir()
	modtest.WriteModule(t, root, "ordinary", "")
	dir := modtest.WriteModule(t, root, "disabled", "")
	modtest.EditFile(t, filepath.Join(dir, "module.yaml"), "enabled: true", "enabled: false")
	os.Remove(filepath.Join(dir, "module.pb"))
	r, c := loadCatalog(t, root)
	if len(c.Methods) != 1 || c.Methods[0].Name != "ordinary.execute" || c.Methods[0].Callable() {
		t.Fatal(c.Methods)
	}
	if _, ok := c.Messages["ordinary::agentwow.module.v1.WorldPacket"]; ok {
		t.Fatal("packet-only contract leaked into public catalog")
	}
	d, _ := r.Lookup("ordinary")
	if _, ok := d.BeforeLogout(); !ok {
		t.Fatal("fixture lacks lifecycle hook")
	}
	for _, metadata := range []string{"missing: {effect: read}", "execute: {effect: write}", "execute: {description: No effect}", "execute: {effect: read, typo: true}", "execute: {effect: read, description: '" + strings.Repeat("x", 2049) + "'}"} {
		path := filepath.Join(root, "ordinary", "module.yaml")
		original, _ := os.ReadFile(path)
		modtest.WriteFile(t, path, append(original, []byte("script_api:\n  "+metadata+"\n")...))
		if _, err := Load(root); err == nil {
			t.Fatal("accepted invalid script metadata", metadata[:min(len(metadata), 80)])
		}
		modtest.WriteFile(t, path, original)
	}
}

func TestCatalogKeepsIsolatedVersionsOfSameProtobufName(t *testing.T) {
	root := t.TempDir()
	writeCatalogModule(t, root, "one", EffectRead, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
	writeCatalogModule(t, root, "two", EffectRead, descriptorpb.FieldDescriptorProto_TYPE_STRING)
	_, catalog := loadCatalog(t, root)
	one, two := catalog.Messages["one::catalog.Node"], catalog.Messages["two::catalog.Node"]
	if one.ProtobufName != two.ProtobufName || one.Fields[0].Kind != "message" || two.Fields[0].Kind != "string" || one.Fields[0].Type != "one::catalog.Node" {
		t.Fatal(one, two)
	}
}

func TestCatalogForMethodTransitiveProjection(t *testing.T) {
	root := t.TempDir()
	writeCatalogModule(t, root, "one", EffectRead, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
	writeCatalogModule(t, root, "two", EffectMutation, descriptorpb.FieldDescriptorProto_TYPE_STRING)
	// A distinct output references the recursive input. A second public method
	// contributes an unrelated schema in the SAME module to the full catalog.
	file := catalogDescriptor(t, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
	file.MessageType = append(file.MessageType,
		&descriptorpb.DescriptorProto{Name: proto.String("Reply"), Field: []*descriptorpb.FieldDescriptorProto{{Name: proto.String("payload"), Number: proto.Int32(1), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String(".catalog.Node")}}},
		&descriptorpb.DescriptorProto{Name: proto.String("Unrelated")})
	file.Service[0].Method[0].OutputType = proto.String(".catalog.Reply")
	file.Service[0].Method = append(file.Service[0].Method, &descriptorpb.MethodDescriptorProto{Name: proto.String("Other"), InputType: proto.String(".catalog.Unrelated"), OutputType: proto.String(".catalog.Unrelated")})
	data, err := proto.Marshal(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{file}})
	if err != nil {
		t.Fatal(err)
	}
	modtest.WriteFile(t, filepath.Join(root, "one", "module.pb"), data)
	modtest.EditFile(t, filepath.Join(root, "one", "module.yaml"), "rpc: {get: /catalog.Public/Get}", "rpc: {get: /catalog.Public/Get, other: /catalog.Public/Other}")
	r, catalog := loadCatalog(t, root)
	projected, err := catalog.ForMethod("one.get")
	if err != nil {
		t.Fatal(err)
	}
	if projected.Revision != catalog.Revision || len(projected.Methods) != 1 || projected.Methods[0] != catalog.Methods[0] || len(projected.Messages) != 3 || len(projected.Enums) != 1 {
		t.Fatal(projected)
	}
	for _, key := range []string{"one::catalog.Node", "one::catalog.Node.AttributesEntry", "one::catalog.Reply"} {
		if got, ok := projected.Messages[key]; !ok || !reflect.DeepEqual(got, catalog.Messages[key]) {
			t.Fatalf("lost reachable schema %s", key)
		}
	}
	if !reflect.DeepEqual(projected.Enums["one::catalog.Mode"], catalog.Enums["one::catalog.Mode"]) {
		t.Fatal("lost reachable enum")
	}
	for _, key := range []string{"one::catalog.Unrelated", "two::catalog.Node", "two::catalog.Node.AttributesEntry"} {
		if _, ok := projected.Messages[key]; ok {
			t.Fatalf("unreachable schema leaked: %s", key)
		}
	}
	if _, ok := projected.Enums["two::catalog.Mode"]; ok {
		t.Fatal("foreign namespace enum leaked")
	}
	// Projection mutation must not alter the full snapshot or later lookups.
	projected.Methods[0].Effect = EffectMutation
	projected.Messages["one::catalog.Node"].Fields[0].Name = "forged"
	projected.Enums["one::catalog.Mode"].Values[0].Name = "forged"
	if !reflect.DeepEqual(catalog, r.Catalog()) {
		t.Fatal("projection shared mutable contract storage")
	}
	unspecified, err := catalog.ForMethod("one.other")
	if err != nil || unspecified.Methods[0].Callable() || len(unspecified.Messages) != 1 || len(unspecified.Enums) != 0 {
		t.Fatal("projection changed unspecified admission", unspecified, err)
	}
}

func TestCatalogForMethodRejectsUnknownAndMissingSchema(t *testing.T) {
	root := t.TempDir()
	writeCatalogModule(t, root, "one", EffectRead, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
	r, catalog := loadCatalog(t, root)
	for _, name := range []string{"", "one.Get", "missing.get"} {
		if _, err := catalog.ForMethod(name); err == nil {
			t.Fatalf("accepted unknown method %q", name)
		}
	}
	for _, key := range []string{"one::catalog.Node", "one::catalog.Node.AttributesEntry", "one::catalog.Mode"} {
		broken := r.Catalog()
		delete(broken.Messages, key)
		delete(broken.Enums, key)
		if _, err := broken.ForMethod("one.get"); err == nil {
			t.Fatalf("accepted incomplete schema graph without %s", key)
		}
	}
}

func TestRepositoryPublicCatalogContracts(t *testing.T) {
	_, catalog := loadCatalog(t, filepath.Join("..", "..", "..", "modules"))
	if len(catalog.Methods) == 0 || len(catalog.Revision) != 64 {
		t.Fatal("empty repository contracts")
	}
	for _, method := range catalog.Methods {
		if method.Name == "buddy-supplies.buy" && method.Effect != EffectMutation {
			t.Fatal("shopping mutation not declared")
		}
	}
	if data, err := json.Marshal(catalog); err != nil {
		t.Fatal(err)
	} else {
		t.Logf("public catalog: %d methods, %d message schemas, %d bytes", len(catalog.Methods), len(catalog.Messages), len(data))
	}
}

// Verify our complex fixture is genuine protobuf, not hand-written catalog data.
func TestCatalogFixtureDescriptorValid(t *testing.T) {
	if _, err := protodesc.NewFile(catalogDescriptor(t, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE), nil); err != nil {
		t.Fatal(err)
	}
}
