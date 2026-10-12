//go:build windows

package server

import "os"

func managerSignals() []os.Signal { return []os.Signal{os.Interrupt} }
