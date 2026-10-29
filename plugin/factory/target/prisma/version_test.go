// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package prisma

import "testing"

// TestSetVersion: the prisma_version opt takes an exact release, refuses a
// range, and falls back to DefaultVersion when empty.
func TestSetVersion(t *testing.T) {
	t.Cleanup(func() { version = DefaultVersion })
	for _, v := range []string{"7.10.1", "8.0.0-rc.28"} {
		if err := SetVersion(v); err != nil || version != v {
			t.Errorf("SetVersion(%q): %v, version %q", v, err, version)
		}
	}
	for _, v := range []string{"^7.10.1", "7.x", "latest", `7.10.1", "x": "1`} {
		if err := SetVersion(v); err == nil {
			t.Errorf("SetVersion(%q) accepted a non-exact version", v)
		}
	}
	if err := SetVersion(""); err != nil || version != DefaultVersion {
		t.Errorf("SetVersion(\"\"): %v, version %q, want %q", err, version, DefaultVersion)
	}
}
