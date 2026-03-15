package rescuecrawl

import (
	"context"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	pathpkg "path"
	"regexp"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
	mt "myrient-horizon/pkg/myrienttree"
)

var (
	listTablePattern = regexp.MustCompile(`(?is)<table\s+id="list"[^>]*>.*?<tbody>(.*?)</tbody>`)
	listRowPattern   = regexp.MustCompile(`(?is)<tr>\s*<td class="link"><a href="([^"]+)"[^>]*>(.*?)</a></td>\s*<td class="size">([^<]*)</td>\s*<td class="date">([^<]*)</td>\s*</tr>`)
	sizePattern      = regexp.MustCompile(`^\s*([0-9]+(?:\.[0-9]+)?)\s*([KMGTPE]?i?B|B)\s*$`)
)

type Dir struct {
	Name  string
	Path  string
	Dirs  []*Dir
	Files []*File

	dirByName  map[string]*Dir
	fileByName map[string]*File
}

type File struct {
	Name string
	Size int64
}

type RemoteEntry struct {
	Name      string
	IsDir     bool
	RoughSize RoughSize
	URL       *url.URL
}

type RoughSize struct {
	Raw string
}

type CrawlConfig struct {
	BaseURL     string
	UserAgent   string
	Client      *http.Client
	Concurrency int
}

type Report struct {
	AddedDirs         []string
	AddedFiles        []AddedFile
	UpdatedFiles      []UpdatedFile
	DisplayMismatches []DisplayMismatch
	MissingDirs       []string
	MissingFiles      []MissingFile
	Conflicts         []string
	EmptyDirs         []string
	GetRequests       int
	HeadRequests      int
}

type AddedFile struct {
	Path string
	Size int64
}

type UpdatedFile struct {
	Path    string
	OldSize int64
	NewSize int64
}

type DisplayMismatch struct {
	Path      string
	Expected  string
	Actual    string
	ExactSize int64
}

type MissingFile struct {
	Path string
	Size int64
}

type Crawler struct {
	baseURL     *url.URL
	baseURLPath string
	client      *http.Client
	userAgent   string
	concurrency int
	sem         chan struct{}
	reportMu    sync.Mutex
	report      *Report
}

func NewTreeFromBase[DirExt any, FileExt any](base *mt.Tree[DirExt, FileExt]) *Dir {
	dirs := make([]*Dir, len(base.Dirs))
	for i := range base.Dirs {
		dirs[i] = &Dir{
			Name:       base.Dirs[i].Name,
			Path:       base.Dirs[i].Path,
			dirByName:  make(map[string]*Dir),
			fileByName: make(map[string]*File),
		}
	}

	for i := range base.Dirs {
		dir := dirs[i]
		for _, childID := range base.Dirs[i].SubDirs {
			child := dirs[childID]
			dir.Dirs = append(dir.Dirs, child)
			dir.dirByName[child.Name] = child
		}
	}

	for i := range base.Files {
		dir := dirs[base.Files[i].DirIdx]
		file := &File{Name: base.Files[i].Name, Size: base.Files[i].Size}
		dir.Files = append(dir.Files, file)
		dir.fileByName[file.Name] = file
	}

	return dirs[0]
}

func (d *Dir) ToSerializedNode() *mt.SerializedNode {
	children := make([]*mt.SerializedNode, 0, len(d.Files)+len(d.Dirs))
	for _, file := range d.Files {
		children = append(children, &mt.SerializedNode{
			Name: file.Name,
			Size: file.Size,
		})
	}
	for _, child := range d.Dirs {
		children = append(children, child.ToSerializedNode())
	}

	return &mt.SerializedNode{
		Name:     d.Name,
		Size:     mt.EmptyDirectorySize,
		Children: children,
	}
}

func NewCrawler(cfg CrawlConfig) (*Crawler, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("crawler: base URL is required")
	}

	baseURL, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("crawler: parse base URL: %w", err)
	}
	if !strings.HasSuffix(baseURL.Path, "/") {
		baseURL.Path += "/"
	}

	client := cfg.Client
	if client == nil {
		client = &http.Client{}
	}

	userAgent := strings.TrimSpace(cfg.UserAgent)
	if userAgent == "" {
		userAgent = "myrient-horizon-rescue-crawler/1.0"
	}
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 8
	}

	return &Crawler{
		baseURL:     baseURL,
		baseURLPath: baseURL.Path,
		client:      client,
		userAgent:   userAgent,
		concurrency: concurrency,
		sem:         make(chan struct{}, concurrency),
		report:      &Report{},
	}, nil
}

