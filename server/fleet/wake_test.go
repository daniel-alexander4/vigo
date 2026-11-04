// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package fleet

import "testing"

// TestUnregisterWake_SupersededCallKeepsNewerChannel: two Wake calls for one
// envoy. B's RegisterWake closes A's channel; A's deferred UnregisterWake then
// runs. It must not delete B's channel, or a SignalWake meant for B is lost
// until B's wait times out.
func TestUnregisterWake_SupersededCallKeepsNewerChannel(t *testing.T) {
	f := NewFleetIndex()
	const id = "envoy-wake-1"

	a := f.RegisterWake(id)
	b := f.RegisterWake(id)
	select {
	case <-a:
	default:
		t.Fatal("registering B should close superseded channel A")
	}

	f.UnregisterWake(id, a) // A's deferred cleanup, after B registered
	f.SignalWake(id)

	select {
	case <-b:
	default:
		t.Fatal("B's wake channel was not signaled — A's unregister deleted B's entry")
	}

	f.UnregisterWake(id, b)
	f.wakeMu.Lock()
	_, left := f.wakeChans[id]
	f.wakeMu.Unlock()
	if left {
		t.Fatal("owner's UnregisterWake left its channel registered")
	}
}
