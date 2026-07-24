// SPDX-License-Identifier: MIT

package config

import "testing"

// baseConfig is a minimal valid config a test can then break in one specific
// way, so a validation failure can only be the thing under test.
func baseConfig(t *testing.T, target Target) *Config {
	t.Helper()
	c := New()
	c.Folders = []Folder{{ID: "f1", Path: "/tmp/src", Label: "src"}}
	c.Targets = []Target{target}
	return c
}

func TestWakeValidation(t *testing.T) {
	share := Target{Type: "share", Name: "nas", URL: "//NAS/backups", Folders: []string{}}
	device := Target{Type: "device", Name: "pc", DeviceID: "ID", Folders: []string{}}
	drive := Target{Type: "drive", Name: "sd", Path: "/media/sd", Folders: []string{}}

	t.Run("valid MAC on a share", func(t *testing.T) {
		tgt := share
		tgt.MAC = "aa:bb:cc:dd:ee:ff"
		if err := baseConfig(t, tgt).Validate(); err != nil {
			t.Errorf("valid MAC rejected: %v", err)
		}
	})

	t.Run("valid MAC on a device", func(t *testing.T) {
		tgt := device
		tgt.MAC = "aabbccddeeff"
		if err := baseConfig(t, tgt).Validate(); err != nil {
			t.Errorf("valid bare-hex MAC rejected: %v", err)
		}
	})

	t.Run("malformed MAC is rejected", func(t *testing.T) {
		tgt := share
		tgt.MAC = "not-a-mac"
		if err := baseConfig(t, tgt).Validate(); err == nil {
			t.Error("malformed MAC accepted")
		}
	})

	// A local drive has no machine of its own to wake; silently accepting a
	// MAC there would look configured while doing nothing.
	t.Run("MAC on a local drive is rejected", func(t *testing.T) {
		tgt := drive
		tgt.MAC = "aa:bb:cc:dd:ee:ff"
		if err := baseConfig(t, tgt).Validate(); err == nil {
			t.Error("MAC on a drive target accepted")
		}
	})

	t.Run("wake_broadcast without a MAC is rejected", func(t *testing.T) {
		tgt := share
		tgt.WakeBroadcast = "192.168.1.255"
		if err := baseConfig(t, tgt).Validate(); err == nil {
			t.Error("wake_broadcast without mac accepted")
		}
	})

	t.Run("non-IPv4 wake_broadcast is rejected", func(t *testing.T) {
		tgt := share
		tgt.MAC = "aa:bb:cc:dd:ee:ff"
		tgt.WakeBroadcast = "not-an-ip"
		if err := baseConfig(t, tgt).Validate(); err == nil {
			t.Error("non-IP wake_broadcast accepted")
		}
	})

	t.Run("no MAC is valid and disables waking", func(t *testing.T) {
		if err := baseConfig(t, share).Validate(); err != nil {
			t.Errorf("target without a MAC rejected: %v", err)
		}
		if share.WakeEnabled() {
			t.Error("WakeEnabled true with no MAC")
		}
	})
}

func TestWakeEnabled(t *testing.T) {
	cases := []struct {
		name string
		tgt  Target
		want bool
	}{
		{"share with MAC", Target{Type: "share", MAC: "aa:bb:cc:dd:ee:ff"}, true},
		{"device with MAC", Target{Type: "device", MAC: "aa:bb:cc:dd:ee:ff"}, true},
		{"drive with MAC", Target{Type: "drive", MAC: "aa:bb:cc:dd:ee:ff"}, false},
		{"share without MAC", Target{Type: "share"}, false},
	}
	for _, c := range cases {
		if got := c.tgt.WakeEnabled(); got != c.want {
			t.Errorf("%s: WakeEnabled() = %v, want %v", c.name, got, c.want)
		}
	}
}