func (c *Crawler) Sync(ctx context.Context, root *Dir) (*Report, error) {
	if root == nil {
		return nil, fmt.Errorf("crawler: nil root tree")
	}
	if root.Path != "/" {
		return nil, fmt.Errorf("crawler: expected root path \"/\", got %q", root.Path)
	}
	if err := c.syncDir(ctx, root, c.baseURL); err != nil {
		return nil, err
	}

	sort.Strings(c.report.AddedDirs)
	sort.Strings(c.report.MissingDirs)
	sort.Strings(c.report.Conflicts)
	sort.Strings(c.report.EmptyDirs)
	sort.Slice(c.report.AddedFiles, func(i, j int) bool { return c.report.AddedFiles[i].Path < c.report.AddedFiles[j].Path })
	sort.Slice(c.report.DisplayMismatches, func(i, j int) bool { return c.report.DisplayMismatches[i].Path < c.report.DisplayMismatches[j].Path })
	sort.Slice(c.report.UpdatedFiles, func(i, j int) bool { return c.report.UpdatedFiles[i].Path < c.report.UpdatedFiles[j].Path })
	sort.Slice(c.report.MissingFiles, func(i, j int) bool { return c.report.MissingFiles[i].Path < c.report.MissingFiles[j].Path })

	return c.report, nil
}

func (c *Crawler) syncDir(ctx context.Context, dir *Dir, dirURL *url.URL) error {
	entries, err := c.fetchDirectory(ctx, dirURL)
	if err != nil {
		return fmt.Errorf("crawler: fetch %s: %w", dir.Path, err)
	}
	if len(entries) == 0 && len(dir.Dirs) == 0 && len(dir.Files) == 0 {
		c.appendEmptyDir(dir.Path)
	}

	seenDirs := make(map[string]struct{}, len(entries))
	seenFiles := make(map[string]struct{}, len(entries))

	var childSync []func() error
	for _, entry := range entries {
		if entry.IsDir {
			childPath, err := c.treeDirPath(entry.URL.Path)
			if err != nil {
				return err
			}
			name := strings.TrimSuffix(entry.Name, "/")
			if name == "" {
				continue
			}
			if _, exists := dir.fileByName[name]; exists {
				c.appendConflict(fmt.Sprintf("%s%s: remote directory conflicts with local file", dir.Path, name))
				continue
			}

			child, exists := dir.dirByName[name]
			if !exists {
				child = &Dir{
					Name:       name,
					Path:       childPath,
					dirByName:  make(map[string]*Dir),
					fileByName: make(map[string]*File),
				}
				dir.Dirs = append(dir.Dirs, child)
				dir.dirByName[name] = child
				c.appendAddedDir(child.Path)
			}
			seenDirs[name] = struct{}{}
			childDir := child
			childURL := entry.URL
			childSync = append(childSync, func() error { return c.syncDir(ctx, childDir, childURL) })
			continue
		}

		if _, exists := dir.dirByName[entry.Name]; exists {
			c.appendConflict(fmt.Sprintf("%s%s: remote file conflicts with local directory", dir.Path, entry.Name))
			continue
		}

		seenFiles[entry.Name] = struct{}{}
		local, exists := dir.fileByName[entry.Name]
		filePath := dir.Path + entry.Name
		if !exists {
			size, err := c.fetchExactSize(ctx, entry.URL)
			if err != nil {
				return err
			}
			file := &File{Name: entry.Name, Size: size}
			dir.Files = append(dir.Files, file)
			dir.fileByName[file.Name] = file
			c.appendAddedFile(AddedFile{Path: filePath, Size: size})
			continue
		}

		expectedDisplay := FormatRoughSize(local.Size)
		if entry.RoughSize.Raw == expectedDisplay {
			continue
		}

		size, err := c.fetchExactSize(ctx, entry.URL)
		if err != nil {
			return err
		}
		c.appendDisplayMismatch(DisplayMismatch{
			Path:      filePath,
			Expected:  expectedDisplay,
			Actual:    entry.RoughSize.Raw,
			ExactSize: size,
		})
		if size != local.Size {
			c.appendUpdatedFile(UpdatedFile{
				Path:    filePath,
				OldSize: local.Size,
				NewSize: size,
			})
			local.Size = size
		}
	}

	for _, child := range dir.Dirs {
		if _, ok := seenDirs[child.Name]; !ok {
			c.appendMissingDir(child.Path)
		}
	}
	for _, file := range dir.Files {
		if _, ok := seenFiles[file.Name]; !ok {
			c.appendMissingFile(MissingFile{
				Path: dir.Path + file.Name,
				Size: file.Size,
			})
		}
	}

	if len(childSync) > 0 {
		g, _ := errgroup.WithContext(ctx)
		for _, fn := range childSync {
			fn := fn
			g.Go(func() error { return fn() })
		}
		if err := g.Wait(); err != nil {
			return fmt.Errorf("crawler: sync child dirs: %w", err)
		}
	}

	return nil
}

