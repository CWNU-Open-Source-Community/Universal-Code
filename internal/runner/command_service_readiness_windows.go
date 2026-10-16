//go:build windows

package runner

import (
	"encoding/binary"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

var commandServiceTCPTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")
var commandServiceProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

// Check membership against our retained Job Object, never PID ancestry or a
// process name. A foreign listener on a reused port must not become our service.
func (p *windowsCommandRuntimeProcess) OwnsTCPListener(ip net.IP, port uint16) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.job == 0 {
		return false
	}
	family, stride, portOffset, pidOffset := uintptr(windows.AF_INET), 24, 8, 20
	if ip.To4() == nil {
		family, stride, portOffset, pidOffset = windows.AF_INET6, 56, 20, 52
	}
	var size uint32
	const ownerPIDListeners = 3
	code, _, _ := commandServiceTCPTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, family, ownerPIDListeners, 0)
	if code != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) || size < 4 || size > 1<<20 {
		return false
	}
	buffer := make([]byte, size)
	code, _, _ = commandServiceTCPTable.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)), 0, family, ownerPIDListeners, 0)
	if code != 0 || size < 4 || int(size) > len(buffer) {
		return false
	}
	count := int(binary.LittleEndian.Uint32(buffer[:4]))
	if count > (int(size)-4)/stride {
		return false
	}
	found := false
	for i := 0; i < count; i++ {
		row := buffer[4+i*stride : 4+(i+1)*stride]
		if binary.BigEndian.Uint16(row[portOffset:portOffset+2]) != port {
			continue
		}
		bound := net.IP(row[4:8])
		if family == windows.AF_INET6 {
			bound = net.IP(row[:16])
		}
		if !bound.IsUnspecified() && !bound.Equal(ip) {
			continue
		}
		pid := binary.LittleEndian.Uint32(row[pidOffset : pidOffset+4])
		process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if err != nil {
			return false
		}
		var member int32
		ok, _, _ := commandServiceProcessInJob.Call(uintptr(process), uintptr(p.job), uintptr(unsafe.Pointer(&member)))
		_ = windows.CloseHandle(process)
		if ok == 0 || member == 0 {
			return false
		}
		found = true
	}
	return found
}
