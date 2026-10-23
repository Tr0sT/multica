//go:build !windows

package main

import (
	"os"
	"syscall"
)

func openTeamBridgeTokenInput(directory *os.Root, name string) (*os.File, error) {
	return directory.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
