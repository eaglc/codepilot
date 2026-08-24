package ui

// pickerWindow returns a clamped [start, end) range centered around the
// cursor. Callers reserve their own fixed header/footer and row height before
// passing visible so the selected item remains on-screen.
func pickerWindow(cursor, count, visible int) (int, int) {
	if count <= 0 || visible <= 0 {
		return 0, 0
	}
	if count <= visible {
		return 0, count
	}
	cursor = min(max(0, cursor), count-1)
	start := cursor - visible/2
	start = max(0, min(start, count-visible))
	return start, start + visible
}
