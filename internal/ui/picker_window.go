package ui

// maxVisiblePickerItems bounds how many selectable rows a provider/session
// picker renders at once. The cursor is windowed around the highlight and
// overflow indicators are shown so the highlight never scrolls off-screen.
const maxVisiblePickerItems = 8

// pickerWindow returns the [start, end) index range of items visible around the
// cursor so the highlight stays on-screen. When the list is longer than visible,
// the window centers the cursor and clamps to the list bounds.
func pickerWindow(cursor int, count int, visible int) (int, int) {
	if visible <= 0 || count <= 0 {
		return 0, 0
	}
	if count <= visible {
		return 0, count
	}
	cursor = clampInt(cursor, 0, count-1)
	start := max(cursor-visible/2, 0)
	start = min(start, count-visible)
	return start, start + visible
}
