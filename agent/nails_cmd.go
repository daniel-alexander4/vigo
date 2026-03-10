package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"

	"github.com/hobnob/hobnob/agent/nails"
	"github.com/spf13/cobra"
)

var nailsCmd = &cobra.Command{
	Use:   "nails [collector...] or nails <dot.path>",
	Short: "Print collected system nails as JSON",
	Long: `Collect and print system nails to stdout as pretty-printed JSON.

With no arguments, prints all nails from every collector.
With collector names (e.g. "os", "hardware"), prints only those categories.
With a dot-path (e.g. "os.distro"), prints just that value.

Examples:
  hob nails                  # all nails
  hob nails os               # only OS nails
  hob nails os hardware      # OS and hardware nails
  hob nails os.distro        # just the distro value`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Suppress slog output for clean JSON-only stdout.
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))

		ctx := context.Background()
		allCollectors := nails.DefaultCollectors()

		// No args: collect everything and print.
		if len(args) == 0 {
			collected, err := nails.CollectAll(ctx, allCollectors)
			if err != nil {
				return err
			}
			return printJSON(collected)
		}

		// If first arg contains a dot, treat as dot-path query.
		if strings.Contains(args[0], ".") {
			dotPath := args[0]
			collected, err := nails.CollectAll(ctx, allCollectors)
			if err != nil {
				return err
			}
			val, ok := traverseDotPath(collected, dotPath)
			if !ok {
				fmt.Fprintf(os.Stderr, "nail path '%s' not found\n", dotPath)
				os.Exit(1)
			}
			fmt.Println(formatValue(val))
			return nil
		}

		// Otherwise, treat args as collector name filters.
		filtered, err := filterCollectors(allCollectors, args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s\n", err)
			os.Exit(1)
		}

		collected, err := nails.CollectAll(ctx, filtered)
		if err != nil {
			return err
		}
		return printJSON(collected)
	},
}

// traverseDotPath walks a nested map using a dot-separated path and returns
// the value at that path. Returns (nil, false) if the path is empty or any
// segment is not found.
func traverseDotPath(data map[string]any, path string) (any, bool) {
	if path == "" {
		return nil, false
	}

	parts := strings.SplitN(path, ".", 2)
	val, ok := data[parts[0]]
	if !ok {
		return nil, false
	}

	// If there are no more segments, return the value.
	if len(parts) == 1 {
		return val, true
	}

	// If there are more segments, the current value must be a map.
	sub, isMap := val.(map[string]any)
	if !isMap {
		return nil, false
	}
	return traverseDotPath(sub, parts[1])
}

// filterCollectors returns only the collectors whose Name() matches one of the
// provided names. Returns an error if any name has no matching collector.
func filterCollectors(all []nails.Collector, names []string) ([]nails.Collector, error) {
	available := make(map[string]nails.Collector, len(all))
	for _, c := range all {
		available[c.Name()] = c
	}

	var result []nails.Collector
	var unknown []string
	for _, name := range names {
		c, ok := available[name]
		if !ok {
			unknown = append(unknown, name)
		} else {
			result = append(result, c)
		}
	}

	if len(unknown) > 0 {
		avail := make([]string, 0, len(available))
		for k := range available {
			avail = append(avail, k)
		}
		return nil, fmt.Errorf("unknown collector(s): %s (available: %s)",
			strings.Join(unknown, ", "), strings.Join(avail, ", "))
	}

	return result, nil
}

// formatValue returns a string representation of a value. Scalars (string,
// numbers, bools) are returned via fmt.Sprint. Maps and slices are returned
// as pretty-printed JSON.
func formatValue(v any) string {
	rv := reflect.ValueOf(v)
	kind := rv.Kind()

	// Maps and slices get pretty JSON.
	if kind == reflect.Map || kind == reflect.Slice || kind == reflect.Array {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(b)
	}

	// Everything else: scalar representation.
	return fmt.Sprint(v)
}

// printJSON marshals v as indented JSON and prints it to stdout.
func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}
	fmt.Println(string(b))
	return nil
}

func init() {
	rootCmd.AddCommand(nailsCmd)
}
