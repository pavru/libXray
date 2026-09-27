//go:build linux && !android

package main

// checkWriteTarget has nothing to refuse on Linux: the Core runs as the App
// user with network capabilities only, so its writes grant no extra access.
func checkWriteTarget(string) error { return nil }
