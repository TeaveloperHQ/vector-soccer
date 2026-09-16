package main

import "net"

// networkMedia 는 primaryIP 를 소유한 인터페이스의 연결 방식을 알려준다.
// 반환값: "wireless"(무선) | "wired"(유선/랜선) | "unknown".
//
// 용도: 교사가 학생 무선망 대신 "업무망 랜선"에 꽂고 켜는 실수를 교사 화면에서 경고하려는 것.
// (학생이 접속할 주소 = primaryIP 이므로, 그 IP 를 가진 인터페이스의 종류를 보여주면 정확하다.)
func networkMedia(primaryIP string) string {
	target := net.ParseIP(primaryIP)
	if target == nil {
		return "unknown"
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return "unknown"
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.Equal(target) {
				return mediaOfInterface(ifi) // 플랫폼별 구현(netmedia_*.go)
			}
		}
	}
	return "unknown"
}
