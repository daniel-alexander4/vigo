package traits

import (
	"bufio"
	"bytes"
	"context"
	"strings"

	"github.com/hobnob/hobnob/agent/internal/cmdrun"
)

// LocaleCollector gathers locale traits from the `locale` command.
// Returns the current LANG, LC_ALL, and all LC_* category values.
type LocaleCollector struct{}

func (c *LocaleCollector) Name() string { return "locale" }

func (c *LocaleCollector) Collect(ctx context.Context) (map[string]any, error) {
	runner := &cmdrun.Default{}

	out, err := runner.Run(ctx, "locale")
	if err != nil {
		return map[string]any{}, nil
	}

	result := make(map[string]any)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(parts[0])
		val := strings.Trim(parts[1], "\"")
		result[key] = val
	}

	return result, nil
}
