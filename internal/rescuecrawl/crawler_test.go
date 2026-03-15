package rescuecrawl

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	mt "myrient-horizon/pkg/myrienttree"
)

func TestCrawlerSyncUsesHEADOnlyForNewOrMismatchedFiles(t *testing.T) {
	var (
		mu         sync.Mutex
		headCounts = make(map[string]int)
	)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/files/":
			fmt.Fprint(w, listingHTML([]listingRow{
				{name: "Parent directory/", href: "../", size: "-", date: "-"},
				{name: "./", href: "./", size: "-", date: "01-Jan-2026 00:00"},
				{name: "same.bin", href: "same.bin", size: "1.0 MiB", date: "01-Jan-2026 00:00"},
				{name: "display.bin", href: "display.bin", size: "1.4 KiB", date: "01-Jan-2026 00:00"},
				{name: "changed.bin", href: "changed.bin", size: "2.0 KiB", date: "01-Jan-2026 00:00"},
				{name: "new.bin", href: "new.bin", size: "3.0 KiB", date: "01-Jan-2026 00:00"},
				{name: "sub/", href: "sub/", size: "-", date: "01-Jan-2026 00:00"},
			}))
		case r.Method == http.MethodGet && r.URL.Path == "/files/sub/":
			fmt.Fprint(w, listingHTML([]listingRow{
				{name: "Parent directory/", href: "../", size: "-", date: "-"},
			}))
		case r.Method == http.MethodHead:
			mu.Lock()
			headCounts[r.URL.Path]++
			mu.Unlock()

			switch r.URL.Path {
			case "/files/changed.bin":
				w.Header().Set("Content-Length", "2048")
			case "/files/display.bin":
				w.Header().Set("Content-Length", "1536")
			case "/files/new.bin":
				w.Header().Set("Content-Length", "3072")
			case "/files/same.bin":
				w.Header().Set("Content-Length", "1048576")
			default:
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	base := &mt.Tree[struct{}, struct{}]{
		Dirs: []mt.DirNode[struct{}]{
			{Name: "/", ID: 0, ParentIdx: -1, Path: "/", SubDirs: []int32{1}},
			{Name: "sub", ID: 1, ParentIdx: 0, Path: "/sub/"},
		},
		Files: []mt.FileNode[struct{}]{
			{Name: "same.bin", Size: 1048576, DirIdx: 0},
			{Name: "display.bin", Size: 1536, DirIdx: 0},
			{Name: "changed.bin", Size: 1500, DirIdx: 0},
			{Name: "missing.bin", Size: 99, DirIdx: 0},
		},
	}

	root := NewTreeFromBase(base)
	crawler, err := NewCrawler(CrawlConfig{
		BaseURL: NormalizeDirURL(srv.URL + "/files/"),
		Client:  srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewCrawler() error = %v", err)
	}

	report, err := crawler.Sync(context.Background(), root)
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	if report.GetRequests != 2 {
		t.Fatalf("GetRequests = %d, want 2", report.GetRequests)
	}
	if report.HeadRequests != 3 {
		t.Fatalf("HeadRequests = %d, want 3", report.HeadRequests)
	}

	mu.Lock()
	defer mu.Unlock()
	if headCounts["/files/same.bin"] != 0 {
		t.Fatalf("HEAD same.bin count = %d, want 0", headCounts["/files/same.bin"])
	}
	if headCounts["/files/display.bin"] != 1 {
		t.Fatalf("HEAD display.bin count = %d, want 1", headCounts["/files/display.bin"])
	}
	if headCounts["/files/changed.bin"] != 1 {
		t.Fatalf("HEAD changed.bin count = %d, want 1", headCounts["/files/changed.bin"])
	}
	if headCounts["/files/new.bin"] != 1 {
		t.Fatalf("HEAD new.bin count = %d, want 1", headCounts["/files/new.bin"])
	}

	if got := root.fileByName["changed.bin"].Size; got != 2048 {
		t.Fatalf("changed.bin size = %d, want 2048", got)
	}
	if got := root.fileByName["new.bin"].Size; got != 3072 {
		t.Fatalf("new.bin size = %d, want 3072", got)
	}

	if len(report.AddedFiles) != 1 || report.AddedFiles[0].Path != "/new.bin" {
		t.Fatalf("AddedFiles = %+v, want /new.bin", report.AddedFiles)
	}
	if len(report.UpdatedFiles) != 1 || report.UpdatedFiles[0].Path != "/changed.bin" {
		t.Fatalf("UpdatedFiles = %+v, want /changed.bin", report.UpdatedFiles)
	}
	if len(report.DisplayMismatches) != 2 {
		t.Fatalf("DisplayMismatches = %+v, want 2 items", report.DisplayMismatches)
	}
	if len(report.MissingFiles) != 1 || report.MissingFiles[0].Path != "/missing.bin" {
		t.Fatalf("MissingFiles = %+v, want /missing.bin", report.MissingFiles)
	}
	if len(report.EmptyDirs) != 1 || report.EmptyDirs[0] != "/sub/" {
		t.Fatalf("EmptyDirs = %+v, want /sub/", report.EmptyDirs)
	}
}

func TestParseDirectoryListingTable(t *testing.T) {
	baseURL := mustParseURL(t, "https://myrient.erista.me/files/demo/")
	body := listingHTML([]listingRow{
		{name: "Parent directory/", href: "../", size: "-", date: "-"},
		{name: "./", href: "./", size: "-", date: "01-Jan-2026 00:00"},
		{name: "sub dir/", href: "sub%20dir/", size: "-", date: "01-Jan-2026 00:00"},
		{name: "hello.bin", href: "hello.bin", size: "62.9 KiB", date: "01-Jan-2026 00:00"},
	})

	entries, err := parseDirectoryListing(baseURL, body)
	if err != nil {
		t.Fatalf("parseDirectoryListing() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if !entries[0].IsDir || entries[0].Name != "sub dir" {
		t.Fatalf("dir entry = %+v, want sub dir/", entries[0])
	}
	if entries[1].IsDir || entries[1].Name != "hello.bin" {
		t.Fatalf("file entry = %+v, want hello.bin", entries[1])
	}
	if entries[1].RoughSize.Raw != "62.9 KiB" {
		t.Fatalf("rough size raw = %q, want 62.9 KiB", entries[1].RoughSize.Raw)
	}
}

type listingRow struct {
	name string
	href string
	size string
	date string
}

func listingHTML(rows []listingRow) string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><body><table id="list"><tbody>`)
	for _, row := range rows {
		fmt.Fprintf(
			&b,
			`<tr><td class="link"><a href="%s">%s</a></td><td class="size">%s</td><td class="date">%s</td></tr>`,
			row.href,
			row.name,
			row.size,
			row.date,
		)
	}
	b.WriteString(`</tbody></table></body></html>`)
	return b.String()
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", raw, err)
	}
	return u
}
