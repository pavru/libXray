//go:build windows || (linux && !android)

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadConfigRequiresTheExpectedContent(t *testing.T) {
	path := filepath.Join(commandTestDirectory(t), "xray.json")
	content := []byte(`{"log":{"loglevel":"none"}}`)
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	expected := strings.ToUpper(hex.EncodeToString(sum[:]))

	if got, err := readConfig(path, expected); err != nil || string(got) != string(content) {
		t.Fatalf("matching config: %q, %v", got, err)
	}
	if err := os.WriteFile(path, []byte(`{"log":{"error":"C:\\Windows\\evil.dll"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfig(path, expected); err == nil {
		t.Fatal("a swapped config was accepted")
	}
}

func TestParseRunOptionsRejectsMalformedConfigHash(t *testing.T) {
	args := []string{"run", "-dns", "8.8.8.8:53", "-interface", "Ethernet", "-config", "xray.json", "-config-sha256", "abc"}
	if _, err := parseRunOptions(args); err == nil {
		t.Fatal("a malformed hash was accepted")
	}
}

func TestLogFilesIgnoresDisabledLogs(t *testing.T) {
	files, err := logFiles([]byte(`{
		// Xray JSON allows comments.
		"log": {"access": "none", "error": "C:/run/error.log"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(files, []string{"C:/run/error.log"}) {
		t.Fatalf("files = %q", files)
	}
}
