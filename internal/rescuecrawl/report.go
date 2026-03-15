package rescuecrawl

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"sort"

	mt "myrient-horizon/pkg/myrienttree"
)

type DirRemap struct {
	Path  string
	OldID int32
	NewID int32
}

type FileRemap struct {
	Path  string
	OldID int32
	NewID int32
}

type DiffSummary struct {
	OldDirCount  int
	OldFileCount int
	NewDirCount  int
	NewFileCount int
	TreeSHA1     string
	DirRemaps    []DirRemap
	FileRemaps   []FileRemap
}

func TreeSHA1(data []byte) string {
	sum := sha1.Sum(data)
	return hex.EncodeToString(sum[:])
}

func BuildDiffSummary[DirExt any, FileExt any](oldTree, newTree *mt.Tree[DirExt, FileExt], treeData []byte) DiffSummary {
	return DiffSummary{
		OldDirCount:  len(oldTree.Dirs),
		OldFileCount: len(oldTree.Files),
		NewDirCount:  len(newTree.Dirs),
		NewFileCount: len(newTree.Files),
		TreeSHA1:     TreeSHA1(treeData),
		DirRemaps:    BuildDirRemaps(oldTree, newTree),
		FileRemaps:   BuildFileRemaps(oldTree, newTree),
	}
}

func BuildDirRemaps[DirExt any, FileExt any](oldTree, newTree *mt.Tree[DirExt, FileExt]) []DirRemap {
	newIDs := make(map[string]int32, len(newTree.Dirs))
	for _, dir := range newTree.Dirs {
		newIDs[dir.Path] = dir.ID
	}

	remaps := make([]DirRemap, 0)
	for _, dir := range oldTree.Dirs {
		newID, ok := newIDs[dir.Path]
		if !ok || newID == dir.ID {
			continue
		}
		remaps = append(remaps, DirRemap{Path: dir.Path, OldID: dir.ID, NewID: newID})
	}
	sort.Slice(remaps, func(i, j int) bool { return remaps[i].OldID < remaps[j].OldID })
	return remaps
}

func BuildFileRemaps[DirExt any, FileExt any](oldTree, newTree *mt.Tree[DirExt, FileExt]) []FileRemap {
	newIDs := make(map[string]int32, len(newTree.Files))
	for i := range newTree.Files {
		newIDs[FilePath(newTree, int32(i))] = int32(i)
	}

	remaps := make([]FileRemap, 0)
	for i := range oldTree.Files {
		path := FilePath(oldTree, int32(i))
		newID, ok := newIDs[path]
		if !ok || newID == int32(i) {
			continue
		}
		remaps = append(remaps, FileRemap{Path: path, OldID: int32(i), NewID: newID})
	}
	sort.Slice(remaps, func(i, j int) bool { return remaps[i].OldID < remaps[j].OldID })
	return remaps
}

