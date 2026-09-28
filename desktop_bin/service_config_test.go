//go:build windows || (linux && !android)

package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPrepareServiceConfigConfinesLogsAndAssets(t *testing.T) {
	logDir := filepath.Join(t.TempDir(), "logs")
	assetDir := filepath.Join(t.TempDir(), "assets")
	access := filepath.ToSlash(filepath.Join(logDir, "access.log"))
	text := `{
		// Xray accepts comments.
		"log": {"access": "` + access + `", "error": "none", "loglevel": "warning"},
		"env": {"xray.location.asset": "C:/Users/me/dat", "xray.location.confdir": "C:/Windows"},
		"dns": {"servers": ["https://1.1.1.1/dns-query", {"address": "tcp://8.8.8.8:53"}]},
		"inbounds": [{"listen": "127.0.0.1", "port": 1080, "protocol": "socks"}],
		"routing": {"rules": [{"domain": ["ext:geosite.dat:cn"], "outboundTag": "direct"}]}
	}`
	config, err := prepareServiceConfig(text, logDir, assetDir)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		t.Fatal(err)
	}
	wantEnv := map[string]any{"xray.location.asset": assetDir, "xray.location.cert": assetDir}
	if !reflect.DeepEqual(root["env"], wantEnv) {
		t.Fatalf("env = %#v", root["env"])
	}
	if root["log"].(map[string]any)["access"] != access {
		t.Fatalf("log changed: %#v", root["log"])
	}
	if bytes.Contains(config, []byte(`\u0026`)) {
		t.Fatal("configuration was HTML-escaped")
	}
}

func TestPrepareServiceConfigRefusesFilesAndSockets(t *testing.T) {
	logDir := filepath.Join(t.TempDir(), "logs")
	cases := map[string]string{
		"other log":        `{"log": {"error": "C:/Windows/System32/drivers/etc/hosts"}}`,
		"log beside":       `{"log": {"access": "` + filepath.ToSlash(filepath.Join(logDir, "error.log")) + `"}}`,
		"log type":         `{"log": {"error": 1}}`,
		"certificate file": `{"outbounds": [{"streamSettings": {"tlsSettings": {"certificates": [{"certificateFile": "C:/secret.pem"}]}}}]}`,
		"key file":         `{"inbounds": [{"streamSettings": {"tlsSettings": {"certificates": [{"KeyFile": "key.pem"}]}}}]}`,
		"key log":          `{"outbounds": [{"streamSettings": {"realitySettings": {"masterKeyLog": "C:/keys.txt"}}}]}`,
		"masquerade":       `{"inbounds": [{"streamSettings": {"hysteriaSettings": {"masquerade": {"type": "file", "dir": "C:/"}}}}]}`,
		"unix listen":      `{"inbounds": [{"listen": "C:/sockets/xray.sock"}]}`,
		"abstract socket":  `{"inbounds": [{"listen": "@xray"}]}`,
		"fallback socket":  `{"inbounds": [{"settings": {"fallbacks": [{"dest": "/run/xray.sock"}]}}]}`,
		"not an object":    `[]`,
		"invalid":          `{`,
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := prepareServiceConfig(text, logDir, t.TempDir()); err == nil {
				t.Fatal("configuration was accepted")
			}
		})
	}
}

func TestServiceMessagesRoundTrip(t *testing.T) {
	var buffer bytes.Buffer
	request := serviceRequest{Command: serviceCommandStart, Config: `{"a":"<&>"}`, DNS: "8.8.8.8:53"}
	if err := writeServiceMessage(&buffer, request); err != nil {
		t.Fatal(err)
	}
	var got serviceRequest
	if err := readServiceMessage(&buffer, &got); err != nil {
		t.Fatal(err)
	}
	if got != request {
		t.Fatalf("got %#v", got)
	}
	for _, frame := range [][]byte{{0, 0, 0, 0}, {0xff, 0xff, 0xff, 0x7f}, {5, 0, 0, 0, '{'}} {
		if err := readServiceMessage(bytes.NewReader(frame), &got); err == nil {
			t.Fatalf("frame %v was accepted", frame)
		}
	}
	if err := writeServiceMessage(&buffer, strings.Repeat("x", maxServiceMessage)); err == nil {
		t.Fatal("oversized message was written")
	}
}
