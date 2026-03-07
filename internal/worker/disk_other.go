//go:build !windows
// +build !windows

package worker

import "syscall"

// GetDiskFreeGB returns the free disk space in GB for the given path.
// Server alignment: Shared utility function for disk monitoring.
// Stub implementation for non-Windows platforms.
func GetDiskFreeGB(path string) float64 {
	var stat syscall.Statfs_t
	err := syscall.Statfs(path, &stat)
	if err != nil {
		return 0
	}
	// Calculate free space: free blocks * block size
	freeBytes := uint64(stat.Bfree) * uint64(stat.Bsize)
	return float64(freeBytes) / (1024 * 1024 * 1024)
}
