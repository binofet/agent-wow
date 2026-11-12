// luapolicy is a stateless bridge for editable task policies and descriptor-
// checked public API requests. Game transport and task persistence stay outside.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/agent-wow/agent-wow/pkg/behavior"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/dynamicpb"
)

func validate(registry *moddisc.Registry, name string, raw json.RawMessage) (json.RawMessage, error) {
	module, alias, ok := strings.Cut(name, ".")
	if !ok {
		return nil, errors.New("public module.method required")
	}
	definition, ok := registry.Lookup(module)
	if !ok {
		return nil, errors.New("module is unavailable")
	}
	method, ok := definition.RPC(alias)
	if !ok {
		return nil, errors.New("public method is unavailable")
	}
	metadata, ok := definition.Manifest().ScriptAPI[alias]
	if !ok || !(moddisc.CatalogMethod{Effect: metadata.Effect}).Callable() {
		return nil, errors.New("method has no explicit script admission metadata")
	}
	if len(raw) == 0 || raw[0] != '{' {
		return nil, errors.New("parameters must be an object")
	}
	message := dynamicpb.NewMessage(method.Descriptor().Input())
	if err := (protojson.UnmarshalOptions{Resolver: definition.Types()}).Unmarshal(raw, message); err != nil {
		return nil, fmt.Errorf("request violates descriptor: %w", err)
	}
	return protojson.Marshal(message)
}

func run(args []string, in io.Reader, out io.Writer) error {
	flags := flag.NewFlagSet("luapolicy", flag.ContinueOnError)
	modules := flags.String("modules", "modules", "validated module metadata directory")
	mode := flags.String("mode", "evaluate", "evaluate, validate or describe")
	source := flags.String("source", "", "Lua decide policy")
	method := flags.String("method", "", "public method to validate or describe")
	if err := flags.Parse(args); err != nil {
		return err
	}
	var value any
	switch *mode {
	case "evaluate":
		file, err := os.Open(*source)
		if err != nil {
			return err
		}
		defer file.Close()
		code, err := io.ReadAll(io.LimitReader(file, behavior.MaxSourceBytes+1))
		if err != nil || len(code) > behavior.MaxSourceBytes {
			return errors.New("bounded policy source required")
		}
		input := map[string]any{}
		decoder := json.NewDecoder(io.LimitReader(in, behavior.MaxJSONBytes+1))
		decoder.UseNumber()
		if err := decoder.Decode(&input); err != nil {
			return err
		}
		if decoder.Decode(new(any)) != io.EOF {
			return errors.New("one bounded JSON input required")
		}
		runtime := behavior.New()
		if _, err := runtime.Reload(string(code), ""); err != nil {
			return err
		}
		value, err = runtime.Evaluate(input)
		if err != nil {
			return err
		}
	case "validate", "describe":
		registry, err := moddisc.Load(*modules)
		if err != nil {
			return err
		}
		if *mode == "describe" {
			catalog := registry.Catalog()
			if *method != "" {
				catalog, err = catalog.ForMethod(*method)
			}
			if err != nil {
				return err
			}
			value = catalog
		} else {
			raw, err := io.ReadAll(io.LimitReader(in, behavior.MaxJSONBytes+1))
			if err != nil || len(raw) > behavior.MaxJSONBytes {
				return errors.New("bounded request required")
			}
			canonical, err := validate(registry, *method, json.RawMessage(strings.TrimSpace(string(raw))))
			if err != nil {
				return err
			}
			value = canonical
		}
	default:
		return errors.New("unknown mode")
	}
	return json.NewEncoder(out).Encode(value)
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
