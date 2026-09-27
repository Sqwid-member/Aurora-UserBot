package app

import "runtime/debug"

// debugSetMemoryLimit applies the Go runtime's soft memory limit.
//
// This is the single biggest lever for "lowest possible RAM" on a phone: once
// the heap approaches the cap the collector works harder instead of letting
// Android's OOM killer decide our fate.
func debugSetMemoryLimit(limit int64) {
	if limit <= 0 {
		debug.SetMemoryLimit(-1)
		return
	}
	debug.SetMemoryLimit(limit)
}
