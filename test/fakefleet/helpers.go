// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

// newFlagSet returns a per-command flag set that prints the command name
// on error rather than the bare binary path.
func newFlagSet(cmd string) *flag.FlagSet {
	fs := flag.NewFlagSet("vigo "+cmd, flag.ExitOnError)
	return fs
}

// die prints to stderr and exits non-zero.
func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "vigo: "+format+"\n", args...)
	os.Exit(1)
}

// trimToken strips whitespace/newlines from a token file's contents.
func trimToken(s string) string { return strings.TrimSpace(s) }

// runPool runs fn over [0,n) with at most `concurrency` in flight, and
// reports progress to stderr every ~5%. It returns the count of
// invocations that returned a non-nil error; the per-item error handling
// (log, collect) is the caller's job inside fn.
func runPool(n, concurrency int, label string, fn func(i int) error) int {
	if concurrency < 1 {
		concurrency = 1
	}
	var (
		wg     sync.WaitGroup
		sem    = make(chan struct{}, concurrency)
		fails  atomic.Int64
		done   atomic.Int64
		step   = n / 20 // ~5% increments
		nextAt atomic.Int64
	)
	if step < 1 {
		step = 1
	}
	nextAt.Store(int64(step))
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(i); err != nil {
				fails.Add(1)
			}
			d := done.Add(1)
			if d >= nextAt.Load() {
				nextAt.Add(int64(step))
				fmt.Fprintf(os.Stderr, "\r%s: %d/%d (%d failed)", label, d, n, fails.Load())
			}
		}(i)
	}
	wg.Wait()
	fmt.Fprintf(os.Stderr, "\r%s: %d/%d (%d failed)\n", label, n, n, fails.Load())
	return int(fails.Load())
}
