// Package moddisc discovers module manifests and validates contracts and dependencies offline.
package moddisc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/agent-wow/agent-wow/pkg/opcode"
	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Manifest describes module.yaml. Asset paths are relative to Directory.
type Manifest struct {
	Name        string   `yaml:"-" json:"name"`
	Directory   string   `yaml:"-" json:"directory"`
	Path        string   `yaml:"-" json:"manifest"`
	APIVersion  int      `yaml:"api_version" json:"api_version"`
	Enabled     bool     `yaml:"enabled" json:"enabled"`
	Description string   `yaml:"description" json:"description"`
	Requires    []string `yaml:"requires" json:"requires"`
	Compose     struct {
		File    string `yaml:"file" json:"file"`
		Service string `yaml:"service" json:"service"`
	} `yaml:"compose" json:"compose"`
	GRPC struct {
		DescriptorSet string `yaml:"descriptor_set" json:"descriptor_set"`
	} `yaml:"grpc" json:"grpc"`
	RPC       map[string]string       `yaml:"rpc" json:"rpc"`
	ScriptAPI map[string]ScriptMethod `yaml:"script_api" json:"script_api,omitempty"`
	Packets   map[string]string       `yaml:"packets" json:"packets"`
	Lifecycle struct {
		BeforeLogout string `yaml:"before_logout" json:"before_logout,omitempty"`
	} `yaml:"lifecycle" json:"lifecycle"`
}

var moduleName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
var aliasName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// Discover reads installed manifests for offline listing. A missing root returns an empty list.
func Discover(root string) ([]Manifest, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []Manifest{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]Manifest, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !moduleName.MatchString(name) || name == "session" {
			return nil, fmt.Errorf("invalid or reserved module name %q", name)
		}
		dir, err := filepath.Abs(filepath.Join(root, name))
		if err != nil {
			return nil, err
		}
		path := filepath.Join(dir, "module.yaml")
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("module %s: %w", name, err)
		}
		dec := yaml.NewDecoder(io.LimitReader(f, 1<<20))
		dec.KnownFields(true)
		var m Manifest
		err = dec.Decode(&m)
		if err == nil {
			var extra any
			if e := dec.Decode(&extra); e != io.EOF {
				err = errors.New("expected exactly one YAML document")
			}
		}
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("module %s: %w", name, err)
		}
		if m.APIVersion != 1 {
			return nil, fmt.Errorf("module %s: unsupported api_version %d", name, m.APIVersion)
		}
		m.Name, m.Directory, m.Path = name, dir, path
		if m.Requires == nil {
			m.Requires = []string{}
		}
		if m.RPC == nil {
			m.RPC = map[string]string{}
		}
		if m.Packets == nil {
			m.Packets = map[string]string{}
		}
		for alias := range m.RPC {
			if !aliasName.MatchString(alias) {
				return nil, fmt.Errorf("module %s: invalid method alias %q", name, alias)
			}
		}
		for alias, script := range m.ScriptAPI {
			if _, ok := m.RPC[alias]; !ok {
				return nil, fmt.Errorf("module %s: script_api alias %q is not public", name, alias)
			}
			if !validEffect(script.Effect) {
				return nil, fmt.Errorf("module %s: invalid script_api effect %q", name, script.Effect)
			}
			if len(script.Description) > 2048 {
				return nil, fmt.Errorf("module %s: script_api description exceeds 2048 bytes", name)
			}
		}
		result = append(result, m)
	}
	return result, nil
}

// Method describes a validated unary gRPC method.
type Method struct {
	path       string
	descriptor protoreflect.MethodDescriptor
}

// Definition holds an enabled module's validated contracts and isolated protobuf resolver.
type Definition struct {
	manifest     Manifest
	rpc          map[string]Method
	packets      map[uint16]Method
	beforeLogout *Method
	types        *dynamicpb.Types
}

// Registry is an immutable graph of validated, enabled modules. Its zero value is empty.
type Registry struct {
	definitions map[string]*Definition
	order       []string
}

// Load validates enabled modules' assets, protobuf contracts, and dependencies without starting services.
func Load(root string) (*Registry, error) {
	manifests, err := Discover(root)
	if err != nil {
		return nil, err
	}
	r := &Registry{definitions: map[string]*Definition{}}
	for _, m := range manifests {
		if !m.Enabled {
			continue
		}
		d, err := loadDefinition(m)
		if err != nil {
			return nil, fmt.Errorf("module %s: %w", m.Name, err)
		}
		r.definitions[m.Name] = d
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if visiting[name] {
			return fmt.Errorf("module dependency cycle at %s", name)
		}
		if visited[name] {
			return nil
		}
		d, ok := r.definitions[name]
		if !ok {
			return fmt.Errorf("required module %q is missing or disabled", name)
		}
		visiting[name] = true
		seen := map[string]bool{}
		for _, dep := range d.manifest.Requires {
			if seen[dep] {
				return fmt.Errorf("module %s: duplicate dependency %s", name, dep)
			}
			seen[dep] = true
			if err := visit(dep); err != nil {
				return fmt.Errorf("module %s: %w", name, err)
			}
		}
		visiting[name] = false
		visited[name] = true
		r.order = append(r.order, name)
		return nil
	}
	names := make([]string, 0, len(r.definitions))
	for n := range r.definitions {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := visit(n); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func asset(m Manifest, path string) (string, error) {
	if strings.TrimSpace(path) == "" || filepath.IsAbs(path) {
		return "", errors.New("asset path must be relative to module directory")
	}
	full, err := filepath.EvalSymlinks(filepath.Join(m.Directory, path))
	if err != nil {
		return "", err
	}
	base, err := filepath.EvalSymlinks(m.Directory)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("asset path escapes module directory")
	}
	st, err := os.Stat(full)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", errors.New("asset must be a regular file")
	}
	return full, nil
}

