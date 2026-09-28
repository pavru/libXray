//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	coreStartGrace   = time.Second
	coreStopTimeout  = 5 * time.Second
	maxCoreErrorText = 64 * 1024
)

// coreSupervisor runs at most one Core (`OneXrayCore.exe run`) as a child of
// the service. The child sits in a job that kills it if the service dies.
type coreSupervisor struct {
	executable string
	root       string
	job        windows.Handle

	operation sync.Mutex // serializes start and stop

	mu        sync.Mutex
	process   *os.Process
	exited    chan struct{}
	errorFile string
	owner     uint32 // session that started the running Core
	state     string
	lastError string
	changed   chan struct{}
}

func newCoreSupervisor(executable, root string) (*coreSupervisor, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	return &coreSupervisor{
		executable: executable,
		root:       root,
		job:        job,
		state:      serviceStateStopped,
		changed:    make(chan struct{}),
	}, nil
}

func (s *coreSupervisor) status() serviceResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	return serviceResponse{OK: true, State: s.state, Error: s.lastError}
}

func (s *coreSupervisor) watch(known string, timeout time.Duration) serviceResponse {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.mu.Lock()
		if s.state != known {
			s.mu.Unlock()
			return s.status()
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-changed:
		case <-timer.C:
			return s.status()
		}
	}
}

// setStateLocked records a state and wakes watchers; s.mu must be held.
func (s *coreSupervisor) setStateLocked(state, lastError string) {
	s.state = state
	s.lastError = lastError
	close(s.changed)
	s.changed = make(chan struct{})
}

// mayControl lets a Core started in one Windows session be replaced or
// stopped only from that session, or by an administrator.
func (s *coreSupervisor) mayControl(client clientInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.process != nil && s.owner != client.session && !client.elevated {
		return errors.New("the VPN is in use by another Windows user")
	}
	return nil
}

func (s *coreSupervisor) start(request serviceRequest, client clientInfo, assets []*os.File) serviceResponse {
	s.operation.Lock()
	defer s.operation.Unlock()
	if err := s.mayControl(client); err != nil {
		return serviceResponse{Error: err.Error()}
	}
	if err := s.stopCore(); err != nil {
		return serviceResponse{Error: err.Error()}
	}
	if err := s.launch(request, client, assets); err != nil {
		return serviceResponse{Error: err.Error()}
	}
	return s.status()
}

func (s *coreSupervisor) launch(request serviceRequest, client clientInfo, assets []*os.File) error {
	if err := checkServiceDNS(request.DNS); err != nil {
		return err
	}
	if request.Interface == "" || strings.ContainsRune(request.Interface, 0) {
		return errors.New("interface is required")
	}
	runDir := filepath.Join(s.root, "run")
	logDir := filepath.Join(s.root, "logs")
	assetDir := filepath.Join(runDir, "assets")
	if err := os.RemoveAll(runDir); err != nil {
		return err
	}
	if err := os.MkdirAll(assetDir, 0700); err != nil {
		return err
	}
	if err := createLogDirectory(logDir, client.sid); err != nil {
		return err
	}
	config, err := prepareServiceConfig(request.Config, logDir, assetDir)
	if err != nil {
		return err
	}
	if err := copyAssets(assets, assetDir); err != nil {
		return err
	}
	configPath := filepath.Join(runDir, "config.json")
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		return err
	}
	errorFile := filepath.Join(runDir, "error.txt")
	if err := os.WriteFile(errorFile, nil, 0600); err != nil {
		return err
	}
	sum := sha256.Sum256(config)
	command := exec.Command(
		s.executable,
		"run",
		"-dns", request.DNS,
		"-interface", request.Interface,
		"-config", configPath,
		"-config-sha256", hex.EncodeToString(sum[:]),
		"-error-file", errorFile,
	)
	command.Dir = runDir
	command.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	if err := command.Start(); err != nil {
		return err
	}
	if err := s.assignJob(command.Process.Pid); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return err
	}
	exited := make(chan struct{})
	s.mu.Lock()
	s.process = command.Process
	s.exited = exited
	s.errorFile = errorFile
	s.owner = client.session
	s.mu.Unlock()
	go s.reap(command, exited)

	select {
	case <-exited:
		s.mu.Lock()
		defer s.mu.Unlock()
		return errors.New(s.lastError)
	case <-time.After(coreStartGrace):
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.process == command.Process && s.state != serviceStateRunning {
		s.setStateLocked(serviceStateRunning, "")
	}
	return nil
}

