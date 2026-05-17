// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"os/user"
	"time"

	"github.com/spf13/cobra"
)

var workflowCmd = &cobra.Command{
	Use:   "workflow",
	Short: "Execute and monitor multi-step orchestration workflows defined in workflows/*.yaml",
}

var workflowRunCmd = &cobra.Command{
	Use:   "run <name>",
	Short: "Start a named workflow and optionally wait for all steps to complete",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		operator, _ := cmd.Flags().GetString("operator")
		wait, _ := cmd.Flags().GetBool("wait")

		if operator == "" {
			if u, err := user.Current(); err == nil {
				operator = u.Username
			} else {
				operator = "vigocli"
			}
		}

		client := newAPIClient()

		var resp struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		err := client.post("/api/v1/workflows", map[string]any{
			"name":     args[0],
			"operator": operator,
		}, &resp)
		if err != nil {
			return err
		}

		fmt.Printf("Workflow %s started (id: %s)\n", args[0], resp.ID)

		if !wait {
			return nil
		}

		// Poll until complete.
		deadline := time.Now().Add(30 * time.Minute)
		for time.Now().Before(deadline) {
			time.Sleep(2 * time.Second)

			wf, err := fetchWorkflow(client, resp.ID)
			if err != nil {
				return err
			}

			switch wf.Workflow.Status {
			case "running", "pending":
				fmt.Printf("\r  Step %d/%d running...", wf.Workflow.CurrentStep+1, wf.Workflow.TotalSteps)
				continue
			default:
				fmt.Println()
				printWorkflowResults(wf)
				return nil
			}
		}

		fmt.Println("\nTimed out waiting for workflow completion.")
		return nil
	},
}

var workflowStatusCmd = &cobra.Command{
	Use:   "status <id>",
	Short: "Show step-by-step progress and outcomes for a workflow execution",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		wf, err := fetchWorkflow(client, args[0])
		if err != nil {
			return err
		}
		printWorkflowResults(wf)
		return nil
	},
}

var workflowListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent workflow executions with step progress and final status",
	RunE: func(cmd *cobra.Command, args []string) error {
		limit, _ := cmd.Flags().GetInt("limit")
		client := newAPIClient()

		var resp struct {
			Workflows []struct {
				ID           string `json:"id"`
				WorkflowName string `json:"workflowName"`
				Status       string `json:"status"`
				CurrentStep  int64  `json:"currentStep"`
				TotalSteps   int64  `json:"totalSteps"`
				Operator     string `json:"operator"`
				CreatedAt    string `json:"createdAt"`
				FinishedAt   string `json:"finishedAt"`
			} `json:"workflows"`
		}
		path := fmt.Sprintf("/api/v1/workflows?limit=%d", limit)
		if err := client.get(path, &resp); err != nil {
			return err
		}

		if len(resp.Workflows) == 0 {
			fmt.Println("No workflows found.")
			return nil
		}

		w := newAlignedTable(os.Stdout)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", header("ID"), header("NAME"), header("STATUS"), header("STEPS"), header("CREATED"))
		for _, wf := range resp.Workflows {
			fmt.Fprintf(w, "%s\t%s\t%s\t%d/%d\t%s\n",
				dim(wf.ID), wf.WorkflowName, colorStatus(wf.Status),
				wf.CurrentStep, wf.TotalSteps,
				dim(formatTimestamp(wf.CreatedAt)))
		}
		return w.Flush()
	},
}

var workflowAbortCmd = &cobra.Command{
	Use:   "abort <id>",
	Short: "Cancel a running workflow and stop further step execution",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !confirmAction("Abort this running workflow?") {
			fmt.Println("Cancelled.")
			return nil
		}

		client := newAPIClient()
		var resp struct {
			Status string `json:"status"`
		}
		err := client.post("/api/v1/workflows/"+args[0]+"/abort", nil, &resp)
		if err != nil {
			return err
		}
		fmt.Printf("Workflow %s: %s\n", args[0], resp.Status)
		return nil
	},
}

type workflowResponse struct {
	Workflow struct {
		ID            string `json:"id"`
		WorkflowName  string `json:"workflowName"`
		Status        string `json:"status"`
		CurrentStep   int64  `json:"currentStep"`
		TotalSteps    int64  `json:"totalSteps"`
		Operator      string `json:"operator"`
		CreatedAt     string `json:"createdAt"`
		StartedAt     string `json:"startedAt"`
		FinishedAt    string `json:"finishedAt"`
		FailureReason string `json:"failureReason"`
	} `json:"workflow"`
	Steps []struct {
		ID         string `json:"id"`
		StepIndex  int64  `json:"stepIndex"`
		StepName   string `json:"stepName"`
		PushRunID  string `json:"pushRunId"`
		Status     string `json:"status"`
		StartedAt  string `json:"startedAt"`
		FinishedAt string `json:"finishedAt"`
	} `json:"steps"`
}

func fetchWorkflow(client *apiClient, id string) (*workflowResponse, error) {
	var resp workflowResponse
	if err := client.get("/api/v1/workflows/"+id, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func printWorkflowResults(resp *workflowResponse) {
	wf := resp.Workflow
	fmt.Printf("Workflow: %s\n", wf.ID)
	fmt.Printf("Name:     %s\n", wf.WorkflowName)
	fmt.Printf("Status:   %s\n", wf.Status)
	fmt.Printf("Steps:    %d/%d\n", wf.CurrentStep, wf.TotalSteps)
	fmt.Printf("Operator: %s\n", wf.Operator)
	fmt.Printf("Created:  %s\n", formatTimestamp(wf.CreatedAt))
	if wf.StartedAt != "" {
		fmt.Printf("Started:  %s\n", formatTimestamp(wf.StartedAt))
	}
	if wf.FinishedAt != "" {
		fmt.Printf("Finished: %s\n", formatTimestamp(wf.FinishedAt))
	}
	if wf.FailureReason != "" {
		fmt.Printf("Reason:   %s\n", wf.FailureReason)
	}

	if len(resp.Steps) == 0 {
		return
	}

	fmt.Println()
	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", header("STEP"), header("NAME"), header("STATUS"), header("TASK RUN"))
	for _, s := range resp.Steps {
		taskID := dim("-")
		if s.PushRunID != "" {
			taskID = s.PushRunID
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", s.StepIndex, s.StepName, colorStatus(s.Status), taskID)
	}
	w.Flush()
}

func init() {
	workflowRunCmd.Flags().String("operator", "", "Operator name for audit (default: current user)")
	workflowRunCmd.Flags().Bool("wait", false, "Wait for workflow to complete")
	workflowListCmd.Flags().Int("limit", 20, "Maximum number of workflows to display")

	workflowCmd.AddCommand(workflowRunCmd, workflowStatusCmd, workflowListCmd, workflowAbortCmd)
}
