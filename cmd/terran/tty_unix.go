//go:build darwin || linux

package main

import "os"

func terminalFile(file *os.File) bool {
	return file != nil && isTerminalFD(file.Fd())
}
