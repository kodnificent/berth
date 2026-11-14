package winsys

import (
	"encoding/binary"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetExtendedTcpTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

const tcpTableOwnerPIDListener = 3

// The rows of TCP_TABLE_OWNER_PID_LISTENER for IPv4 and IPv6, from iphlpapi.
type tcpRow struct {
	State, LocalAddr, LocalPort, RemoteAddr, RemotePort, PID uint32
}

type tcp6Row struct {
	LocalAddr     [16]byte
	LocalScopeID  uint32
	LocalPort     uint32
	RemoteAddr    [16]byte
	RemoteScopeID uint32
	RemotePort    uint32
	State         uint32
	PID           uint32
}

// Listeners lists the pids of the processes listening on TCP port, on any address.
func Listeners(port int) ([]int, error) {
	seen := map[int]bool{}
	var pids []int
	for _, family := range []uint32{windows.AF_INET, windows.AF_INET6} {
		table, err := tcpTable(family)
		if err != nil {
			return nil, err
		}
		n := binary.LittleEndian.Uint32(table)
		rows := unsafe.Pointer(&table[4])
		for i := range uintptr(n) {
			var local, pid uint32
			if family == windows.AF_INET {
				r := (*tcpRow)(unsafe.Add(rows, i*unsafe.Sizeof(tcpRow{})))
				local, pid = r.LocalPort, r.PID
			} else {
				r := (*tcp6Row)(unsafe.Add(rows, i*unsafe.Sizeof(tcp6Row{})))
				local, pid = r.LocalPort, r.PID
			}
			// The port is in network byte order in the low two bytes.
			if int(local&0xff)<<8|int(local>>8&0xff) == port && !seen[int(pid)] {
				seen[int(pid)] = true
				pids = append(pids, int(pid))
			}
		}
	}
	return pids, nil
}

func tcpTable(family uint32) ([]byte, error) {
	size := uint32(16 << 10)
	for {
		buf := make([]byte, size)
		r, _, _ := procGetExtendedTcpTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, uintptr(family), tcpTableOwnerPIDListener, 0)
		switch windows.Errno(r) {
		case 0:
			return buf, nil
		case windows.ERROR_INSUFFICIENT_BUFFER:
			continue
		default:
			return nil, fmt.Errorf("listing TCP listeners: %w", windows.Errno(r))
		}
	}
}
