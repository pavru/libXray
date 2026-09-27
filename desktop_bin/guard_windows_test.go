//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCheckWriteTargetRefusesLinks(t *testing.T) {
	directory := commandTestDirectory(t)
	session := filepath.Join(directory, "session")
	if err := os.Mkdir(session, 0700); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(session, "error.log")
	if err := os.WriteFile(plain, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkWriteTarget(plain); err != nil {
		t.Fatalf("plain file refused: %v", err)
	}
	if err := checkWriteTarget(filepath.Join(session, "missing.log")); err != nil {
		t.Fatalf("new file refused: %v", err)
	}

	// Standard users may create junctions and hard links without privileges.
	junction := filepath.Join(directory, "junction")
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", junction, session).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v: %s", err, output)
	}
	if err := checkWriteTarget(filepath.Join(junction, "error.log")); err == nil {
		t.Fatal("a write through a junction was accepted")
	}
	hardLink := filepath.Join(session, "access.log")
	if err := os.Link(plain, hardLink); err != nil {
		t.Fatal(err)
	}
	if err := checkWriteTarget(hardLink); err == nil {
		t.Fatal("a hard-linked file was accepted")
	}
}