func (s *coreSupervisor) assignJob(pid int) error {
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	return windows.AssignProcessToJobObject(s.job, process)
}

// reap records a Core that exits on its own; a stop clears s.process first.
func (s *coreSupervisor) reap(command *exec.Cmd, exited chan struct{}) {
	waitErr := command.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	defer close(exited)
	if s.process != command.Process {
		return
	}
	message := readCoreError(s.errorFile)
	if message == "" {
		message = "Core exited"
		if waitErr != nil {
			message = "Core exited: " + waitErr.Error()
		}
	}
	s.process = nil
	s.setStateLocked(serviceStateStopped, message)
}

func (s *coreSupervisor) stop(client clientInfo) serviceResponse {
	s.operation.Lock()
	defer s.operation.Unlock()
	if err := s.mayControl(client); err != nil {
		return serviceResponse{Error: err.Error()}
	}
	if err := s.stopCore(); err != nil {
		return serviceResponse{Error: err.Error()}
	}
	return s.status()
}

func (s *coreSupervisor) stopAll() {
	s.operation.Lock()
	defer s.operation.Unlock()
	_ = s.stopCore()
}

func (s *coreSupervisor) sessionLoggedOff(session uint32) {
	s.operation.Lock()
	defer s.operation.Unlock()
	s.mu.Lock()
	owned := s.process != nil && s.owner == session
	s.mu.Unlock()
	if owned {
		_ = s.stopCore()
	}
}

// stopCore must be called with s.operation held.
func (s *coreSupervisor) stopCore() error {
	s.mu.Lock()
	process, exited := s.process, s.exited
	s.process = nil
	if process == nil {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		s.mu.Lock()
		s.process = process
		s.mu.Unlock()
		return err
	}
	select {
	case <-exited:
	case <-time.After(coreStopTimeout):
		return errors.New("timed out waiting for the Core to stop")
	}
	s.mu.Lock()
	s.setStateLocked(serviceStateStopped, "")
	s.mu.Unlock()
	return nil
}

func checkServiceDNS(value string) error {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("dns must be IP:port: %w", err)
	}
	number, err := strconv.Atoi(port)
	if net.ParseIP(host) == nil || err != nil || number < 1 || number > 65535 {
		return errors.New("dns must be IP:port")
	}
	return nil
}

// createLogDirectory recreates the log folder so that only SYSTEM and
// Administrators may write in it and the App user may read the logs.
func createLogDirectory(path string, reader *windows.SID) error {
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	sddl := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	if reader != nil {
		sddl += "(A;OICI;FR;;;" + reader.String() + ")"
	}
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.CreateDirectory(name, &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	})
}

func copyAssets(assets []*os.File, directory string) error {
	remaining := int64(maxServiceAssetBytes)
	for _, source := range assets {
		name := filepath.Base(source.Name())
		target, err := os.OpenFile(filepath.Join(directory, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		written, err := io.Copy(target, io.LimitReader(source, remaining+1))
		closeErr := target.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		remaining -= written
		if remaining < 0 {
			return fmt.Errorf("assets exceed %d MB", maxServiceAssetBytes>>20)
		}
	}
	return nil
}

func readCoreError(path string) string {
	if path == "" {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	data, _ := io.ReadAll(io.LimitReader(file, maxCoreErrorText))
	return strings.TrimSpace(string(data))
}
