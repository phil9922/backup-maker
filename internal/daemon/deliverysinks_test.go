// SPDX-License-Identifier: MIT

package daemon

import (
	"log/slog"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/notify"
)

// bareDaemon is the smallest thing deliverySinks needs: a config, the private
// state the credentials live in, and somewhere to log.
func bareDaemon(cfg *config.Config, state *config.State) *daemon {
	return &daemon{cfg: cfg, state: state, log: slog.New(slog.DiscardHandler)}
}

func sinkMethods(n notify.Notifier) []string {
	m, ok := n.(notify.Multi)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(m))
	for _, s := range m {
		out = append(out, s.Method)
	}
	return out
}

func has(methods []string, want string) bool {
	for _, m := range methods {
		if m == want {
			return true
		}
	}
	return false
}

// THE LINK THAT WOULD FAIL SILENTLY. A method can be switched on in the panel,
// save correctly, report itself as configured, and pass its own Test button —
// and still never carry a real alert, because the Test button builds the sink
// directly while real alerts go through the fan-out assembled here. Nothing
// else in the system would mention the difference.
func TestARealAlertFansOutToNtfy(t *testing.T) {
	cfg := config.New()
	cfg.General.DesktopAlerts = true
	cfg.General.Ntfy.Enabled = true
	d := bareDaemon(cfg, &config.State{NtfyTopicURL: "https://ntfy.sh/some-topic"})

	methods := sinkMethods(d.deliverySinks(cfg))
	if !has(methods, "ntfy") {
		t.Errorf("ntfy is switched on and configured but is not in the fan-out: %v", methods)
	}
	if !has(methods, "desktop") {
		t.Errorf("adding ntfy dropped the desktop popup: %v", methods)
	}
}

// Every method at once. They are checkboxes, not a choice, and somebody with a
// phone usually wants the desktop popup as well.
func TestEveryDeliveryMethodCanRunAtOnce(t *testing.T) {
	cfg := config.New()
	cfg.General.DesktopAlerts = true
	cfg.General.Webhook.Enabled = true
	cfg.General.Ntfy.Enabled = true
	d := bareDaemon(cfg, &config.State{
		WebhookURL:   "https://hooks.example.com/abc",
		NtfyTopicURL: "https://ntfy.sh/some-topic",
	})

	methods := sinkMethods(d.deliverySinks(cfg))
	if len(methods) != 3 {
		t.Fatalf("got %v, want all three methods", methods)
	}
}

// Half-configured is not an excuse to take the others down. Somebody who ticks
// ntfy and has not yet saved a topic must keep the alerting they already had.
func TestNtfySwitchedOnWithNoTopicDoesNotTakeTheOthersWithIt(t *testing.T) {
	cfg := config.New()
	cfg.General.DesktopAlerts = true
	cfg.General.Ntfy.Enabled = true
	d := bareDaemon(cfg, &config.State{})

	methods := sinkMethods(d.deliverySinks(cfg))
	if has(methods, "ntfy") {
		t.Errorf("an unconfigured ntfy was added to the fan-out: %v", methods)
	}
	if !has(methods, "desktop") {
		t.Errorf("a half-configured ntfy silenced the desktop popup: %v", methods)
	}
}

// THE OTHER HALF OF THE SAME FAILURE. The alerter is switched off entirely
// unless some delivery method is on, so a new method missing from that test
// would compute alerts correctly and deliver none of them — on exactly the
// machine that has ntfy and nothing else, which is a headless one.
func TestNtfyAloneIsEnoughToKeepAlertingSwitchedOn(t *testing.T) {
	cfg := config.New()
	cfg.General.DesktopAlerts = false
	cfg.General.Ntfy.Enabled = true

	if !anyDeliveryOn(cfg) {
		t.Error("with only ntfy on, alerting was switched off entirely")
	}

	cfg.General.Ntfy.Enabled = false
	if anyDeliveryOn(cfg) {
		t.Error("with every method off, alerting stayed on")
	}
}
