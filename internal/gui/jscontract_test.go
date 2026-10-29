package gui

import (
	"io/fs"
	"reflect"
	"regexp"
	"sort"
	"testing"
)

// goWiring is bound by Wails but never called from the page.
var goWiring = map[string]bool{"Attach": true, "SetEmitter": true, "Sink": true}

var bridgeCallRE = regexp.MustCompile(`call\('(\w+)'\)`)

// TestBridgeJSContract pins the page's method list to *Bridge's exported
// surface: every call('Name') in bridge.js must exist on *Bridge, and
// every exported *Bridge method must be listed in bridge.js. Arity is
// checked at render time by the fixture bridge (/arity.json).
func TestBridgeJSContract(t *testing.T) {
	src, err := fs.ReadFile(Assets, "frontend/js/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, m := range bridgeCallRE.FindAllStringSubmatch(string(src), -1) {
		listed[m[1]] = true
	}
	typ := reflect.TypeOf((*Bridge)(nil))
	var missing []string
	exported := map[string]bool{}
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		if goWiring[name] {
			continue
		}
		exported[name] = true
		if !listed[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for name := range listed {
		if !exported[name] && !goWiring[name] {
			t.Errorf("bridge.js calls %q but *Bridge has no such method", name)
		}
	}
	for _, name := range missing {
		t.Errorf("*Bridge exports %q but bridge.js does not list it", name)
	}
}
