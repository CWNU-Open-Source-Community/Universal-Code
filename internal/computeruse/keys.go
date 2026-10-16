package computeruse

import (
	"errors"
	"strconv"
	"strings"
)

func parseChord(s string) ([]uint16, error) {
	parts := strings.Split(strings.ToUpper(strings.TrimSpace(s)), "+")
	if len(parts) > 5 {
		return nil, errors.New("too many keys")
	}
	named := map[string]uint16{"CTRL": 0x11, "CONTROL": 0x11, "SHIFT": 0x10, "ALT": 0x12, "WIN": 0x5b, "ENTER": 0x0d, "TAB": 9, "ESC": 0x1b, "ESCAPE": 0x1b, "SPACE": 0x20, "BACKSPACE": 8, "DELETE": 0x2e, "INSERT": 0x2d, "HOME": 0x24, "END": 0x23, "PAGEUP": 0x21, "PAGEDOWN": 0x22, "LEFT": 0x25, "UP": 0x26, "RIGHT": 0x27, "DOWN": 0x28}
	for i := uint16(1); i <= 12; i++ {
		named["F"+strconv.Itoa(int(i))] = 0x6f + i
	}
	seen := map[uint16]bool{}
	out := make([]uint16, 0, len(parts))
	for i, p := range parts {
		p = strings.TrimSpace(p)
		k, ok := named[p]
		if !ok && len(p) == 1 && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= '0' && p[0] <= '9')) {
			k = uint16(p[0])
			ok = true
		}
		if !ok || seen[k] {
			return nil, errors.New("unsupported or duplicate key")
		}
		if i < len(parts)-1 && k != 0x10 && k != 0x11 && k != 0x12 && k != 0x5b {
			return nil, errors.New("only modifiers may precede the final key")
		}
		seen[k] = true
		out = append(out, k)
	}
	return out, nil
}

// KeyRequiresDesktopApproval identifies known Windows shortcuts whose routing
// reaches the desktop or another application. It uses the same virtual keys as
// validation and SendInput; an application grant cannot authorize these calls.
func KeyRequiresDesktopApproval(s string) (bool, error) {
	keys, err := parseChord(s)
	if err != nil {
		return false, err
	}
	var ctrl, alt, shift, win bool
	var key uint16
	for _, vk := range keys {
		switch vk {
		case 0x11:
			ctrl = true
		case 0x12:
			alt = true
		case 0x10:
			shift = true
		case 0x5b:
			win = true
		default:
			key = vk
		}
	}
	return win || (alt && (key == 9 || key == 0x1b)) || (ctrl && key == 0x1b) ||
		(ctrl && alt && (key == 0x2e || key == 0x2d)) ||
		(key == 0 && shift && (alt || ctrl)), nil
}
