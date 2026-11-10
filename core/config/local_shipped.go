//go:build !devserver

package config

// A shipped build sends model requests to the caveira account and nowhere
// else: no address is a model server it may use, so it carries no way to
// name one (local.go is in development builds only).

// CheckLocal refuses every address in a shipped build.
func CheckLocal(string) error { return ErrAccountOnly }

// IsLocal is false in a shipped build.
func IsLocal(string) bool { return false }
