//go:build windows || (linux && !android)

package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Core service protocol: every message is a 4-byte little-endian length
// followed by that many bytes of JSON. A connection carries one request and
// one response.
const maxServiceMessage = 32 << 20

const (
	serviceCommandStart  = "start"
	serviceCommandStop   = "stop"
	serviceCommandStatus = "status"
	// watch answers when the state differs from known, or after a timeout.
	serviceCommandWatch = "watch"
)

const (
	serviceStateStopped = "stopped"
	serviceStateRunning = "running"
)

type serviceRequest struct {
	Command string `json:"command"`
	// start only.
	Config    string `json:"config,omitempty"`
	DNS       string `json:"dns,omitempty"`
	Interface string `json:"interface,omitempty"`
	Assets    string `json:"assets,omitempty"`
	// watch only.
	Known string `json:"known,omitempty"`
}

type serviceResponse struct {
	OK    bool   `json:"ok"`
	State string `json:"state,omitempty"`
	// The last Core failure; with OK false, why this request failed.
	Error string `json:"error,omitempty"`
}

func writeServiceMessage(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > maxServiceMessage {
		return errors.New("service message is too large")
	}
	frame := make([]byte, 4+len(data))
	binary.LittleEndian.PutUint32(frame, uint32(len(data)))
	copy(frame[4:], data)
	_, err = w.Write(frame)
	return err
}

func readServiceMessage(r io.Reader, value any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	size := binary.LittleEndian.Uint32(header[:])
	if size == 0 || size > maxServiceMessage {
		return fmt.Errorf("invalid service message size %d", size)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}
