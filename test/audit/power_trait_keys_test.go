package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.alexander4.io/vigo/server/traits"
)

// TestPowerTraitKeysMatchAgent pins the Go constants for the power-loss traits
// against the Rust collector that emits them.
//
// Nothing in either toolchain compares the two sides: the agent builds the JSON
// key as a string literal, `fleet.FlattenJSON` turns it into a dotted path, and
// the server looks it up with another string literal. Both compile, both test
// suites pass, and the wire is dead — a rename on either side is silent, and
// the failure mode is a metric that reads as "this host has never lost power"
// forever. This test is the only thing that would notice.
func TestPowerTraitKeysMatchAgent(t *testing.T) {
	root := repoRoot(t)
	src := filepath.Join(root, "agent", "src", "traits", "host_self.rs")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("reading %s: %v", src, err)
	}
	rust := string(data)

	const prefix = "host_self."
	for _, key := range []string{
		traits.PowerDetectorState,
		traits.UncleanShutdowns30d,
		traits.BootTransitions30d,
		traits.LastUncleanDate,
	} {
		if !strings.HasPrefix(key, prefix) {
			t.Errorf("key %q does not live under the host_self collector; "+
				"the constant and its emitter have diverged", key)
			continue
		}
		field := strings.TrimPrefix(key, prefix)
		// The emitter writes each field as a quoted literal, either in the
		// json! block or via obj.insert("<field>".into(), ...).
		if !strings.Contains(rust, `"`+field+`"`) {
			t.Errorf("Go constant %q expects field %q in %s, but no such literal is emitted there",
				key, field, src)
		}
	}
}

// TestPowerCountsAreGatedOnDetectorState guards the property the whole feature
// rests on: the counts must never be emitted unless the detector is armed.
//
// A zero from a detector that has never observed a boot transition is
// byte-identical on the wire to a genuine "this host has not lost power", and
// the server's health rollup reads a missing key as unknown but a zero as good
// news. If the gate is ever removed, every host with a fresh or broken
// detector starts reporting itself healthy.
func TestPowerCountsAreGatedOnDetectorState(t *testing.T) {
	root := repoRoot(t)
	src := filepath.Join(root, "agent", "src", "traits", "host_self.rs")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("reading %s: %v", src, err)
	}
	rust := string(data)

	gate := "if status.detector != DetectorState::Armed {"
	if !strings.Contains(rust, gate) {
		t.Fatalf("the armed-only gate is gone from %s: expected %q.\n"+
			"Without it, unclean_shutdowns_30d is emitted as 0 by detectors that "+
			"have never observed anything, which reads as a healthy host.", src, gate)
	}
	// The gate must precede both counts, or it gates nothing.
	gateAt := strings.Index(rust, gate)
	for _, field := range []string{"unclean_shutdowns_30d", "boot_transitions_30d"} {
		at := strings.Index(rust, `obj.insert("`+field+`"`)
		if at < 0 {
			t.Errorf("no insert of %q found in %s", field, src)
			continue
		}
		if at < gateAt {
			t.Errorf("%q is inserted before the armed-only gate, so it is emitted "+
				"even when the detector has observed nothing", field)
		}
	}
}
