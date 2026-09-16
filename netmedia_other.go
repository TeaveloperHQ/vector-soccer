//go:build !linux && !windows

package main

import "net"

// 그 외 OS(예: macOS): 판별 수단을 두지 않고 불명 처리.
func mediaOfInterface(net.Interface) string { return "unknown" }
