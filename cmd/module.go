package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/agent-wow/agent-wow/internal/config"
	"github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"github.com/spf13/cobra"
)

func newModuleCommand() *cobra.Command {
	var asJSON bool
	command := &cobra.Command{Use: "module", Short: "Inspect installed gameplay modules", Args: cobra.NoArgs}
	list := &cobra.Command{Use: "list", Short: "List modules and their configured interfaces without starting Docker", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cmd.SilenceUsage = true
		items, err := moddisc.Discover(config.Get().ModuleDir)
		if err != nil {
			return err
		}
		if asJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(struct {
				Modules []moddisc.Manifest `json:"modules"`
			}{items})
		}
		if len(items) == 0 {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "No modules installed.")
			return err
		}
		var out strings.Builder
		for i, m := range items {
			if i > 0 {
				fmt.Fprintln(&out)
			}
			state := "disabled"
			if m.Enabled {
				state = "enabled"
			}
			fmt.Fprintf(&out, "%s (%s)\n", m.Name, state)
			moduleListField(&out, "Description", m.Description)
			moduleListField(&out, "Requires", m.Requires...)
			methods := make([]string, 0, len(m.RPC))
			for alias, target := range m.RPC {
				methods = append(methods, m.Name+"."+alias+"\n  -> "+target)
			}
			sort.Strings(methods)
			moduleListField(&out, "RPC methods", methods...)
			handlers := make(map[string][]string)
			for op, target := range m.Packets {
				handlers[target] = append(handlers[target], op)
			}
			packets := make([]string, 0, len(handlers))
			for target, ops := range handlers {
				sort.Strings(ops)
				packets = append(packets, target+"\n  "+strings.Join(ops, "\n  "))
			}
			sort.Strings(packets)
			moduleListField(&out, "Packet handlers (subscribed opcodes)", packets...)
			moduleListField(&out, "Before logout", m.Lifecycle.BeforeLogout)
			moduleListField(&out, "Manifest", m.Path)
			compose, descriptor := "", ""
			if m.Compose.File != "" {
				compose = filepath.Join(m.Directory, m.Compose.File)
			}
			if m.GRPC.DescriptorSet != "" {
				descriptor = filepath.Join(m.Directory, m.GRPC.DescriptorSet)
			}
			moduleListField(&out, "Compose", compose)
			moduleListField(&out, "Service", m.Compose.Service)
			moduleListField(&out, "Descriptor", descriptor)
		}
		_, err = fmt.Fprint(cmd.OutOrStdout(), out.String())
		return err
	}}
	list.Flags().BoolVar(&asJSON, "json", false, "Output module manifests as JSON")
	describe := &cobra.Command{Use: "describe [method]", Short: "Describe validated public module contracts offline as JSON", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		registry, err := moddisc.Load(config.Get().ModuleDir)
		if err != nil {
			return err
		}
		catalog := registry.Catalog()
		if len(args) == 1 {
			catalog, err = catalog.ForMethod(args[0])
			if err != nil {
				return err
			}
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(catalog)
	}}
	command.AddCommand(list, describe)
	return command
}

func moduleListField(out *strings.Builder, label string, values ...string) {
	if len(values) == 0 || len(values) == 1 && values[0] == "" {
		fmt.Fprintf(out, "  %s: none\n", label)
		return
	}
	fmt.Fprintf(out, "  %s:\n", label)
	for _, value := range values {
		fmt.Fprintf(out, "    %s\n", strings.ReplaceAll(value, "\n", "\n    "))
	}
}

func init() { rootCmd.AddCommand(newModuleCommand()) }