func (c *Crawler) fetchDirectory(ctx context.Context, dirURL *url.URL) ([]RemoteEntry, error) {
	if err := c.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.release()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dirURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create GET request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", dirURL, err)
	}
	defer resp.Body.Close()

	c.incrementGetRequests()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %s", dirURL, resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read GET body: %w", err)
	}

	entries, err := parseDirectoryListing(dirURL, string(body))
	if err != nil {
		return nil, err
	}

	log.Printf("crawler: scanned %s: %d entries", dirURL, len(entries))
	return entries, nil
}

func (c *Crawler) fetchExactSize(ctx context.Context, fileURL *url.URL) (int64, error) {
	if err := c.acquire(ctx); err != nil {
		return 0, err
	}
	defer c.release()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, fileURL.String(), nil)
	if err != nil {
		return 0, fmt.Errorf("create HEAD request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("HEAD %s: %w", fileURL, err)
	}
	defer resp.Body.Close()

	c.incrementHeadRequests()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HEAD %s: status %s", fileURL, resp.Status)
	}
	if resp.ContentLength < 0 {
		return 0, fmt.Errorf("HEAD %s: missing Content-Length", fileURL)
	}
	return resp.ContentLength, nil
}

func (c *Crawler) treeDirPath(urlPath string) (string, error) {
	if !strings.HasPrefix(urlPath, c.baseURLPath) {
		return "", fmt.Errorf("crawler: path %q is outside base path %q", urlPath, c.baseURLPath)
	}

	rel := strings.TrimPrefix(urlPath, c.baseURLPath)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return "/", nil
	}
	if !strings.HasSuffix(rel, "/") {
		rel += "/"
	}
	return "/" + rel, nil
}

func parseDirectoryListing(baseURL *url.URL, body string) ([]RemoteEntry, error) {
	match := listTablePattern.FindStringSubmatch(body)
	if len(match) != 2 {
		return nil, fmt.Errorf("parse directory listing: listing table not found")
	}

	rowMatches := listRowPattern.FindAllStringSubmatch(match[1], -1)
	if len(rowMatches) == 0 {
		return nil, fmt.Errorf("parse directory listing: no rows found")
	}

	entries := make([]RemoteEntry, 0, len(rowMatches))
	for _, row := range rowMatches {
		href := html.UnescapeString(strings.TrimSpace(row[1]))
		name := strings.TrimSpace(html.UnescapeString(stripTags(row[2])))
		sizeText := strings.TrimSpace(html.UnescapeString(stripTags(row[3])))

		if href == "" || href == "./" || href == "../" || strings.HasPrefix(href, "?") {
			continue
		}

		target, err := baseURL.Parse(href)
		if err != nil {
			return nil, fmt.Errorf("parse directory listing href %q: %w", href, err)
		}

		isDir := strings.HasSuffix(target.Path, "/") || strings.HasSuffix(name, "/")
		if isDir {
			name = strings.TrimSuffix(name, "/")
			if name == "" || name == "." || name == ".." || strings.EqualFold(name, "Parent directory") {
				continue
			}
			entries = append(entries, RemoteEntry{
				Name:  name,
				IsDir: true,
				URL:   target,
			})
			continue
		}

		rough, err := ParseRoughSize(sizeText)
		if err != nil {
			return nil, fmt.Errorf("parse size for %q: %w", name, err)
		}

		entries = append(entries, RemoteEntry{
			Name:      name,
			IsDir:     false,
			RoughSize: rough,
			URL:       target,
		})
	}

	return entries, nil
}

