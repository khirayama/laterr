//go:build !darwin

package menubar

// Run shows the icon until there is nothing left to show. Here there is no
// menu bar.
func Run(Config) error { return errUnsupported }
