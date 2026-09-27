//go:build windows || (linux && !android)

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

type runOptions struct {
	dns           string
	interfaceName string
	configPath    string
	configSHA256  string
	errorFile     string
}

func parseRunOptions(args []string) (runOptions, error) {
	var options runOptions
	if len(args) == 0 || args[0] != "run" {
		return options, errors.New("expected run command")
	}

	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.dns, "dns", "", "DNS server IP endpoint")
	flags.StringVar(&options.interfaceName, "interface", "", "outbound network interface")
	flags.StringVar(&options.configPath, "config", "", "Xray JSON configuration path")
	flags.StringVar(&options.configSHA256, "config-sha256", "", "expected SHA-256 of the Xray JSON configuration")
	flags.StringVar(&options.errorFile, "error-file", "", "also write command errors to this file")
	if err := flags.Parse(args[1:]); err != nil {
		return options, err
	}
	if flags.NArg() != 0 {
		return options, errors.New("unexpected positional arguments")
	}
	if options.dns == "" || options.interfaceName == "" || options.configPath == "" {
		return options, errors.New("dns, interface, and config are required")
	}
	if options.configSHA256 != "" && !validSHA256(options.configSHA256) {
		return options, errors.New("config-sha256 must be 64 hexadecimal characters")
	}
	return options, nil
}

func execute(args []string, run func(runOptions) error, stdout, stderr io.Writer) int {
	usage := func() {
		fmt.Fprintln(stdout, "Usage: xray run -dns <IP:port> -interface <name> -config <xray.json> [-config-sha256 <hex>] [-error-file <path>]")
	}
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		usage()
		return 0
	}

	options, err := parseRunOptions(args)
	if errors.Is(err, flag.ErrHelp) {
		usage()
		return 0
	}
	if err == nil && options.errorFile != "" {
		if err := checkWriteTarget(options.errorFile); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		// Clear the previous failure before starting. Reuse a caller-created file
		// so an elevated process preserves the caller's read permissions.
		if err := os.WriteFile(options.errorFile, nil, 0600); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if err == nil {
		err = run(options)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		if options.errorFile != "" && checkWriteTarget(options.errorFile) == nil {
			if writeErr := os.WriteFile(options.errorFile, []byte(err.Error()), 0600); writeErr != nil {
				fmt.Fprintln(stderr, writeErr)
			}
		}
		return 1
	}
	return 0
}
