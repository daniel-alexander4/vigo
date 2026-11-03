// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var configTreeCmd = &cobra.Command{
	Use:   "tree",
	Short: "Show the config directory inheritance tree",
	Long:  "Displays the directory hierarchy with inherited configcrates, roles, and match patterns at each level.",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()

		var tree struct {
			Dir          string     `json:"dir"`
			File         string     `json:"file"`
			Configcrates []string   `json:"configcrates"`
			Roles        []string   `json:"roles"`
			Match        string     `json:"match"`
			Total        []string   `json:"total"`
			Excluded     []string   `json:"excluded"`
			Children     []treeNode `json:"children"`
		}

		if err := client.get("/api/v1/config/tree", &tree); err != nil {
			return err
		}

		// Print root.
		rootNode := treeNode(tree)
		printTreeRoot(&rootNode)

		return nil
	},
}

type treeNode struct {
	Dir          string     `json:"dir"`
	File         string     `json:"file"`
	Configcrates []string   `json:"configcrates"`
	Roles        []string   `json:"roles"`
	Match        string     `json:"match"`
	Total        []string   `json:"total"`
	Excluded     []string   `json:"excluded"`
	Children     []treeNode `json:"children"`
}

func printTreeRoot(node *treeNode) {
	// Root level.
	if node.File != "" {
		configcrates := formatConfigcrateList(node.Configcrates, true)
		roles := ""
		if len(node.Roles) > 0 {
			roles = " roles:" + dim("["+strings.Join(node.Roles, ", ")+"]")
		}
		fmt.Printf("%s → %s%s\n", dim(node.File), configcrates, roles)
	}

	for i, child := range node.Children {
		isLast := i == len(node.Children)-1
		printTreeNode(&child, "", isLast)
	}
}

func printTreeNode(node *treeNode, prefix string, isLast bool) {
	connector := "├── "
	childPrefix := "│   "
	if isLast {
		connector = "└── "
		childPrefix = "    "
	}

	if node.Match != "" {
		// Leaf entry.
		fmt.Printf("%s%s%s → %s → %s\n",
			prefix, connector,
			leafFileName(node),
			yellow(node.Match),
			formatConfigcrateList(node.Configcrates, false))

		// Total line.
		if len(node.Total) > 0 {
			totalPrefix := prefix + childPrefix + "  "
			fmt.Printf("%s%s %s\n", totalPrefix, dim("Total:"), green(strings.Join(node.Total, ", ")))
		}

		// Excluded line.
		if len(node.Excluded) > 0 {
			exclPrefix := prefix + childPrefix + "  "
			excluded := make([]string, len(node.Excluded))
			for i, e := range node.Excluded {
				excluded[i] = strike(e)
			}
			fmt.Printf("%s%s %s\n", exclPrefix, dim("Excluded:"), strings.Join(excluded, ", "))
		}
	} else {
		// Directory with optional common.vgo.
		dirName := blue(lastSegment(node.Dir) + "/")
		if node.File != "" {
			configcrates := formatConfigcrateList(node.Configcrates, true)
			roles := ""
			if len(node.Roles) > 0 {
				roles = " roles:" + dim("["+strings.Join(node.Roles, ", ")+"]")
			}
			fmt.Printf("%s%s%s\n", prefix, connector, dirName)
			fmt.Printf("%s%s%s → %s%s\n", prefix, childPrefix, dim(node.File), configcrates, roles)
		} else {
			fmt.Printf("%s%s%s\n", prefix, connector, dirName)
		}

		for i, child := range node.Children {
			childIsLast := i == len(node.Children)-1
			printTreeNode(&child, prefix+childPrefix, childIsLast)
		}
	}
}

// formatConfigcrateList formats a module list with color.
// inherited=true uses dim (for common.vgo modules), false uses bold white.
func formatConfigcrateList(configcrates []string, inherited bool) string {
	if len(configcrates) == 0 {
		return dim("(none)")
	}
	list := "[" + strings.Join(configcrates, ", ") + "]"
	if inherited {
		return dim(list)
	}
	return bold(list)
}

// leafFileName returns a display name for a leaf entry.
func leafFileName(node *treeNode) string {
	dir := node.Dir
	if dir == "" {
		return node.File
	}
	// Just show the filename part.
	parts := strings.Split(dir, "/")
	return parts[len(parts)-1] + ".vgo"
}

// lastSegment returns the last path segment.
func lastSegment(path string) string {
	parts := strings.Split(path, "/")
	return parts[len(parts)-1]
}

func init() {
	configCmd.AddCommand(configTreeCmd)
}