func WriteDiffLog(path string, report *Report, summary DiffSummary) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("write diff log: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	defer w.Flush()

	fmt.Fprintf(w, "new_tree_sha1\t%s\n", summary.TreeSHA1)
	fmt.Fprintf(w, "old_dirs\t%d\n", summary.OldDirCount)
	fmt.Fprintf(w, "old_files\t%d\n", summary.OldFileCount)
	fmt.Fprintf(w, "new_dirs\t%d\n", summary.NewDirCount)
	fmt.Fprintf(w, "new_files\t%d\n", summary.NewFileCount)
	fmt.Fprintf(w, "get_requests\t%d\n", report.GetRequests)
	fmt.Fprintf(w, "head_requests\t%d\n", report.HeadRequests)
	fmt.Fprintf(w, "added_dirs\t%d\n", len(report.AddedDirs))
	fmt.Fprintf(w, "added_files\t%d\n", len(report.AddedFiles))
	fmt.Fprintf(w, "updated_files\t%d\n", len(report.UpdatedFiles))
	fmt.Fprintf(w, "display_mismatches\t%d\n", len(report.DisplayMismatches))
	fmt.Fprintf(w, "missing_dirs\t%d\n", len(report.MissingDirs))
	fmt.Fprintf(w, "missing_files\t%d\n", len(report.MissingFiles))
	fmt.Fprintf(w, "dir_remaps\t%d\n", len(summary.DirRemaps))
	fmt.Fprintf(w, "file_remaps\t%d\n", len(summary.FileRemaps))
	fmt.Fprintln(w)

	writeStringSection(w, "added_dirs", report.AddedDirs)
	writeAddedFilesSection(w, "added_files", report.AddedFiles)
	writeUpdatedFilesSection(w, "updated_files", report.UpdatedFiles)
	writeDisplayMismatchSection(w, "display_mismatches", report.DisplayMismatches)
	writeStringSection(w, "empty_dirs", report.EmptyDirs)
	writeStringSection(w, "missing_dirs", report.MissingDirs)
	writeMissingFilesSection(w, "missing_files", report.MissingFiles)
	writeStringSection(w, "conflicts", report.Conflicts)
	writeDirRemapSection(w, "dir_remaps", summary.DirRemaps)
	writeFileRemapSection(w, "file_remaps", summary.FileRemaps)

	return nil
}

func WriteDirRemapTSV(path string, remaps []DirRemap) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("write dir remap TSV: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	defer w.Flush()

	fmt.Fprintln(w, "old_id\tnew_id\tpath")
	for _, remap := range remaps {
		fmt.Fprintf(w, "%d\t%d\t%s\n", remap.OldID, remap.NewID, remap.Path)
	}
	return nil
}

func WriteFileRemapTSV(path string, remaps []FileRemap) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("write file remap TSV: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	defer w.Flush()

	fmt.Fprintln(w, "old_id\tnew_id\tpath")
	for _, remap := range remaps {
		fmt.Fprintf(w, "%d\t%d\t%s\n", remap.OldID, remap.NewID, remap.Path)
	}
	return nil
}

func WriteMigrationSQL(path string, summary DiffSummary) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("write migration SQL: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	defer w.Flush()

	fmt.Fprintln(w, "BEGIN;")
	fmt.Fprintln(w)
	writeDirMigrationSQL(w, summary.DirRemaps)
	fmt.Fprintln(w)
	writeFileMigrationSQL(w, summary.FileRemaps)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "COMMIT;")
	return nil
}

func writeDirMigrationSQL(w *bufio.Writer, remaps []DirRemap) {
	if len(remaps) == 0 {
		fmt.Fprintln(w, "-- No directory ID remaps.")
		return
	}

	fmt.Fprintln(w, "CREATE TEMP TABLE crawl_dir_map (old_id INT PRIMARY KEY, new_id INT NOT NULL);")
	fmt.Fprintln(w, "INSERT INTO crawl_dir_map (old_id, new_id) VALUES")
	for i, remap := range remaps {
		suffix := ","
		if i == len(remaps)-1 {
			suffix = ";"
		}
		fmt.Fprintf(w, "  (%d, %d)%s -- %s\n", remap.OldID, remap.NewID, suffix, remap.Path)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "UPDATE reclaims AS r")
	fmt.Fprintln(w, "SET dir_id = m.new_id")
	fmt.Fprintln(w, "FROM crawl_dir_map AS m")
	fmt.Fprintln(w, "WHERE r.dir_id = m.old_id")
	fmt.Fprintln(w, "  AND r.dir_id <> m.new_id;")
}

