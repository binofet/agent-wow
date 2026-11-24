// luapolicy is a stateless bridge for editable task policies and descriptor-
// checked public API requests. Game transport and task persistence stay outside.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

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
	sourceBytes := flags.Int("source-bytes", 0, "Lua source budget (0 uses 8 MiB)")
	valueBytes := flags.Int("value-bytes", 0, "JSON value budget (0 keeps the standalone 32 KiB default)")
	executionMS := flags.Int64("execution-ms", 0, "Lua execution budget in milliseconds (0 keeps the standalone 5 ms default)")
	method := flags.String("method", "", "public method to validate or describe")
	if err := flags.Parse(args); err != nil {
		return err
	}
	limit, err := behavior.SourceLimit(*sourceBytes)
	if err != nil {
		return err
	}
	if *valueBytes < 0 {
		return errors.New("JSON value budget must not be negative")
	}
	if *executionMS < 0 || *executionMS > int64((1<<63-1)/time.Millisecond) {
		return errors.New("Lua execution budget must be nonnegative and fit a duration")
	}
	jsonLimit := behavior.MaxJSONBytes
	if *valueBytes > 0 {
		jsonLimit = *valueBytes
	}
	var value any
	switch *mode {
	case "evaluate":
		file, err := os.Open(*source)
		if err != nil {
			return err
		}
		defer file.Close()
		code, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
		if err != nil || len(code) > limit {
			return fmt.Errorf("bounded policy source required (configured limit %d bytes)", limit)
		}
		input := map[string]any{}
		raw, err := io.ReadAll(io.LimitReader(in, int64(jsonLimit)+1))
		if err != nil || len(raw) > jsonLimit {
			return fmt.Errorf("bounded JSON input required (configured limit %d bytes)", jsonLimit)
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&input); err != nil {
			return err
		}
		if decoder.Decode(new(any)) != io.EOF {
			return errors.New("one bounded JSON input required")
		}
		value, err = behavior.EvaluateSourceContext(context.Background(), string(code), input, behavior.SessionOptions{
			SourceBytes: limit, ValueBytes: *valueBytes, ExecutionLimit: time.Duration(*executionMS) * time.Millisecond,
		})
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
			raw, err := io.ReadAll(io.LimitReader(in, int64(jsonLimit)+1))
			if err != nil || len(raw) > jsonLimit {
				return fmt.Errorf("bounded request required (configured limit %d bytes)", jsonLimit)
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
