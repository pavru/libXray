//go:build windows

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// The Core service runs this executable as SYSTEM so that the unprivileged
// App can start and stop the Core without a UAC prompt on every connection.
// The installer registers it once:
//
//	OneXrayCore.exe service install -name <service> -display <name> -pipe <pipe> -client <App.exe>
//
// and the App then talks to \\.\pipe\<pipe>; see service_pipe_windows.go.
type serviceOptions struct {
	name    string
	display string
	pipe    string
	client  string
}

func serviceCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "expected service install, uninstall or run")
		return 1
	}
	options, err := parseServiceOptions(args[0], args[1:])
	if err == nil {
		switch args[0] {
		case "install":
			err = installService(options)
		case "uninstall":
			err = uninstallService(options.name)
		case "run":
			err = runService(options)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func parseServiceOptions(command string, args []string) (serviceOptions, error) {
	var options serviceOptions
	flags := flag.NewFlagSet("service "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.name, "name", "", "Windows service name")
	switch command {
	case "install":
		flags.StringVar(&options.display, "display", "", "Windows service display name")
		fallthrough
	case "run":
		flags.StringVar(&options.pipe, "pipe", "", "named pipe for App requests")
		flags.StringVar(&options.client, "client", "", "App executable beside this Core")
	case "uninstall":
	default:
		return options, fmt.Errorf("unknown service command %q", command)
	}
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if flags.NArg() != 0 {
		return options, errors.New("unexpected positional arguments")
	}
	if !validServiceName(options.name) {
		return options, errors.New("name must be letters, digits, '.', '-' or '_'")
	}
	if command == "uninstall" {
		return options, nil
	}
	if !validServiceName(options.pipe) {
		return options, errors.New("pipe must be letters, digits, '.', '-' or '_'")
	}
	if options.client == "" || filepath.Base(options.client) != options.client ||
		!strings.EqualFold(filepath.Ext(options.client), ".exe") {
		return options, errors.New("client must be an executable file name")
	}
	if command == "install" && options.display == "" {
		options.display = options.name
	}
	return options, nil
}

func validServiceName(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// serviceDataDirectory is inside SYSTEM's profile, which only SYSTEM and
// Administrators can modify, so no user can create it first or plant links
// in it. The App computes the same path to find the Core logs.
func serviceDataDirectory(name string) (string, error) {
	windowsDir, err := windows.GetSystemWindowsDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(windowsDir, "System32", "config", "systemprofile", "AppData", "Local", name), nil
}

func installService(options serviceOptions) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	manager, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	args := []string{"service", "run", "-name", options.name, "-pipe", options.pipe, "-client", options.client}
	config := mgr.Config{
		DisplayName:  options.display,
		Description:  "Starts and stops the VPN Core for " + strings.TrimSuffix(options.client, filepath.Ext(options.client)) + " without administrator prompts.",
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
		ServiceType:  windows.SERVICE_WIN32_OWN_PROCESS,
	}
	service, err := manager.OpenService(options.name)
	if err == nil {
		// An update: stop the old Core, then point the service at this one.
		if err := stopService(service); err != nil {
			service.Close()
			return err
		}
		current, err := service.Config()
		if err != nil {
			service.Close()
			return err
		}
		command := []string{syscall.EscapeArg(executable)}
		for _, arg := range args {
			command = append(command, syscall.EscapeArg(arg))
		}
		current.BinaryPathName = strings.Join(command, " ")
		current.DisplayName = config.DisplayName
		current.Description = config.Description
		current.StartType = config.StartType
		current.ErrorControl = config.ErrorControl
		current.ServiceType = config.ServiceType
		current.ServiceStartName = "LocalSystem"
		if err := service.UpdateConfig(current); err != nil {
			service.Close()
			return err
		}
	} else {
		service, err = manager.CreateService(options.name, executable, config, args...)
		if err != nil {
			return err
		}
	}
	defer service.Close()
	restart := mgr.RecoveryAction{Type: mgr.ServiceRestart, Delay: 5 * time.Second}
	if err := service.SetRecoveryActions([]mgr.RecoveryAction{restart, restart, restart}, 24*60*60); err != nil {
		return err
	}
	return service.Start()
}

func uninstallService(name string) error {
	manager, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(name)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}
	if err != nil {
		return err
	}
	defer service.Close()
	if err := stopService(service); err != nil {
		return err
	}
	if err := service.Delete(); err != nil {
		return err
	}
	if directory, err := serviceDataDirectory(name); err == nil {
		_ = os.RemoveAll(directory)
	}
	return nil
}

func stopService(service *mgr.Service) error {
	status, err := service.Query()
	if err != nil {
		return err
	}
	if status.State == svc.Stopped {
		return nil
	}
	if status.State != svc.StopPending {
		if _, err := service.Control(svc.Stop); err != nil &&
			!errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return err
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		status, err = service.Query()
		if err != nil {
			return err
		}
		if status.State == svc.Stopped {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("timed out waiting for the Core service to stop")
}

func runService(options serviceOptions) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !isService {
		return errors.New("service run is started by the Windows service manager")
	}
	return svc.Run(options.name, &coreService{options: options})
}

type coreService struct {
	options serviceOptions
}

func (s *coreService) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	supervisor, server, err := s.start()
	if err != nil {
		writeServiceStartError(s.options.name, err)
		return true, 1
	}
	defer server.close()
	accepts := svc.AcceptStop | svc.AcceptShutdown | svc.AcceptSessionChange
	status <- svc.Status{State: svc.Running, Accepts: accepts}
	for request := range requests {
		switch request.Cmd {
		case svc.Interrogate:
			status <- request.CurrentStatus
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending}
			supervisor.stopAll()
			return false, 0
		case svc.SessionChange:
			// EventData points to a WTSSESSION_NOTIFICATION owned by the
			// service manager; go vet cannot see that it is a pointer.
			if request.EventType == windows.WTS_SESSION_LOGOFF && request.EventData != 0 {
				notification := (*windows.WTSSESSION_NOTIFICATION)(unsafe.Pointer(request.EventData))
				go supervisor.sessionLoggedOff(notification.SessionID)
			}
		}
	}
	supervisor.stopAll()
	return false, 0
}

func (s *coreService) start() (*coreSupervisor, *pipeServer, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	root, err := serviceDataDirectory(s.options.name)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, nil, err
	}
	supervisor, err := newCoreSupervisor(executable, root)
	if err != nil {
		return nil, nil, err
	}
	server, err := listenPipe(
		s.options.pipe,
		filepath.Join(filepath.Dir(executable), s.options.client),
		supervisor,
	)
	if err != nil {
		supervisor.stopAll()
		return nil, nil, err
	}
	return supervisor, server, nil
}

// writeServiceStartError leaves the reason the service could not start where
// an administrator can find it; the service manager only records an exit code.
func writeServiceStartError(name string, err error) {
	if directory, dirErr := serviceDataDirectory(name); dirErr == nil {
		if os.MkdirAll(directory, 0700) == nil {
			_ = os.WriteFile(filepath.Join(directory, "service-error.txt"), []byte(err.Error()), 0600)
		}
	}
}
