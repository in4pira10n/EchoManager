//go:build !windows

package server

import (
	"os"
	"syscall"
)

func managerSignals() []os.Signal { return []os.Signal{os.Interrupt, syscall.SIGTERM} }
