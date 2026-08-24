//go:build !windows

package workspace

func canonicalPlatformPath(value string) string { return value }