func loadDefinition(m Manifest) (*Definition, error) {
	compose, err := asset(m, m.Compose.File)
	if err != nil {
		return nil, fmt.Errorf("compose file: %w", err)
	}
	if !moduleName.MatchString(m.Compose.Service) {
		return nil, errors.New("invalid compose service")
	}
	// Compose performs interpolation and full validation; check the selected service offline first.
	body, err := os.ReadFile(compose)
	if err != nil {
		return nil, err
	}
	var model struct {
		Services map[string]any `yaml:"services"`
	}
	if err = yaml.Unmarshal(body, &model); err != nil {
		return nil, err
	}
	if _, ok := model.Services[m.Compose.Service]; !ok {
		return nil, fmt.Errorf("compose service %q does not exist", m.Compose.Service)
	}
	desc, err := asset(m, m.GRPC.DescriptorSet)
	if err != nil {
		return nil, fmt.Errorf("descriptor set: %w", err)
	}
	body, err = os.ReadFile(desc)
	if err != nil {
		return nil, err
	}
	var set descriptorpb.FileDescriptorSet
	if err = proto.Unmarshal(body, &set); err != nil {
		return nil, err
	}
	files, err := protodesc.NewFiles(&set)
	if err != nil {
		return nil, err
	}
	d := &Definition{manifest: m, rpc: map[string]Method{}, packets: map[uint16]Method{}, types: dynamicpb.NewTypes(files)}
	resolve := func(path string) (Method, error) {
		parts := strings.Split(path, "/")
		if len(parts) != 3 || parts[0] != "" || parts[1] == "" || parts[2] == "" {
			return Method{}, fmt.Errorf("invalid gRPC method path %q", path)
		}
		v, err := files.FindDescriptorByName(protoreflect.FullName(parts[1] + "." + parts[2]))
		if err != nil {
			return Method{}, err
		}
		md, ok := v.(protoreflect.MethodDescriptor)
		if !ok {
			return Method{}, fmt.Errorf("%s is not a method", path)
		}
		if md.IsStreamingClient() || md.IsStreamingServer() {
			return Method{}, fmt.Errorf("%s must be unary", path)
		}
		return Method{path, md}, nil
	}
	for alias, path := range m.RPC {
		md, err := resolve(path)
		if err != nil {
			return nil, err
		}
		d.rpc[alias] = md
	}
	for key, path := range m.Packets {
		op, err := parseOpcode(key)
		if err != nil {
			return nil, err
		}
		if _, ok := d.packets[op]; ok {
			return nil, fmt.Errorf("duplicate opcode 0x%x", op)
		}
		md, err := resolve(path)
		if err != nil {
			return nil, err
		}
		if md.descriptor.Input().FullName() != "agentwow.module.v1.WorldPacket" || md.descriptor.Output().FullName() != "google.protobuf.Empty" {
			return nil, fmt.Errorf("%s must accept WorldPacket and return Empty", path)
		}
		// Reject a descriptor which shadows the shared packet's wire contract.
		fields := md.descriptor.Input().Fields()
		if fields.Len() != 2 || fields.ByNumber(1) == nil || fields.ByNumber(1).Kind() != protoreflect.Uint32Kind || fields.ByNumber(2) == nil || fields.ByNumber(2).Kind() != protoreflect.BytesKind || fields.ByNumber(1).IsList() || fields.ByNumber(2).IsList() {
			return nil, errors.New("incompatible WorldPacket descriptor")
		}
		d.packets[op] = md
	}
	if m.Lifecycle.BeforeLogout != "" {
		md, err := resolve(m.Lifecycle.BeforeLogout)
		if err != nil {
			return nil, err
		}
		if md.descriptor.Input().FullName() != "google.protobuf.Empty" || md.descriptor.Output().FullName() != "google.protobuf.Empty" {
			return nil, errors.New("before_logout must accept and return Empty")
		}
		d.beforeLogout = &md
	}
	for _, public := range d.rpc {
		if d.beforeLogout != nil && public.path == d.beforeLogout.path {
			return nil, errors.New("lifecycle hooks cannot be exposed as RPC methods")
		}
		for _, packet := range d.packets {
			if public.path == packet.path {
				return nil, errors.New("packet handlers cannot be exposed as RPC methods")
			}
		}
	}
	return d, nil
}

func parseOpcode(key string) (uint16, error) {
	var value uint64
	var err error
	if strings.HasPrefix(key, "0x") {
		value, err = strconv.ParseUint(key[2:], 16, 16)
	} else {
		value, err = strconv.ParseUint(key, 10, 16)
	}
	if err == nil {
		name := opcode.WorldName(uint32(value))
		if strings.HasPrefix(name, "CMSG_") {
			return 0, fmt.Errorf("%s is not a server opcode", key)
		}
		return uint16(value), nil
	}
	if strings.HasPrefix(key, "SMSG_") || strings.HasPrefix(key, "MSG_") {
		for i := uint32(0); i <= 65535; i++ {
			if opcode.WorldName(i) == key {
				return uint16(i), nil
			}
		}
	}
	return 0, fmt.Errorf("invalid server opcode %q", key)
}
