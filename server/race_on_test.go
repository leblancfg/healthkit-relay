//go:build race

package main

// raceEnabled: the race detector slows the memory tests down several times and adds its own
// allocations, so they run without it (see skipMemoryTest).
const raceEnabled = true
