//go:build windows

package sandbox

import (
	"net/url"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestPreparedLocalCacheEncodesFileProxyPath(t *testing.T) {
	original, err := localEnvironment(map[string]string{"GOMODCACHE": `C:\scratch\go-mod`, "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local"})
	if err != nil {
		t.Fatal(err)
	}
	value, err := localPreparedGoEnvironment(original, `C:\workspace 项目,cache\.traverse-preparation\go-mod`)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range strings.Split(string(utf16.Decode(value)), "\x00") {
		if proxy, found := strings.CutPrefix(entry, "GOPROXY="); found {
			parsed, err := url.Parse(proxy)
			if err != nil || parsed.Scheme != "file" || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
				parsed.Path != "/C:/workspace 项目,cache/.traverse-preparation/go-mod/cache/download" || strings.ContainsAny(proxy, ",| ") {
				t.Fatalf("file proxy encoding %q: %+v %v", proxy, parsed, err)
			}
			return
		}
	}
	t.Fatal("file proxy missing")
}

func TestPreparedLocalCacheKeepsNetworkDisabled(t *testing.T) {
	original, err := localEnvironment(map[string]string{"GOMODCACHE": `C:\scratch\go-mod`, "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local", "GOWORK": "off"})
	if err != nil {
		t.Fatal(err)
	}
	value, err := localPreparedGoEnvironment(original, `C:\workspace\.traverse-preparation\cache\go-mod`)
	if err != nil {
		t.Fatal(err)
	}
	bindings := strings.Split(string(utf16.Decode(value)), "\x00")
	for _, expected := range []string{`GOMODCACHE=C:\scratch\go-mod`, "GOPROXY=file:///C:/workspace/.traverse-preparation/cache/go-mod/cache/download", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOWORK=off"} {
		found := false
		for _, binding := range bindings {
			if binding == expected {
				found = true
			}
		}
		if !found {
			t.Fatalf("prepared environment lost %s", expected)
		}
	}
	if _, err := localPreparedGoEnvironment(original, "../outside"); err == nil {
		t.Fatal("file proxy accepted an unbound relative cache path")
	}
}
