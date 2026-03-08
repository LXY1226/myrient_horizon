package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"myrient-horizon/pkg/protocol"
)

// CleanOldBinary removes leftover .old files from a previous update.
// Should be called at the very start of main().
func CleanOldBinary() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	old := exe + ".old"
	if _, err := os.Stat(old); err == nil {
		if err := os.Remove(old); err != nil {
			log.Printf("updater: failed to remove old binary %s: %v", old, err)
		} else {
			log.Printf("updater: cleaned up old binary %s", old)
		}
	}
}

func doUpdate(resp *http.Response) error {
	var info protocol.UpdateRequiredResponse
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return fmt.Errorf("updater: decode upgrade response: %w", err)
	}
	switch info.Target {
	case "binary":
		return fmt.Errorf("updater: binary upgrade required: current=%q latest=%q", info.CurrentVersion, info.LatestVersion)
	case "tree":
		return fmt.Errorf("updater: tree upgrade required: current_sha1=%q latest_sha1=%q", info.CurrentTreeSHA1, info.LatestTreeSHA1)
	default:
		return fmt.Errorf("updater: unknown upgrade target %q", info.Target)
	}
}

// Apply downloads the new binary, verifies its SHA-256, replaces the
// current executable, and launches the new version. The caller should
// exit after Apply returns nil.
//
// On Windows the running exe cannot be deleted but CAN be renamed,
// so the sequence is:
//
//  1. Download → exe.new
//  2. Verify SHA-256
//  3. Rename exe → exe.old
//  4. Rename exe.new → exe
//  5. Start new exe (same args)
func Apply(info protocol.UpdateRequiredResponse) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("updater: resolve executable path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("updater: eval symlinks: %w", err)
	}

	newPath := exe + ".new"
	oldPath := exe + ".old"

	log.Printf("updater: downloading %s → %s", info.DownloadURL, newPath)

	// 1. Download.
	if err := download(info.DownloadURL, newPath); err != nil {
		os.Remove(newPath)
		return fmt.Errorf("updater: download: %w", err)
	}

	// 2. Verify SHA-256.
	if info.SHA256 != "" {
		hash, err := fileSHA256(newPath)
		if err != nil {
			os.Remove(newPath)
			return fmt.Errorf("updater: hash: %w", err)
		}
		if !strings.EqualFold(hash, info.SHA256) {
			os.Remove(newPath)
			return fmt.Errorf("updater: SHA-256 mismatch: got %s, want %s", hash, info.SHA256)
		}
		log.Printf("updater: SHA-256 verified: %s", hash)
	}

	// 3. Rename current → .old (remove stale .old first).
	os.Remove(oldPath)
	if err := os.Rename(exe, oldPath); err != nil {
		os.Remove(newPath)
		return fmt.Errorf("updater: rename current → old: %w", err)
	}

	// 4. Rename .new → current.
	if err := os.Rename(newPath, exe); err != nil {
		// Try to roll back.
		os.Rename(oldPath, exe)
		return fmt.Errorf("updater: rename new → current: %w", err)
	}

	// 5. Launch new binary with the same arguments.
	log.Printf("updater: launching new version %s", info.LatestVersion)
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Dir, _ = os.Getwd()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("updater: start new process: %w", err)
	}

	log.Printf("updater: new process started (pid %d), exiting current process", cmd.Process.Pid)
	return nil
}

// download fetches url and writes it to dst.
func download(url, dst string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	written, err := io.Copy(f, resp.Body)
	if err != nil {
		return err
	}
	log.Printf("updater: downloaded %d bytes", written)
	return nil
}

// fileSHA256 returns the lowercase hex-encoded SHA-256 digest of a file.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
