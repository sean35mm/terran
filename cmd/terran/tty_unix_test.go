//go:build darwin || linux

package main

import (
	"os"
	"testing"
)

func TestCharacterDeviceIsNotNecessarilyTerminal(t *testing.T) {
	file, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		t.Fatalf("test requires a character-device null file: mode=%v", info.Mode())
	}
	if terminalFile(file) {
		t.Fatal("non-terminal character device enabled prompting")
	}
}
