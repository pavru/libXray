//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Interactive users may read and write requests, but not create pipe
// instances (FILE_CREATE_PIPE_INSTANCE is FILE_APPEND_DATA, left out of
// 0x12019b), so no user can serve this pipe name to other Apps. Clients must
// open the pipe with GENERIC_READ | FILE_WRITE_DATA, and should check that
// SYSTEM or Administrators own it.
var servicePipeSDDL = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x12019b;;;IU)"

const (
	maxServiceConnections = 16
	serviceRequestTimeout = 10 * time.Second
	serviceWatchTimeout   = 25 * time.Second
	pipeBufferSize        = 64 * 1024
)

var (
	advapi32                       = windows.NewLazySystemDLL("advapi32.dll")
	procImpersonateNamedPipeClient = advapi32.NewProc("ImpersonateNamedPipeClient")
)

type pipeServer struct {
	path       string
	clientPath string
	security   *windows.SecurityAttributes
	supervisor *coreSupervisor
	slots      chan struct{}
	closed     chan struct{}
	closeOnce  sync.Once
}

// clientInfo identifies the user behind one pipe connection.
type clientInfo struct {
	session  uint32
	elevated bool
	sid      *windows.SID
}

func listenPipe(name, clientPath string, supervisor *coreSupervisor) (*pipeServer, error) {
	descriptor, err := windows.SecurityDescriptorFromString(servicePipeSDDL)
	if err != nil {
		return nil, err
	}
	server := &pipeServer{
		path:       `\\.\pipe\` + name,
		clientPath: clientPath,
		security: &windows.SecurityAttributes{
			Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
			SecurityDescriptor: descriptor,
		},
		supervisor: supervisor,
		slots:      make(chan struct{}, maxServiceConnections),
		closed:     make(chan struct{}),
	}
	// The first instance fails if anyone else already serves this name.
	first, err := server.instance(true)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", server.path, err)
	}
	go server.serve(first)
	return server, nil
}

func (s *pipeServer) close() {
	s.closeOnce.Do(func() { close(s.closed) })
}

func (s *pipeServer) instance(first bool) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(s.path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	flags := uint32(windows.PIPE_ACCESS_DUPLEX)
	if first {
		flags |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
	}
	return windows.CreateNamedPipe(
		name,
		flags,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		windows.PIPE_UNLIMITED_INSTANCES,
		pipeBufferSize,
		pipeBufferSize,
		0,
		s.security,
	)
}

func (s *pipeServer) serve(handle windows.Handle) {
	for {
		s.slots <- struct{}{}
		err := windows.ConnectNamedPipe(handle, nil)
		select {
		case <-s.closed:
			windows.CloseHandle(handle)
			return
		default:
		}
		if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			windows.CloseHandle(handle)
			<-s.slots
		} else {
			go func(connected windows.Handle) {
				defer func() { <-s.slots }()
				s.handle(connected)
			}(handle)
		}
		for {
			handle, err = s.instance(false)
			if err == nil {
				break
			}
			time.Sleep(time.Second)
		}
	}
}

func (s *pipeServer) handle(handle windows.Handle) {
	pipe := os.NewFile(uintptr(handle), s.path)
	defer func() {
		windows.FlushFileBuffers(handle)
		windows.DisconnectNamedPipe(handle)
		pipe.Close()
	}()
	// A client that never finishes its request is disconnected, which also
	// ends the blocked read.
	timer := time.AfterFunc(serviceRequestTimeout, func() { windows.DisconnectNamedPipe(handle) })
	var request serviceRequest
	err := readServiceMessage(pipe, &request)
	timer.Stop()
	if err != nil {
		return
	}
	response := s.respond(handle, request)
	_ = writeServiceMessage(pipe, response)
}

func (s *pipeServer) respond(handle windows.Handle, request serviceRequest) serviceResponse {
	if err := s.checkClientImage(handle); err != nil {
		return serviceResponse{Error: err.Error()}
	}
	var assets []*os.File
	defer func() {
		for _, file := range assets {
			file.Close()
		}
	}()
	client, err := s.impersonate(handle, func() error {
		if request.Command != serviceCommandStart {
			return nil
		}
		var openErr error
		assets, openErr = openClientAssets(request.Assets)
		return openErr
	})
	if err != nil {
		return serviceResponse{Error: err.Error()}
	}
	switch request.Command {
	case serviceCommandStart:
		return s.supervisor.start(request, client, assets)
	case serviceCommandStop:
		return s.supervisor.stop(client)
	case serviceCommandStatus:
		return s.supervisor.status()
	case serviceCommandWatch:
		return s.supervisor.watch(request.Known, serviceWatchTimeout)
	default:
		return serviceResponse{Error: fmt.Sprintf("unknown command %q", request.Command)}
	}
}

// checkClientImage accepts only the App installed beside this Core. This
// keeps other programs away from the Core, but it is no boundary against code
// already running as the same user, which can control the App itself.
func (s *pipeServer) checkClientImage(handle windows.Handle) error {
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(handle, &pid); err != nil {
		return err
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return err
	}
	image := windows.UTF16ToString(buffer[:size])
	if !strings.EqualFold(filepath.Clean(image), filepath.Clean(s.clientPath)) {
		return errors.New("only the installed App may control the Core service")
	}
	return nil
}

// impersonate runs work with the client's identity, so files it opens are
// limited to what that user may read, and reports who the client is.
func (s *pipeServer) impersonate(handle windows.Handle, work func() error) (clientInfo, error) {
	runtime.LockOSThread()
	if result, _, err := procImpersonateNamedPipeClient.Call(uintptr(handle)); result == 0 {
		runtime.UnlockOSThread()
		return clientInfo{}, fmt.Errorf("identify the App user: %w", err)
	}
	client, err := currentClient()
	if err == nil {
		err = work()
	}
	if revertErr := windows.RevertToSelf(); revertErr != nil {
		// Never reuse a thread that may still carry the client's identity:
		// a goroutine that exits while locked takes its thread with it.
		return clientInfo{}, revertErr
	}
	runtime.UnlockOSThread()
	return client, err
}

func currentClient() (clientInfo, error) {
	var token windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &token); err != nil {
		return clientInfo{}, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return clientInfo{}, err
	}
	sid, err := user.User.Sid.Copy()
	if err != nil {
		return clientInfo{}, err
	}
	var session uint32
	var size uint32
	if err := windows.GetTokenInformation(token, windows.TokenSessionId, (*byte)(unsafe.Pointer(&session)), uint32(unsafe.Sizeof(session)), &size); err != nil {
		return clientInfo{}, err
	}
	return clientInfo{session: session, elevated: token.IsElevated(), sid: sid}, nil
}

const (
	maxServiceAssets     = 64
	maxServiceAssetBytes = 256 << 20
)

// openClientAssets opens the App's Geodata files while impersonating the App
// user: a link to a file that user cannot read fails here instead of being
// read as SYSTEM.
func openClientAssets(directory string) ([]*os.File, error) {
	if directory == "" || !filepath.IsAbs(directory) {
		return nil, errors.New("assets must be an absolute directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read assets: %w", err)
	}
	var files []*os.File
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.EqualFold(filepath.Ext(entry.Name()), ".dat") {
			continue
		}
		if len(files) == maxServiceAssets {
			closeFiles(files)
			return nil, errors.New("too many asset files")
		}
		file, err := os.Open(filepath.Join(directory, entry.Name()))
		if err != nil {
			closeFiles(files)
			return nil, fmt.Errorf("open asset: %w", err)
		}
		files = append(files, file)
	}
	return files, nil
}

func closeFiles(files []*os.File) {
	for _, file := range files {
		file.Close()
	}
}
