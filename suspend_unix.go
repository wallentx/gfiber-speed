//go:build unix

package main

import (
	"syscall"
)

// A suspended process cannot restore the cursor, and a suspended transfer
// cannot produce a continuous measurement. End the interactive run on Ctrl-Z.
func init() { dashboardInterrupts = append(dashboardInterrupts, syscall.SIGTSTP) }
