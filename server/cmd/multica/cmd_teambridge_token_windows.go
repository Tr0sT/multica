package main

import "os"

func openTeamBridgeTokenInput(directory *os.Root, name string) (*os.File, error) {
	return directory.OpenFile(name, os.O_RDONLY, 0)
}
