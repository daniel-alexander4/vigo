// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"reflect"
	"testing"
)

// TestFlattenJSON_TopLevelString pins the baseline: top-level string
// values flatten to unquoted strings. This was already correct before
// the array-string fix and must stay that way.
func TestFlattenJSON_TopLevelString(t *testing.T) {
	out := make(map[string]string)
	FlattenJSON("", map[string]any{
		"os": map[string]any{
			"family": "linux",
			"distro": "ubuntu",
		},
	}, out)

	if got := out["os.family"]; got != "linux" {
		t.Errorf("os.family = %q, want %q", got, "linux")
	}
	if got := out["os.distro"]; got != "ubuntu" {
		t.Errorf("os.distro = %q, want %q", got, "ubuntu")
	}
}

// TestFlattenJSON_ArrayOfStringsStoredVerbatim is the regression test
// for the vigorepo "candidates_count=0" bug: before the fix, array
// string elements were re-JSON-marshaled and stored with literal
// double-quote characters embedded in the value (e.g. `"\"dan\""`
// instead of `"dan"`). That broke every downstream consumer comparing
// flattened trait values to plain strings — most visibly
// vigorepo/builder.go:envoyHasHumanUser which checks `v == "dan"`.
//
// The fix adds a `case string:` branch inside the array-element
// switch so strings bypass the JSON-marshal path.
func TestFlattenJSON_ArrayOfStringsStoredVerbatim(t *testing.T) {
	out := make(map[string]string)
	FlattenJSON("", map[string]any{
		"users": map[string]any{
			"human":      []any{"dan", "ann", "elena"},
			"logged_in":  []any{"dan"},
			"thirdparty": []any{"postfix", "_chrony"},
		},
	}, out)

	// Must be exactly the 3-character string "dan", not the
	// 5-character string `"dan"` with literal JSON quotes.
	for key, want := range map[string]string{
		"users.human.0":      "dan",
		"users.human.1":      "ann",
		"users.human.2":      "elena",
		"users.logged_in.0":  "dan",
		"users.thirdparty.0": "postfix",
		"users.thirdparty.1": "_chrony",
	} {
		got := out[key]
		if got != want {
			t.Errorf("%s = %q (len=%d), want %q (len=%d) — the JSON-quoted bug has regressed",
				key, got, len(got), want, len(want))
		}
	}
}

// TestFlattenJSON_ArrayOfObjectsRecurses verifies that objects
// inside arrays still flatten correctly through nested FlattenJSON
// calls — this path was already handled separately from the string
// case and shouldn't be affected by the fix.
func TestFlattenJSON_ArrayOfObjectsRecurses(t *testing.T) {
	out := make(map[string]string)
	FlattenJSON("", map[string]any{
		"interfaces": []any{
			map[string]any{"name": "eth0", "mac": "aa:bb:cc:dd:ee:ff"},
			map[string]any{"name": "wlan0", "mac": "11:22:33:44:55:66"},
		},
	}, out)

	if got := out["interfaces.0.name"]; got != "eth0" {
		t.Errorf("interfaces.0.name = %q, want eth0", got)
	}
	if got := out["interfaces.1.mac"]; got != "11:22:33:44:55:66" {
		t.Errorf("interfaces.1.mac = %q, want 11:22:33:44:55:66", got)
	}
}

// TestFlattenJSON_ArrayOfNumbersStillJSONMarshaled pins the existing
// behavior for non-string scalar array elements: they continue to be
// rendered via json.Marshal. Numbers become their decimal form,
// booleans become "true"/"false". The fix only touches strings — we
// don't want to accidentally change the wire format of the other
// types.
func TestFlattenJSON_ArrayOfNumbersStillJSONMarshaled(t *testing.T) {
	out := make(map[string]string)
	FlattenJSON("", map[string]any{
		"hardware": map[string]any{
			"cpu_mhz": []any{float64(3200), float64(3400)},
			"hotplug": []any{true, false},
		},
	}, out)

	if got := out["hardware.cpu_mhz.0"]; got != "3200" {
		t.Errorf("hardware.cpu_mhz.0 = %q, want 3200", got)
	}
	if got := out["hardware.hotplug.0"]; got != "true" {
		t.Errorf("hardware.hotplug.0 = %q, want true", got)
	}
	if got := out["hardware.hotplug.1"]; got != "false" {
		t.Errorf("hardware.hotplug.1 = %q, want false", got)
	}
}

// TestFlattenJSON_ProductionVigorepoUseCase replays the exact scenario
// that the diagnostic log uncovered: a fleet envoy reports a
// `users.human` array containing "dan", and the vigorepo candidate
// filter needs `Traits["users.human.0"] == "dan"` to return true.
// Before the fix, the value was `"dan"` (quoted) and the comparison
// failed. After the fix, it's `dan` (bare) and the comparison
// succeeds. This is the end-to-end assertion for the production bug.
func TestFlattenJSON_ProductionVigorepoUseCase(t *testing.T) {
	// Full agent-shape trait payload, matching the real flush-hook
	// input byte-for-byte for the users subtree.
	raw := map[string]any{
		"users": map[string]any{
			"human":     []any{"ann", "dan"},
			"logged_in": []any{"ann", "dan"},
			"os":        []any{"root", "daemon", "sshd"},
		},
	}
	flat := make(map[string]string)
	FlattenJSON("", raw, flat)

	// Simulate vigorepo's envoyHasHumanUser check. The function
	// lives in server/vigorepo/builder.go and does:
	//
	//   for k, v := range traits {
	//       if strings.HasPrefix(k, "users.human.") && v == user {
	//           return true
	//       }
	//   }
	//
	// For this to work, at least one users.human.N entry must
	// exactly equal "dan".
	found := false
	for k, v := range flat {
		if len(k) >= 12 && k[:12] == "users.human." && v == "dan" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("vigorepo's envoyHasHumanUser would not match — flattened users.human.* values: %v",
			filterKeysWithPrefix(flat, "users.human."))
	}
}

func filterKeysWithPrefix(m map[string]string, prefix string) map[string]string {
	out := make(map[string]string)
	for k, v := range m {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out[k] = v
		}
	}
	return out
}

// TestFlattenJSON_NestedMap pins the recursive-map path. This should
// be unaffected by the fix but deserves a baseline test given that
// there were zero unit tests on FlattenJSON before now.
func TestFlattenJSON_NestedMap(t *testing.T) {
	out := make(map[string]string)
	FlattenJSON("", map[string]any{
		"a": map[string]any{
			"b": map[string]any{
				"c": "leaf",
			},
		},
	}, out)

	want := map[string]string{"a.b.c": "leaf"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("nested flatten mismatch: got %v, want %v", out, want)
	}
}
