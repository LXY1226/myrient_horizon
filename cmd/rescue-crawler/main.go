package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"myrient-horizon/internal/rescuecrawl"
	mt "myrient-horizon/pkg/myrienttree"
)

func main() {
	treePath := flag.String("tree", filepath.Join("data", "full_tree.fbd"), "baseline flatbuffer tree")
	baseURL := flag.String("base-url", "https://myrient.erista.me/files/", "remote listing root to crawl")
	outDir := flag.String("out-dir", filepath.Join("data", "rescue-crawl"), "output directory for new tree and logs")
	timeout := flag.Duration("timeout", 30*time.Second, "per-request timeout")
	concurrency := flag.Int("concurrency", 8, "maximum concurrent HTTP requests")
	userAgent := flag.String("user-agent", "myrient-horizon-rescue-crawler/1.0", "HTTP User-Agent")
	flag.Parse()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("crawler: create output directory: %v", err)
	}

	log.Printf("crawler: loading baseline tree from %s", *treePath)
	oldTree, _, err := mt.LoadFromFile[struct{}, struct{}](*treePath)
	if err != nil {
		log.Fatalf("crawler: load baseline tree: %v", err)
	}
	log.Printf("crawler: baseline tree loaded: %d dirs, %d files", len(oldTree.Dirs), len(oldTree.Files))

	client := &http.Client{Timeout: *timeout}
	crawler, err := rescuecrawl.NewCrawler(rescuecrawl.CrawlConfig{
		BaseURL:     rescuecrawl.NormalizeDirURL(*baseURL),
		UserAgent:   *userAgent,
		Client:      client,
		Concurrency: *concurrency,
	})
	if err != nil {
		log.Fatalf("crawler: %v", err)
	}

	root := rescuecrawl.NewTreeFromBase(oldTree)
	report, err := crawler.Sync(context.Background(), root)
	if err != nil {
		log.Fatalf("crawler: sync failed: %v", err)
	}

	treeData, err := mt.MarshalNode(root.ToSerializedNode())
	if err != nil {
		log.Fatalf("crawler: marshal updated tree: %v", err)
	}

	newTreePath := filepath.Join(*outDir, "full_tree.updated.fbd")
	if err := os.WriteFile(newTreePath, treeData, 0o644); err != nil {
		log.Fatalf("crawler: write updated tree: %v", err)
	}

	newTree, err := mt.LoadFromBytes[struct{}, struct{}](treeData)
	if err != nil {
		log.Fatalf("crawler: reload updated tree: %v", err)
	}

	summary := rescuecrawl.BuildDiffSummary(oldTree, newTree, treeData)

	diffLogPath := filepath.Join(*outDir, "tree-diff.log")
	if err := rescuecrawl.WriteDiffLog(diffLogPath, report, summary); err != nil {
		log.Fatalf("crawler: write diff log: %v", err)
	}

	dirMapPath := filepath.Join(*outDir, "dir-id-map.tsv")
	if err := rescuecrawl.WriteDirRemapTSV(dirMapPath, summary.DirRemaps); err != nil {
		log.Fatalf("crawler: write dir remap TSV: %v", err)
	}

	fileMapPath := filepath.Join(*outDir, "file-id-map.tsv")
	if err := rescuecrawl.WriteFileRemapTSV(fileMapPath, summary.FileRemaps); err != nil {
		log.Fatalf("crawler: write file remap TSV: %v", err)
	}

	sqlPath := filepath.Join(*outDir, "migrate-tree-ids.sql")
	if err := rescuecrawl.WriteMigrationSQL(sqlPath, summary); err != nil {
		log.Fatalf("crawler: write migration SQL: %v", err)
	}

	log.Printf(
		"crawler: finished: new_tree=%s sha1=%s added_dirs=%d added_files=%d updated_files=%d dir_remaps=%d file_remaps=%d",
		newTreePath,
		summary.TreeSHA1,
		len(report.AddedDirs),
		len(report.AddedFiles),
		len(report.UpdatedFiles),
		len(summary.DirRemaps),
		len(summary.FileRemaps),
	)
	fmt.Printf("tree=%s\ndiff=%s\ndir_map=%s\nfile_map=%s\nsql=%s\n", newTreePath, diffLogPath, dirMapPath, fileMapPath, sqlPath)
}
