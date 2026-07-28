// SPDX-License-Identifier: MIT

package daemon

import (
	"reflect"
	"strings"
	"testing"
)

// EVERY ACTION THE API EXPOSES MUST BE WIRED, and this test exists because
// twice now one silently was not.
//
// webui.Actions is a struct of function fields, and a nil one is not a compile
// error — it is a route that answers 503 at runtime. The handlers all check for
// nil and report politely, which is right, but it means an unwired action looks
// exactly like a working one until somebody presses the button. Both times the
// cause was the same: a patch to the Actions literal that did not match the
// text it expected, and compiled perfectly.
//
// Reflection rather than a hand-written list, because a hand-written list is
// the thing that gets forgotten alongside the wiring.
func TestEveryActionIsWired(t *testing.T) {
	got := reflect.ValueOf(buildActions(&daemon{}))
	typ := got.Type()

	// Fields that are deliberately optional, with the reason each is allowed
	// to be nil. Anything not named here must be wired.
	optional := map[string]string{
		"LANGate": "nil leaves the network view open to everyone, which is what it was before the setting existed",
	}

	var missing []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if _, ok := optional[f.Name]; ok {
			continue
		}
		v := got.Field(i)
		if v.Kind() != reflect.Func && v.Kind() != reflect.Ptr {
			continue
		}
		if v.IsNil() {
			missing = append(missing, f.Name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("these actions are not wired, so their routes answer 503 at runtime:\n  %s",
			strings.Join(missing, "\n  "))
	}
}
