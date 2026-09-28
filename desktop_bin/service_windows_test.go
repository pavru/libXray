//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestParseServiceOptions(t *testing.T) {
	options, err := parseServiceOptions("install", []string{"-name", "BhsXRayCore", "-pipe", "BhsXRay.Core", "-client", "BhsXRay.exe"})
	if err != nil {
		t.Fatal(err)
	}
	if options.display != "BhsXRayCore" || options.client != "BhsXRay.exe" {
		t.Fatalf("unexpected options: %#v", options)
	}
	if _, err := parseServiceOptions("uninstall", []string{"-name", "BhsXRayCore"}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-name", "Bad Name", "-pipe", "p", "-client", "App.exe"},
		{"-name", "n", "-pipe", `a\b`, "-client", "App.exe"},
		{"-name", "n", "-pipe", "p", "-client", `..\App.exe`},
		{"-name", "n", "-pipe", "p", "-client", "App.dll"},
		{"-name", "n", "-pipe", "p", "-client", "App.exe", "extra"},
	} {
		if _, err := parseServiceOptions("run", args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

func TestCheckServiceDNS(t *testing.T) {
	for _, value := range []string{"8.8.8.8:53", "[2001:4860:4860::8888]:53"} {
		if err := checkServiceDNS(value); err != nil {
			t.Fatalf("%s: %v", value, err)
		}
	}
	for _, value := range []string{"", "8.8.8.8", "dns.google:53", "8.8.8.8:0", "8.8.8.8:x"} {
		if err := checkServiceDNS(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func TestPipeServesOnlyTheInstalledApp(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := newCoreSupervisor(executable, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Creating further instances needs FILE_CREATE_PIPE_INSTANCE, which the
	// service has as SYSTEM; a test runs as an ordinary user.
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	defaultSDDL := servicePipeSDDL
	servicePipeSDDL += "(A;;GA;;;" + user.User.Sid.String() + ")"
	defer func() { servicePipeSDDL = defaultSDDL }()
	name := fmt.Sprintf("libxray-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	server, err := listenPipe(name, executable, supervisor)
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	if _, err := listenPipe(name, executable, supervisor); err == nil {
		t.Fatal("a second server took the same pipe name")
	}

	response := pipeRequest(t, name, serviceRequest{Command: serviceCommandStatus})
	if !response.OK || response.State != serviceStateStopped {
		t.Fatalf("status = %#v", response)
	}
	response = pipeRequest(t, name, serviceRequest{Command: serviceCommandWatch, Known: serviceStateRunning})
	if !response.OK || response.State != serviceStateStopped {
		t.Fatalf("watch = %#v", response)
	}
	response = pipeRequest(t, name, serviceRequest{Command: serviceCommandStop})
	if !response.OK || response.State != serviceStateStopped {
		t.Fatalf("stop = %#v", response)
	}
	response = pipeRequest(t, name, serviceRequest{Command: serviceCommandStart, Assets: "relative"})
	if response.OK || !strings.Contains(response.Error, "absolute") {
		t.Fatalf("start = %#v", response)
	}

	other := name + "-other"
	otherServer, err := listenPipe(other, filepath.Join(filepath.Dir(executable), "App.exe"), supervisor)
	if err != nil {
		t.Fatal(err)
	}
	defer otherServer.close()
	response = pipeRequest(t, other, serviceRequest{Command: serviceCommandStatus})
	if response.OK || !strings.Contains(response.Error, "installed App") {
		t.Fatalf("foreign client = %#v", response)
	}
}

func TestWatchWaitsForAChange(t *testing.T) {
	supervisor, err := newCoreSupervisor("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if response := supervisor.watch(serviceStateStopped, 50*time.Millisecond); response.State != serviceStateStopped {
		t.Fatalf("watch = %#v", response)
	}
	if time.Since(started) < 50*time.Millisecond {
		t.Fatal("watch returned before its timeout")
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		supervisor.mu.Lock()
		supervisor.setStateLocked(serviceStateRunning, "")
		supervisor.mu.Unlock()
	}()
	if response := supervisor.watch(serviceStateStopped, 5*time.Second); response.State != serviceStateRunning {
		t.Fatalf("watch = %#v", response)
	}
}

func TestLogDirectoryIsRecreatedForTheReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(filepath.Join(path, "old"), 0700); err != nil {
		t.Fatal(err)
	}
	sid, err := windows.CreateWellKnownSid(windows.WinInteractiveSid)
	if err != nil {
		t.Fatal(err)
	}
	if err := createLogDirectory(path, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, "old")); !os.IsNotExist(err) {
		t.Fatalf("old content remains: %v", err)
	}
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if text := descriptor.String(); !strings.Contains(text, ";;;IU)") || !strings.Contains(text, "D:P") {
		t.Fatalf("unexpected DACL %s", text)
	}
}

// pipeRequest opens the pipe the way the App does: without
// FILE_APPEND_DATA, which the pipe's DACL withholds from users.
func pipeRequest(t *testing.T, name string, request serviceRequest) serviceResponse {
	t.Helper()
	path, err := windows.UTF16PtrFromString(`\\.\pipe\` + name)
	if err != nil {
		t.Fatal(err)
	}
	var handle windows.Handle
	for attempt := 0; ; attempt++ {
		handle, err = windows.CreateFile(path, windows.GENERIC_READ|windows.FILE_WRITE_DATA, 0, nil, windows.OPEN_EXISTING, 0, 0)
		if err == nil {
			break
		}
		// Between connections the server may not have its next instance yet.
		if err != windows.ERROR_PIPE_BUSY || attempt == 50 {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	pipe := os.NewFile(uintptr(handle), name)
	defer pipe.Close()
	if err := writeServiceMessage(pipe, request); err != nil {
		t.Fatal(err)
	}
	var response serviceResponse
	if err := readServiceMessage(pipe, &response); err != nil {
		t.Fatal(err)
	}
	return response
}
