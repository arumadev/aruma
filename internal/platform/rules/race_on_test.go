//go:build race

package rules

// raceEnabled widens the bounds tests' limits on time and memory: the race detector makes code
// several times slower and keeps sync.Pool from reusing what it holds.
const raceEnabled = true
