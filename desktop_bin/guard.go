//go:build windows || (linux && !android)

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	confJSON "github.com/xtls/xray-core/infra/conf/json"
)

// readConfig reads the Xray configuration once. With an expected SHA-256 it
// rejects any other content: the file lives in a folder the unprivileged App
// user controls, and swapping it before this privileged Core reads it would
// otherwise control what the Core does, including where it writes logs.
func readConfig(path, expectedSHA256 string) ([]byte, error) {
	config, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if expectedSHA256 == "" {
		return config, nil
	}
	sum := sha256.Sum256(config)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), expectedSHA256) {
		return nil, errors.New("configuration does not match its expected SHA-256")
	}
	return config, nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

// logFiles returns the files Xray will write from the configuration's log
// section; "none" and empty values write nothing.
func logFiles(config []byte) ([]string, error) {
	var root struct {
		Log struct {
			Access string `json:"access"`
			Error  string `json:"error"`
		} `json:"log"`
	}
	decoder := json.NewDecoder(&confJSON.Reader{Reader: bytes.NewReader(config)})
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("read log settings: %w", err)
	}
	var files []string
	for _, path := range []string{root.Log.Access, root.Log.Error} {
		if path != "" && !strings.EqualFold(path, "none") {
			files = append(files, path)
		}
	}
	return files, nil
}

// checkWriteTargets refuses files this Core would write through a link; see
// checkWriteTarget.
func checkWriteTargets(paths ...string) error {
	for _, path := range paths {
		if path == "" {
			continue
		}
		if err := checkWriteTarget(path); err != nil {
			return err
		}
	}
	return nil
}