func writeFileMigrationSQL(w *bufio.Writer, remaps []FileRemap) {
	if len(remaps) == 0 {
		fmt.Fprintln(w, "-- No file ID remaps.")
		return
	}

	fmt.Fprintln(w, "CREATE TEMP TABLE crawl_file_map (old_id BIGINT PRIMARY KEY, new_id BIGINT NOT NULL);")
	fmt.Fprintln(w, "INSERT INTO crawl_file_map (old_id, new_id) VALUES")
	for i, remap := range remaps {
		suffix := ","
		if i == len(remaps)-1 {
			suffix = ";"
		}
		fmt.Fprintf(w, "  (%d, %d)%s -- %s\n", remap.OldID, remap.NewID, suffix, remap.Path)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "UPDATE item_status AS s")
	fmt.Fprintln(w, "SET file_id = m.new_id")
	fmt.Fprintln(w, "FROM crawl_file_map AS m")
	fmt.Fprintln(w, "WHERE s.file_id = m.old_id")
	fmt.Fprintln(w, "  AND s.file_id <> m.new_id;")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "UPDATE archive_status AS a")
	fmt.Fprintln(w, "SET file_id = m.new_id")
	fmt.Fprintln(w, "FROM crawl_file_map AS m")
	fmt.Fprintln(w, "WHERE a.file_id = m.old_id")
	fmt.Fprintln(w, "  AND a.file_id <> m.new_id;")
}

func writeStringSection(w *bufio.Writer, title string, items []string) {
	fmt.Fprintf(w, "[%s]\n", title)
	if len(items) == 0 {
		fmt.Fprintln(w, "(none)")
		fmt.Fprintln(w)
		return
	}
	for _, item := range items {
		fmt.Fprintln(w, item)
	}
	fmt.Fprintln(w)
}

func writeAddedFilesSection(w *bufio.Writer, title string, items []AddedFile) {
	fmt.Fprintf(w, "[%s]\n", title)
	if len(items) == 0 {
		fmt.Fprintln(w, "(none)")
		fmt.Fprintln(w)
		return
	}
	for _, item := range items {
		fmt.Fprintf(w, "%s\t%d\n", item.Path, item.Size)
	}
	fmt.Fprintln(w)
}

func writeUpdatedFilesSection(w *bufio.Writer, title string, items []UpdatedFile) {
	fmt.Fprintf(w, "[%s]\n", title)
	if len(items) == 0 {
		fmt.Fprintln(w, "(none)")
		fmt.Fprintln(w)
		return
	}
	for _, item := range items {
		fmt.Fprintf(w, "%s\t%d -> %d\n", item.Path, item.OldSize, item.NewSize)
	}
	fmt.Fprintln(w)
}

func writeDisplayMismatchSection(w *bufio.Writer, title string, items []DisplayMismatch) {
	fmt.Fprintf(w, "[%s]\n", title)
	if len(items) == 0 {
		fmt.Fprintln(w, "(none)")
		fmt.Fprintln(w)
		return
	}
	for _, item := range items {
		fmt.Fprintf(w, "%s\texpected=%s\tactual=%s\thead=%d\n", item.Path, item.Expected, item.Actual, item.ExactSize)
	}
	fmt.Fprintln(w)
}

func writeMissingFilesSection(w *bufio.Writer, title string, items []MissingFile) {
	fmt.Fprintf(w, "[%s]\n", title)
	if len(items) == 0 {
		fmt.Fprintln(w, "(none)")
		fmt.Fprintln(w)
		return
	}
	for _, item := range items {
		fmt.Fprintf(w, "%s\t%d\n", item.Path, item.Size)
	}
	fmt.Fprintln(w)
}

func writeDirRemapSection(w *bufio.Writer, title string, items []DirRemap) {
	fmt.Fprintf(w, "[%s]\n", title)
	if len(items) == 0 {
		fmt.Fprintln(w, "(none)")
		fmt.Fprintln(w)
		return
	}
	for _, item := range items {
		fmt.Fprintf(w, "%d -> %d\t%s\n", item.OldID, item.NewID, item.Path)
	}
	fmt.Fprintln(w)
}

func writeFileRemapSection(w *bufio.Writer, title string, items []FileRemap) {
	fmt.Fprintf(w, "[%s]\n", title)
	if len(items) == 0 {
		fmt.Fprintln(w, "(none)")
		fmt.Fprintln(w)
		return
	}
	for _, item := range items {
		fmt.Fprintf(w, "%d -> %d\t%s\n", item.OldID, item.NewID, item.Path)
	}
	fmt.Fprintln(w)
}