func ParseRoughSize(raw string) (RoughSize, error) {
	raw = strings.TrimSpace(raw)
	match := sizePattern.FindStringSubmatch(raw)
	if len(match) != 3 {
		return RoughSize{}, fmt.Errorf("invalid rough size %q", raw)
	}

	valueText := match[1]
	unitText := match[2]

	return RoughSize{Raw: fmt.Sprintf("%s %s", valueText, unitText)}, nil
}

func stripTags(s string) string {
	for {
		start := strings.IndexByte(s, '<')
		if start < 0 {
			return s
		}
		end := strings.IndexByte(s[start:], '>')
		if end < 0 {
			return s
		}
		s = s[:start] + s[start+end+1:]
	}
}

func FilePath[DirExt any, FileExt any](tree *mt.Tree[DirExt, FileExt], fileID int32) string {
	file := tree.Files[fileID]
	return tree.Dirs[file.DirIdx].Path + file.Name
}

func NormalizeDirURL(raw string) string {
	if strings.HasSuffix(raw, "/") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Path = pathpkg.Clean(u.Path) + "/"
	return u.String()
}

func FormatRoughSize(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}

	type unit struct {
		name string
		size float64
	}
	units := []unit{
		{name: "EiB", size: 1024 * 1024 * 1024 * 1024 * 1024 * 1024},
		{name: "PiB", size: 1024 * 1024 * 1024 * 1024 * 1024},
		{name: "TiB", size: 1024 * 1024 * 1024 * 1024},
		{name: "GiB", size: 1024 * 1024 * 1024},
		{name: "MiB", size: 1024 * 1024},
		{name: "KiB", size: 1024},
	}
	value := float64(size)
	for _, unit := range units {
		if value >= unit.size {
			return fmt.Sprintf("%.1f %s", value/unit.size, unit.name)
		}
	}
	return fmt.Sprintf("%d B", size)
}

func (c *Crawler) acquire(ctx context.Context) error {
	select {
	case c.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Crawler) release() {
	<-c.sem
}

func (c *Crawler) incrementGetRequests() {
	c.reportMu.Lock()
	defer c.reportMu.Unlock()
	c.report.GetRequests++
}

func (c *Crawler) incrementHeadRequests() {
	c.reportMu.Lock()
	defer c.reportMu.Unlock()
	c.report.HeadRequests++
}

func (c *Crawler) appendAddedDir(path string) {
	c.reportMu.Lock()
	defer c.reportMu.Unlock()
	c.report.AddedDirs = append(c.report.AddedDirs, path)
}

func (c *Crawler) appendAddedFile(item AddedFile) {
	c.reportMu.Lock()
	defer c.reportMu.Unlock()
	c.report.AddedFiles = append(c.report.AddedFiles, item)
}

func (c *Crawler) appendUpdatedFile(item UpdatedFile) {
	c.reportMu.Lock()
	defer c.reportMu.Unlock()
	c.report.UpdatedFiles = append(c.report.UpdatedFiles, item)
}

func (c *Crawler) appendDisplayMismatch(item DisplayMismatch) {
	c.reportMu.Lock()
	defer c.reportMu.Unlock()
	c.report.DisplayMismatches = append(c.report.DisplayMismatches, item)
}

func (c *Crawler) appendMissingDir(path string) {
	c.reportMu.Lock()
	defer c.reportMu.Unlock()
	c.report.MissingDirs = append(c.report.MissingDirs, path)
}

func (c *Crawler) appendMissingFile(item MissingFile) {
	c.reportMu.Lock()
	defer c.reportMu.Unlock()
	c.report.MissingFiles = append(c.report.MissingFiles, item)
}

func (c *Crawler) appendConflict(item string) {
	c.reportMu.Lock()
	defer c.reportMu.Unlock()
	c.report.Conflicts = append(c.report.Conflicts, item)
}

func (c *Crawler) appendEmptyDir(path string) {
	c.reportMu.Lock()
	defer c.reportMu.Unlock()
	c.report.EmptyDirs = append(c.report.EmptyDirs, path)
}
