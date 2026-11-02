package main

import "syscall"

// processAlive reports whether pid runs.
func processAlive(pid int) bool {
	const queryLimitedInformation = 0x1000
	h, err := syscall.OpenProcess(queryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	const stillActive = 259
	return syscall.GetExitCodeProcess(h, &code) == nil && code == stillActive
}
