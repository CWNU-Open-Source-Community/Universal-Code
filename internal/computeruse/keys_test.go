package computeruse

import "testing"

func TestKeyRequiresDesktopApprovalUsesNativeChordSemantics(t *testing.T) {
	for _, chord := range []string{
		"WIN", "WIN+D", "WIN+E", "WIN+R", "CTRL+SHIFT+WIN+LEFT",
		"ALT+TAB", "SHIFT+ALT+TAB", "ALT+CTRL+TAB", "ALT+ESC", "ALT+SHIFT+ESCAPE",
		"CTRL+ESC", "CONTROL+SHIFT+ESCAPE", "CTRL+ALT+DELETE", "SHIFT+ALT+CTRL+INSERT",
		"ALT+SHIFT", "SHIFT+ALT", "CTRL+SHIFT", "SHIFT+CONTROL", "ALT+CTRL+SHIFT",
		"  control + escape  ",
	} {
		t.Run(chord, func(t *testing.T) {
			global, err := KeyRequiresDesktopApproval(chord)
			if err != nil || !global {
				t.Fatalf("system shortcut was not identified: %q %v", chord, err)
			}
		})
	}
	for _, chord := range []string{"CTRL+A", "CTRL+SHIFT+S", "SHIFT+TAB", "ALT+F4", "ALT+SPACE", "F12", "CTRL+ALT+A", "ESC", "ENTER", "CTRL", "SHIFT"} {
		t.Run(chord, func(t *testing.T) {
			global, err := KeyRequiresDesktopApproval(chord)
			if err != nil || global {
				t.Fatalf("ordinary application chord was classified as a system shortcut: %q %v", chord, err)
			}
		})
	}
	for _, chord := range []string{"", "CTRL++ESC", "CONTROL+CTRL+ESC", "TAB+ALT", "WIN+F13"} {
		t.Run(chord, func(t *testing.T) {
			if _, err := KeyRequiresDesktopApproval(chord); err == nil {
				t.Fatalf("invalid native chord accepted: %q", chord)
			}
		})
	}
}
