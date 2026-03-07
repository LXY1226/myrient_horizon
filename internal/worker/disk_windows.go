package worker

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// GetDiskFreeGB returns the free disk space in GB for the given path.
// Server alignment: Shared utility function for disk monitoring.
func GetDiskFreeGB(path string) float64 {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return 0
	}

	var freeBytes uint64
	pathPtr, _ := windows.UTF16PtrFromString(filepath.VolumeName(absPath) + "\\")
	err = windows.GetDiskFreeSpaceEx(pathPtr, nil, nil, &freeBytes)
	if err != nil {
		return 0
	}
	return float64(freeBytes) / (1024 * 1024 * 1024)
}
