// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var taskCmd = &cobra.Command{
	Use:   "task",
	Short: "Dispatch ad-hoc commands to targeted envoys and view results in real time",
}

var taskRunCmd = &cobra.Command{
	Use:   "run <command-or-task-name>",
	Short: "Execute an ad-hoc shell command or a stored task definition on matching envoys",
	Long: `Run an ad-hoc command or a named task on targeted envoys.

  Ad-hoc command:  vigocli task run "uptime" --target "*.web.*"
  Named task:      vigocli task run restart-app --target "*.web.*" --param env=staging`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		target, _ := cmd.Flags().GetString("target")
		timeout, _ := cmd.Flags().GetInt("timeout")
		nice, _ := cmd.Flags().GetInt("nice")
		operator, _ := cmd.Flags().GetString("operator")
		batchSize, _ := cmd.Flags().GetString("batch-size")
		maxFailures, _ := cmd.Flags().GetString("max-failures")
		healthCheck, _ := cmd.Flags().GetString("health-check-url")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		paramFlags, _ := cmd.Flags().GetStringArray("param")

		if target == "" && len(paramFlags) == 0 {
			return fmt.Errorf("--target is required")
		}
		if operator == "" {
			if u, err := user.Current(); err == nil {
				operator = u.Username
			} else {
				operator = "vigocli"
			}
		}

		// Parse --param key=value flags.
		params := make(map[string]string)
		for _, p := range paramFlags {
			k, v, ok := strings.Cut(p, "=")
			if !ok {
				return fmt.Errorf("invalid --param format %q (expected key=value)", p)
			}
			params[k] = v
		}

		client := newAPIClient()

		// Build the dispatch request.
		body := map[string]any{
			"target":      target,
			"operator":    operator,
			"timeout":     timeout,
			"nice":        nice,
			"batchSize":   batchSize,
			"maxFailures": maxFailures,
			"healthCheck": healthCheck,
			"dryRun":      dryRun,
		}
		if len(params) > 0 {
			body["taskName"] = args[0]
			body["params"] = params
		} else {
			body["command"] = args[0]
		}

		// Dispatch the task.
		var dispatchResp struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Batches []struct {
				Index     int      `json:"index"`
				Hostnames []string `json:"hostnames"`
			} `json:"batches"`
			Offline []string `json:"offline"`
		}
		err := client.post("/api/v1/task", body, &dispatchResp)
		if err != nil {
			return err
		}

		// Dry-run: display batch plan and exit.
		if dispatchResp.Status == "dry_run" {
			fmt.Println("Dry-run batch plan:")
			fmt.Println()
			for _, b := range dispatchResp.Batches {
				fmt.Printf("  Batch %d: %s\n", b.Index, strings.Join(b.Hostnames, ", "))
			}
			if len(dispatchResp.Offline) > 0 {
				fmt.Printf("\n  Offline (excluded): %s\n", strings.Join(dispatchResp.Offline, ", "))
			}
			fmt.Printf("\n%d batch(es), %d target(s), %d offline\n",
				len(dispatchResp.Batches),
				countBatchTargets(dispatchResp.Batches),
				len(dispatchResp.Offline))
			return nil
		}

		fmt.Printf("Task %s dispatched", dispatchResp.ID)
		if batchSize != "" {
			fmt.Printf(" (rolling, batch_size=%s)", batchSize)
		}
		fmt.Println()

		// Poll until the task finishes.
		deadline := time.Now().Add(time.Duration(timeout+30) * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(1 * time.Second)

			resp, err := fetchTaskRun(client, dispatchResp.ID)
			if err != nil {
				return err
			}

			if resp.Task.Status == "running" || resp.Task.Status == "pending" {
				continue
			}

			// Task is done — display results.
			printTaskResults(resp)
			return nil
		}

		// Timed out waiting — show current state.
		resp, err := fetchTaskRun(client, dispatchResp.ID)
		if err != nil {
			return err
		}
		printTaskResults(resp)
		return nil
	},
}

var taskStatusCmd = &cobra.Command{
	Use:   "status <id>",
	Short: "Show per-target exit codes, stdout, and timing for a dispatched task",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		resp, err := fetchTaskRun(client, args[0])
		if err != nil {
			return err
		}
		printTaskResults(resp)
		return nil
	},
}

var taskListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent ad-hoc task dispatches with completion status",
	RunE: func(cmd *cobra.Command, args []string) error {
		limit, _ := cmd.Flags().GetInt("limit")

		client := newAPIClient()
		var resp struct {
			TaskRuns []struct {
				ID           string `json:"id"`
				Command      string `json:"command"`
				Target       string `json:"target"`
				Operator     string `json:"operator"`
				Status       string `json:"status"`
				TotalTargets int64  `json:"totalTargets"`
				CreatedAt    string `json:"createdAt"`
				FinishedAt   string `json:"finishedAt"`
			} `json:"taskRuns"`
		}

		path := fmt.Sprintf("/api/v1/task?limit=%d", limit)
		if err := client.get(path, &resp); err != nil {
			return err
		}

		if len(resp.TaskRuns) == 0 {
			fmt.Println("No tasks found.")
			return nil
		}

		const cmdMax = 40 // truncation cap
		w := newAlignedTable(os.Stdout)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			header("ID"), header("COMMAND"), header("TARGET"),
			header("STATUS"), header("TARGETS"), header("CREATED"))
		for _, t := range resp.TaskRuns {
			command := t.Command
			if len(command) > cmdMax {
				command = command[:cmdMax-3] + "..."
			}
			count := strconv.FormatInt(t.TotalTargets, 10)
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				dim(t.ID), command, t.Target,
				colorStatus(t.Status), count,
				dim(formatTimestamp(t.CreatedAt)))
		}
		return w.Flush()
	},
}

type taskResponse struct {
	Task struct {
		ID           string `json:"id"`
		Command      string `json:"command"`
		Target       string `json:"target"`
		Operator     string `json:"operator"`
		Status       string `json:"status"`
		TotalTargets int64  `json:"totalTargets"`
		TimeoutSecs  int64  `json:"timeoutSecs"`
		BatchSize    string `json:"batchSize"`
		MaxFailures  string `json:"maxFailures"`
		CreatedAt    string `json:"createdAt"`
		StartedAt    string `json:"startedAt"`
		FinishedAt   string `json:"finishedAt"`
	} `json:"task"`
	Targets []struct {
		ID         string `json:"id"`
		Hostname   string `json:"hostname"`
		Status     string `json:"status"`
		ExitCode   *int64 `json:"exitCode"`
		Stdout     string `json:"stdout"`
		Stderr     string `json:"stderr"`
		DurationMs *int64 `json:"durationMs"`
		ErrorMsg   string `json:"errorMsg"`
		FinishedAt string `json:"finishedAt"`
	} `json:"targets"`
}

