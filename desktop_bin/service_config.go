//go:build windows || (linux && !android)

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	confJSON "github.com/xtls/xray-core/infra/conf/json"
)

// Fields through which an Xray configuration makes the Core read or write a
// file named in the configuration (TLS certificates and keys, TLS/REALITY key
// logs, the Hysteria masquerade file server). The Core service runs as
// SYSTEM for any interactive user, so these would expose every file on the
// machine. Review this list when updating Xray-core.
var serviceFileFields = map[string]bool{
	"certificatefile": true,
	"keyfile":         true,
	"masterkeylog":    true,
	"dir":             true,
}

// Fields that may name a Unix domain socket, which the Core would create.
var serviceSocketFields = map[string]bool{
	"listen":  true,
	"dest":    true,
	"address": true,
}

// prepareServiceConfig checks a configuration received over the Core service
// pipe and returns the one the service runs. Logs may only go to logDir, the
// environment is replaced so that assets and certificates are read from the
// service's private copy in assetDir, and fields that name other files are
// refused.
func prepareServiceConfig(text, logDir, assetDir string) ([]byte, error) {
	decoder := json.NewDecoder(&confJSON.Reader{Reader: strings.NewReader(text)})
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("read configuration: %w", err)
	}
	if root == nil {
		return nil, errors.New("configuration must be a JSON object")
	}
	if err := checkServiceLog(root["log"], logDir); err != nil {
		return nil, err
	}
	if err := checkServiceValue(root); err != nil {
		return nil, err
	}
	root["env"] = map[string]any{
		"xray.location.asset": assetDir,
		"xray.location.cert":  assetDir,
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(root); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func checkServiceLog(value any, logDir string) error {
	if value == nil {
		return nil
	}
	log, ok := value.(map[string]any)
	if !ok {
		return errors.New("log must be an object")
	}
	for _, name := range []string{"access", "error"} {
		raw, present := log[name]
		if !present {
			continue
		}
		path, ok := raw.(string)
		if !ok {
			return fmt.Errorf("log.%s must be a string", name)
		}
		if path == "" || strings.EqualFold(path, "none") {
			continue
		}
		want := filepath.Join(logDir, name+".log")
		if !strings.EqualFold(filepath.Clean(path), want) {
			return fmt.Errorf("log.%s must be %s or none", name, want)
		}
	}
	return nil
}

func checkServiceValue(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if err := checkServiceField(key, child); err != nil {
				return err
			}
			if err := checkServiceValue(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := checkServiceValue(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkServiceField(key string, value any) error {
	text, ok := value.(string)
	if !ok || text == "" {
		return nil
	}
	name := strings.ToLower(key)
	if serviceFileFields[name] {
		return fmt.Errorf("configuration field %q names a file, which the Core service does not allow", key)
	}
	if serviceSocketFields[name] && !strings.Contains(text, "://") &&
		(strings.ContainsAny(text, `/\`) || strings.HasPrefix(text, "@")) {
		return fmt.Errorf("configuration field %q names a Unix socket, which the Core service does not allow", key)
	}
	return nil
}
