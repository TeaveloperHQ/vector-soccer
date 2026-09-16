//go:build windows

package main

import (
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 윈도우(실제 배포 대상): GetAdaptersAddresses 로 인터페이스 종류(IfType)를 읽어 무선/유선 판별.
// IF_TYPE_IEEE80211(71)=무선, IF_TYPE_ETHERNET_CSMACD(6)=유선.
func mediaOfInterface(ifi net.Interface) string {
	size := uint32(15000)
	for try := 0; try < 3; try++ {
		buf := make([]byte, size)
		aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(
			windows.AF_UNSPEC,
			windows.GAA_FLAG_SKIP_UNICAST|windows.GAA_FLAG_SKIP_ANYCAST|
				windows.GAA_FLAG_SKIP_MULTICAST|windows.GAA_FLAG_SKIP_DNS_SERVER,
			0, aa, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue // size 가 필요한 크기로 갱신됨 — 다시 시도
		}
		if err != nil {
			return "unknown"
		}
		for p := aa; p != nil; p = p.Next {
			if int(p.IfIndex) != ifi.Index && int(p.Ipv6IfIndex) != ifi.Index {
				continue
			}
			switch p.IfType {
			case windows.IF_TYPE_IEEE80211:
				return "wireless"
			case windows.IF_TYPE_ETHERNET_CSMACD:
				return "wired"
			default:
				return "unknown"
			}
		}
		return "unknown"
	}
	return "unknown"
}
