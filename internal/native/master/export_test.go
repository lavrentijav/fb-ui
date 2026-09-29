package master

// SetWallClock replaces the host clock and returns the undo.
func SetWallClock(now func() int64) func() {
	old := wallClock
	wallClock = now
	return func() { wallClock = old }
}
