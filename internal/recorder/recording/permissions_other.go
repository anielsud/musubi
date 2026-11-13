//go:build !darwin

package recording

// CheckPermissions does nothing: only macOS asks for these permissions.
func CheckPermissions() {}
