package workspace

import "path/filepath"

// CanonicalPath resolves existing filesystem aliases so path comparisons use
// the same spelling on platforms with symlinked or short-name path aliases.
func CanonicalPath(value string) string {
	value = filepath.Clean(value)
	if resolved, err := filepath.EvalSymlinks(value); err == nil {
		value = filepath.Clean(resolved)
	}
	return canonicalPlatformPath(value)
}
