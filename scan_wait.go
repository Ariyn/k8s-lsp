package main

import "time"

const defaultScanWaitTimeout = 1500 * time.Millisecond

// waitForWorkspaceScanOnce waits for the initial workspace scan to finish, up to timeout.
// It returns true if it actually waited (i.e., a scan was in progress / had started).
func waitForWorkspaceScanOnce(timeout time.Duration) bool {
	if state == nil {
		return false
	}

	state.startWorkspaceScan()

	state.scanMu.Lock()
	scanStarted := state.scanStarted
	scanDone := state.ScanDone
	state.scanMu.Unlock()

	if !scanStarted || scanDone == nil {
		return false
	}

	select {
	case <-scanDone:
	case <-time.After(timeout):
	}

	return true
}
