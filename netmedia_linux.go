//go:build linux

package main

import (
	"net"
	"os"
)

// 리눅스: /sys/class/net/<iface>/wireless 디렉터리(또는 phy80211 링크)가 있으면 무선.
func mediaOfInterface(ifi net.Interface) string {
	base := "/sys/class/net/" + ifi.Name
	if fi, err := os.Stat(base + "/wireless"); err == nil && fi.IsDir() {
		return "wireless"
	}
	if _, err := os.Lstat(base + "/phy80211"); err == nil {
		return "wireless"
	}
	return "wired"
}
