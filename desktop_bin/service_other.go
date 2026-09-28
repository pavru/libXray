//go:build linux && !android

package main

import (
	"fmt"
	"io"
)

func serviceCommand(_ []string, _, stderr io.Writer) int {
	fmt.Fprintln(stderr, "the Core service is only available on Windows")
	return 1
}
