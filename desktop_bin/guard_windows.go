//go:build windows

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// checkWriteTarget refuses a file the elevated Core would write when the
// file or any folder above it is a symbolic link or junction, or when the file
// has other hard links. These paths are in folders the unprivileged App user
// controls; a link there would redirect an administrator's write to any file.
// Links created after this check are not detected.
func checkWriteTarget(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(absolute)
	current := volume + string(filepath.Separator)
	parts := strings.Split(strings.Trim(absolute[len(volume):], `\/`), string(filepath.Separator))
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		// Go reports symbolic links as ModeSymlink and junctions as ModeIrregular.
		if info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return fmt.Errorf("refusing to write through a link: %s", current)
		}
	}
	return checkSingleLink(absolute)
}

func checkSingleLink(path string) error {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := syscall.CreateFile(
		name,
		0,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_OPEN_REPARSE_POINT|syscall.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	defer syscall.CloseHandle(handle)
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(handle, &info); err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if info.NumberOfLinks > 1 {
		return fmt.Errorf("refusing to write a file with other hard links: %s", path)
	}
	return nil
}
