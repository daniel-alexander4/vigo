// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
)

// HashModuleContent computes a SHA256 hash of a ModuleConfig's content.
// The hash covers resources (name, type, attributes, when, notify, subscribes),
// depends_on, and the module-level when expression — everything that affects
// the module's behavior. The module name is NOT included (it's the cache key).
//
// This produces the same hash as grpc.hashPolicyModule for the same content,
// allowing pre-computation at config load time instead of per-check-in.
func HashModuleContent(mod ModuleConfig) string {
	h := sha256.New()

	// Module-level fields.
	io.WriteString(h, "when:")
	io.WriteString(h, mod.When)
	io.WriteString(h, "\ndeps:")
	for _, dep := range mod.DependsOn {
		io.WriteString(h, dep)
		io.WriteString(h, ",")
	}

	// Resources in order.
	for _, res := range mod.Resources {
		io.WriteString(h, "\nres:")
		io.WriteString(h, res.Name)
		io.WriteString(h, "|")
		io.WriteString(h, res.Type)
		io.WriteString(h, "|when:")
		io.WriteString(h, res.When)

		// Sort attribute keys for deterministic hashing.
		keys := make([]string, 0, len(res.Attributes))
		for k := range res.Attributes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			io.WriteString(h, "|")
			io.WriteString(h, k)
			io.WriteString(h, "=")
			io.WriteString(h, fmt.Sprintf("%v", res.Attributes[k]))
		}

		// Notify and subscribes.
		io.WriteString(h, "|notify:")
		for _, n := range res.Notify {
			io.WriteString(h, n)
			io.WriteString(h, ",")
		}
		io.WriteString(h, "|subscribes:")
		for _, s := range res.Subscribes {
			io.WriteString(h, s)
			io.WriteString(h, ",")
		}
		io.WriteString(h, "|subscribes_secret:")
		for _, s := range res.SubscribesSecret {
			io.WriteString(h, s)
			io.WriteString(h, ",")
		}
	}

	return hex.EncodeToString(h.Sum(nil))
}
