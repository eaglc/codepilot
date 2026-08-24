//go:build windows

package workspace

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func canonicalPlatformPath(value string) string {
	path, err := windows.UTF16PtrFromString(value)
	if err != nil {
		return value
	}
	bufferSize := uint32(260)
	for attempt := 0; attempt < 2; attempt++ {
		buffer := make([]uint16, bufferSize)
		length, err := windows.GetLongPathName(path, &buffer[0], bufferSize)
		if err != nil || length == 0 {
			return value
		}
		if length < bufferSize {
			return filepath.Clean(windows.UTF16ToString(buffer[:length]))
		}
		bufferSize = length + 1
	}
	return value
}
