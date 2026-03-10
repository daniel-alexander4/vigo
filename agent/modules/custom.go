package modules

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// CustomExecutor runs an external script with a JSON protocol.
// The script receives {"params": {...}, "dry_run": bool} on stdin
// and must write {"action": "...", "changed": bool, "error": "..."} to stdout.
type CustomExecutor struct {
	ScriptPath string
}

// customInput is the JSON structure sent to the script on stdin.
type customInput struct {
	Params map[string]string `json:"params"`
	DryRun bool              `json:"dry_run"`
}

// customOutput is the JSON structure expected from the script on stdout.
type customOutput struct {
	Action  string `json:"action"`
	Changed bool   `json:"changed"`
	Error   string `json:"error"`
}

func (e *CustomExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	result := ResourceResult{
		ResourceType: "custom",
		ResourceName: params["name"],
	}

	defer func() {
		result.DurationMs = int32(time.Since(start).Milliseconds())
	}()

	input := customInput{Params: params, DryRun: dryRun}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		result.Error = fmt.Errorf("marshaling input: %w", err)
		return result
	}

	cmd := exec.CommandContext(ctx, e.ScriptPath)
	cmd.Stdin = bytes.NewReader(inputJSON)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		result.Error = fmt.Errorf("running %s: %w: %s", e.ScriptPath, err, stderr.String())
		return result
	}

	var output customOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		result.Error = fmt.Errorf("parsing output from %s: %w\nstdout: %s", e.ScriptPath, err, stdout.String())
		return result
	}

	if output.Error != "" {
		result.Error = fmt.Errorf("%s", output.Error)
		return result
	}

	result.Changed = output.Changed
	result.Action = ActionTaken(output.Action)

	// Validate action is a known value; default to ActionNone if empty.
	if result.Action == "" {
		result.Action = ActionNone
	}

	return result
}