func fetchTaskRun(client *apiClient, id string) (*taskResponse, error) {
	var resp taskResponse
	if err := client.get("/api/v1/task/"+id, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func printTaskResults(resp *taskResponse) {
	t := resp.Task
	fmt.Printf("%s %s\n", bold("Task:"), dim(t.ID))
	fmt.Printf("%s %s\n", bold("Command:"), t.Command)
	fmt.Printf("%s %s\n", bold("Target:"), t.Target)
	fmt.Printf("%s %s\n", bold("Operator:"), t.Operator)
	fmt.Printf("%s %s\n", bold("Status:"), colorStatus(t.Status))
	fmt.Printf("%s %d\n", bold("Targets:"), t.TotalTargets)
	if t.BatchSize != "" {
		fmt.Printf("%s %s\n", bold("Batch:"), t.BatchSize)
	}
	if t.MaxFailures != "" {
		fmt.Printf("%s %s\n", bold("MaxFail:"), t.MaxFailures)
	}
	fmt.Printf("%s %s\n", bold("Created:"), dim(formatTimestamp(t.CreatedAt)))
	if t.StartedAt != "" {
		fmt.Printf("%s %s\n", bold("Started:"), dim(formatTimestamp(t.StartedAt)))
	}
	if t.FinishedAt != "" {
		fmt.Printf("%s %s\n", bold("Finished:"), dim(formatTimestamp(t.FinishedAt)))
	}

	if len(resp.Targets) == 0 {
		return
	}

	fmt.Println()
	for _, tgt := range resp.Targets {
		fmt.Printf("--- %s [%s]", bold(tgt.Hostname), colorStatus(tgt.Status))
		if tgt.ExitCode != nil {
			code := fmt.Sprintf("exit=%d", *tgt.ExitCode)
			if *tgt.ExitCode == 0 {
				fmt.Printf(" %s", green(code))
			} else {
				fmt.Printf(" %s", red(code))
			}
		}
		if tgt.DurationMs != nil {
			fmt.Printf(" %s", dim(fmt.Sprintf("%dms", *tgt.DurationMs)))
		}
		fmt.Println()

		if tgt.Stdout != "" {
			stdout := tgt.Stdout
			if len(stdout) > 1048576 {
				stdout = stdout[:1048576] + "\n[output truncated]"
			}
			fmt.Print(stdout)
			if !strings.HasSuffix(stdout, "\n") {
				fmt.Println()
			}
		}
		if tgt.Stderr != "" {
			fmt.Fprintf(os.Stderr, "%s", red(tgt.Stderr))
			if !strings.HasSuffix(tgt.Stderr, "\n") {
				fmt.Fprintln(os.Stderr)
			}
		}
		if tgt.ErrorMsg != "" {
			fmt.Printf("%s %s\n", red("error:"), tgt.ErrorMsg)
		}
	}
}

func countBatchTargets(batches []struct {
	Index     int      `json:"index"`
	Hostnames []string `json:"hostnames"`
}) int {
	total := 0
	for _, b := range batches {
		total += len(b.Hostnames)
	}
	return total
}

var taskCancelCmd = &cobra.Command{
	Use:   "cancel [id]",
	Short: "Cancel a stuck task or all running tasks",
	Long: `Cancel a specific task by ID, or cancel all tasks stuck in running/pending state.

  vigocli task cancel <id>       Cancel a specific task
  vigocli task cancel --running  Cancel all running/pending tasks`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cancelRunning, _ := cmd.Flags().GetBool("running")
		client := newAPIClient()

		if cancelRunning {
			var resp struct {
				Status string `json:"status"`
				Count  int    `json:"count"`
			}
			if err := client.post("/api/v1/task/cancel-running", nil, &resp); err != nil {
				return err
			}
			fmt.Printf("%s %d task(s) cancelled\n", green("OK"), resp.Count)
			return nil
		}

		if len(args) < 1 {
			return fmt.Errorf("specify a task ID, or use --running to cancel all")
		}

		var resp struct {
			Status string `json:"status"`
			ID     string `json:"id"`
		}
		if err := client.post("/api/v1/task/"+args[0]+"/cancel", nil, &resp); err != nil {
			return err
		}
		fmt.Printf("%s task %s cancelled\n", green("OK"), resp.ID)
		return nil
	},
}

func init() {
	taskRunCmd.Flags().String("target", "", "Target spec: hostname, glob (*.web.*), UUID, or trait filter (os.distro=ubuntu)")
	taskRunCmd.Flags().Int("timeout", 60, "Command timeout in seconds")
	taskRunCmd.Flags().Int("nice", 0, "CPU/IO priority 0-19 (0=normal, 19=lowest)")
	taskRunCmd.Flags().String("operator", "", "Operator name for audit (default: current user)")
	taskRunCmd.Flags().String("batch-size", "", "Batch size: count (5) or percentage (25%)")
	taskRunCmd.Flags().String("max-failures", "", "Max failures before abort: count (2) or percentage (10%)")
	taskRunCmd.Flags().String("health-check-url", "", "URL to probe between batches (abort on non-2xx)")
	taskRunCmd.Flags().Bool("dry-run", false, "Display batch plan without executing")
	taskRunCmd.Flags().StringArray("param", nil, "Task parameter (key=value), repeatable")
	taskListCmd.Flags().Int("limit", 20, "Maximum number of tasks to display")
	taskCancelCmd.Flags().Bool("running", false, "Cancel all tasks in running/pending state")

	taskCmd.AddCommand(taskRunCmd, taskStatusCmd, taskListCmd, taskCancelCmd)
}
