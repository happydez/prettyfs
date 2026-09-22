// Package pretty is a styled alternative to http.FileServer:
// HTML listings, zip downloads, private folders and rate limiting.
package pretty

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// pageCSP is set on listings and error pages.
const pageCSP = "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

const defaultFavicon = "data:image/svg+xml," +
	"%3Csvg%20xmlns='http://www.w3.org/2000/svg'%20viewBox='0%200%2064%2064'%3E" +
	"%3Crect%20width='64'%20height='64'%20rx='14'%20fill='%230b0b0b'/%3E" +
	"%3Ctext%20x='32'%20y='45'%20text-anchor='middle'%20fill='%23ffffff'" +
	"%20font-family='Helvetica,Arial,sans-serif'%20font-size='34'%20font-weight='700'%3E" +
	"fs%3C/text%3E%3C/svg%3E"

// Option configures FileServer.
type Option func(*config)

type config struct {
	title          string
	favicon        string
	privateMarker  string
	showHidden     bool
	serveIndex     bool
	followSymlinks bool
	sandbox        bool
	allowZip       bool
	zipMaxBytes    int64
	zipMaxFiles    int
	maxZips        int
	maxEntries     int
	cacheControl   string
	proxyHops      int
	browse         limits
	download       limits
	log            *slog.Logger
}

// WithTitle sets the listing page title.
func WithTitle(title string) Option {
	return func(c *config) {
		c.title = title
	}
}

// WithFavicon sets the listing favicon: a URL or a data: URI.
// An empty value removes the link.
func WithFavicon(v string) Option {
	return func(c *config) {
		c.favicon = v
	}
}

// WithPrivateMarker sets the private folder marker file name (default ".private").
// An empty name disables private folders.
func WithPrivateMarker(name string) Option {
	return func(c *config) {
		c.privateMarker = name
	}
}

// WithHiddenFiles shows and serves dotfiles. Disabled by default.
func WithHiddenFiles(show bool) Option {
	return func(c *config) {
		c.showHidden = show
	}
}

// WithIndexHTML serves index.html instead of a listing. Enabled by default.
func WithIndexHTML(serve bool) Option {
	return func(c *config) {
		c.serveIndex = serve
	}
}

// WithSymlinks follows symlinks inside the root. Disabled by default:
// a symlink can point into a private folder and bypass its marker.
func WithSymlinks(follow bool) Option {
	return func(c *config) {
		c.followSymlinks = follow
	}
}

// WithSandbox serves HTML, SVG and XML files with "CSP: sandbox", so their
// scripts can't act on your origin. Enabled by default.
func WithSandbox(enabled bool) Option {
	return func(c *config) {
		c.sandbox = enabled
	}
}

// WithZip allows downloading folders as .zip. Enabled by default.
func WithZip(enabled bool) Option {
	return func(c *config) {
		c.allowZip = enabled
	}
}

// WithZipLimit caps the total size and entry count of a zip
// (default 1 GiB, 10000 entries). Zero disables a bound.
func WithZipLimit(maxBytes int64, maxFiles int) Option {
	return func(c *config) {
		c.zipMaxBytes = maxBytes
		c.zipMaxFiles = maxFiles
	}
}

// WithMaxConcurrentZips caps zips built at once (default 4). Zero means no cap.
func WithMaxConcurrentZips(n int) Option {
	return func(c *config) {
		c.maxZips = n
	}
}

// WithMaxEntries caps rows shown in a listing (default 10000). Zero means no cap.
func WithMaxEntries(n int) Option {
	return func(c *config) {
		c.maxEntries = n
	}
}

// WithCacheControl sets Cache-Control for files (default "no-cache").
// An empty value omits the header.
func WithCacheControl(v string) Option {
	return func(c *config) {
		c.cacheControl = v
	}
}

// WithRateLimit limits all requests per IP.
func WithRateLimit(requests int, per time.Duration) Option {
	return func(c *config) {
		c.browse.perIP = nil
		if l := newWindowLimiter(requests, per); l != nil {
			c.browse.perIP = l
		}
	}
}

// WithDownloadRateLimit limits distinct files and zips per IP.
func WithDownloadRateLimit(requests int, per time.Duration) Option {
	return func(c *config) {
		c.download.perIP = nil
		if l := newWindowLimiter(requests, per); l != nil {
			c.download.perIP = l
		}
	}
}

// WithGlobalRateLimit limits all requests server-wide.
func WithGlobalRateLimit(requests int, per time.Duration) Option {
	return func(c *config) {
		c.browse.global = nil
		if l := newTokenBucket(requests, per); l != nil {
			c.browse.global = l
		}
	}
}

// WithGlobalDownloadRateLimit limits downloads server-wide.
func WithGlobalDownloadRateLimit(requests int, per time.Duration) Option {
	return func(c *config) {
		c.download.global = nil
		if l := newWindowLimiter(requests, per); l != nil {
			c.download.global = l
		}
	}
}

// WithGlobalLimiter sets a custom server-wide request limiter. nil disables it.
func WithGlobalLimiter(l Limiter) Option {
	return func(c *config) {
		c.browse.global = l
	}
}

// WithGlobalDownloadLimiter sets a custom server-wide download limiter. nil disables it.
func WithGlobalDownloadLimiter(l Limiter) Option {
	return func(c *config) {
		c.download.global = l
	}
}

// WithTrustedProxy takes the client IP from X-Forwarded-For, where hops is
// the number of your proxies in front of the server.
// Use only if clients can't reach the server directly.
func WithTrustedProxy(hops int) Option {
	return func(c *config) {
		c.proxyHops = hops
	}
}

// WithLogger sets the error logger (default slog.Default()).
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		if l != nil {
			c.log = l
		}
	}
}

// FileServer returns a handler that serves fsys, typically os.Root.FS().
// Mount it with http.StripPrefix on a pattern ending in "/".
func FileServer(fsys fs.FS, opts ...Option) http.Handler {
	if fsys == nil {
		panic("pretty: nil fs.FS")
	}

	cfg := config{
		title:         "Files",
		favicon:       defaultFavicon,
		privateMarker: ".private",
		serveIndex:    true,
		sandbox:       true,
		allowZip:      true,
		zipMaxBytes:   1 << 30,
		zipMaxFiles:   10000,
		maxZips:       4,
		maxEntries:    10000,
		cacheControl:  "no-cache",
		log:           slog.Default(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	h := &prettyFileHandler{fsys: fsys, cfg: cfg}
	if cfg.maxZips > 0 {
		h.zipSem = make(chan struct{}, cfg.maxZips)
	}

	return h
}

type prettyFileHandler struct {
	fsys   fs.FS
	cfg    config
	zipSem chan struct{}
}

func (h *prettyFileHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.allow(w, r, h.cfg.browse, "", "Too many requests from your IP") {
		return
	}

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		h.renderError(w, r, http.StatusMethodNotAllowed, "Only GET and HEAD are supported")
		return
	}

	if r.URL.Path == "" {
		orig, _, _ := strings.Cut(r.RequestURI, "?")
		localRedirect(w, r, path.Base(orig)+"/")
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/") {
		r.URL.Path = "/" + r.URL.Path
	}

	name, ok := sanitize(r.URL.Path)
	if !ok {
		h.renderError(w, r, http.StatusBadRequest, "Invalid path")
		return
	}
	if h.hiddenPath(name) || h.hasSymlink(name) {
		h.notFound(w, r)
		return
	}

	q := r.URL.Query()
	if h.cfg.serveIndex && strings.HasSuffix(r.URL.Path, "/index.html") && !q.Has("download") {
		localRedirect(w, r, "./")
		return
	}

	f, err := h.fsys.Open(toFS(name))
	if err != nil {
		h.openError(w, r, err)
		return
	}
	defer func() {
		_ = f.Close()
	}()

	fi, err := f.Stat()
	if err != nil {
		h.openError(w, r, err)
		return
	}

	dir := name
	if !fi.IsDir() {
		dir = path.Dir(name)
	}
	if h.isPrivate(dir) {
		h.notFound(w, r)
		return
	}

	if fi.IsDir() {
		if !strings.HasSuffix(r.URL.Path, "/") {
			localRedirect(w, r, path.Base(r.URL.Path)+"/")
			return
		}
		if q.Has("zip") && h.cfg.allowZip {
			h.serveZip(w, r, name)
			return
		}
		if h.cfg.serveIndex && h.serveIndex(w, r, name) {
			return
		}
		h.serveDir(w, r, f, name)
		return
	}

	if strings.HasSuffix(r.URL.Path, "/") {
		localRedirect(w, r, "../"+path.Base(name))
		return
	}
	h.serveFile(w, r, f, fi, name, q.Has("download"))
}

// toFS converts a clean URL path ("/a/b") to an fs.FS name ("a/b").
func toFS(p string) string {
	if p == "/" {
		return "."
	}
	return strings.TrimPrefix(p, "/")
}

// sanitize rejects traversal and odd bytes and returns the clean path.
func sanitize(p string) (string, bool) {
	if strings.ContainsAny(p, "\x00\\") {
		return "", false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", false
		}
		if runtime.GOOS == "windows" && strings.ContainsRune(seg, ':') {
			return "", false
		}
	}

	p = path.Clean(p)
	if p != "/" && !fs.ValidPath(p[1:]) {
		return "", false
	}

	return p, true
}

func (h *prettyFileHandler) hiddenName(name string) bool {
	if h.cfg.privateMarker != "" && strings.EqualFold(name, h.cfg.privateMarker) {
		return true
	}
	return !h.cfg.showHidden && strings.HasPrefix(name, ".")
}

func (h *prettyFileHandler) hiddenPath(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg != "" && h.hiddenName(seg) {
			return true
		}
	}
	return false
}

type lstatFS interface {
	Lstat(name string) (fs.FileInfo, error)
}

// hasSymlink reports whether any component of p is a symlink.
func (h *prettyFileHandler) hasSymlink(p string) bool {
	if h.cfg.followSymlinks || p == "/" {
		return false
	}

	lfs, ok := h.fsys.(lstatFS)
	if !ok {
		return false
	}

	cur := ""
	for _, seg := range strings.Split(strings.Trim(p, "/"), "/") {
		cur = path.Join(cur, seg)
		fi, err := lfs.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return false // Open will 404
		}
		if err != nil || fi.Mode()&fs.ModeSymlink != 0 {
			return true
		}
	}

	return false
}

// isPrivate reports whether dir or any parent holds the marker.
func (h *prettyFileHandler) isPrivate(dir string) bool {
	if h.cfg.privateMarker == "" {
		return false
	}

	for {
		if h.hasMarker(dir) {
			return true
		}
		if dir == "/" {
			return false
		}
		dir = path.Dir(dir)
	}
}

func (h *prettyFileHandler) hasMarker(dir string) bool {
	if h.cfg.privateMarker == "" {
		return false
	}
	_, err := fs.Stat(h.fsys, toFS(path.Join(dir, h.cfg.privateMarker)))
	return err == nil || !errors.Is(err, fs.ErrNotExist)
}

// entryInfo returns info for a directory entry or nil if it must be skipped.
func (h *prettyFileHandler) entryInfo(e fs.DirEntry, child string) fs.FileInfo {
	if e.Type()&fs.ModeSymlink != 0 {
		if !h.cfg.followSymlinks {
			return nil
		}
		fi, err := fs.Stat(h.fsys, toFS(child))
		if err != nil {
			return nil
		}
		return fi
	}

	fi, err := e.Info()
	if err != nil {
		return nil
	}

	return fi
}

// limits pairs a per-IP limiter with a server-wide one.
type limits struct {
	perIP  Limiter
	global Limiter
}

const globalKey = "*"

func (h *prettyFileHandler) allow(w http.ResponseWriter, r *http.Request, l limits, item, reason string) bool {
	if l.perIP == nil && l.global == nil {
		return true
	}

	ip := h.clientIP(r)
	ok, retry := true, time.Duration(0)

	// Per-IP first, so rejected floods don't burn the global budget.
	if l.perIP != nil {
		ok, retry = l.perIP.Allow(ip, item)
	}
	if ok && l.global != nil {
		gitem := ""
		if item != "" {
			gitem = ip + "|" + item
		}
		if ok, retry = l.global.Allow(globalKey, gitem); !ok {
			reason = "The server is busy right now"
		}
	}
	if ok {
		return true
	}

	secs := max(int(math.Ceil(retry.Seconds())), 1)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	h.writeError(w, r, http.StatusTooManyRequests, reason+". Try again in "+strconv.Itoa(secs)+" s", secs)

	return false
}

// clientIP returns the rate limit key of the client.
func (h *prettyFileHandler) clientIP(r *http.Request) string {
	ip := remoteHost(r.RemoteAddr)
	if h.cfg.proxyHops > 0 {
		if fwd := forwardedFor(r); len(fwd) > 0 {
			if i := len(fwd) - h.cfg.proxyHops; i >= 0 {
				if _, err := netip.ParseAddr(fwd[i]); err == nil {
					ip = fwd[i]
				}
			}
		} else if h.cfg.proxyHops == 1 {
			if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
				if _, err := netip.ParseAddr(v); err == nil {
					ip = v
				}
			}
		}
	}

	return limitKey(ip)
}

func forwardedFor(r *http.Request) []string {
	var out []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func remoteHost(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

func limitKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap().WithZone("")
	if a.Is6() {
		return netip.PrefixFrom(a, 64).Masked().String()
	}
	return a.String()
}

// files
func (h *prettyFileHandler) serveFile(w http.ResponseWriter, r *http.Request, f fs.File, fi fs.FileInfo, name string, download bool) {
	if !fi.Mode().IsRegular() {
		h.notFound(w, r)
		return
	}
	if !h.allow(w, r, h.cfg.download, "file:"+name, "Download limit reached for your IP") {
		return
	}
	if download {
		w.Header().Set("Content-Disposition", attachment(fi.Name()))
	}
	h.serveContent(w, r, f, fi)
}

func (h *prettyFileHandler) serveIndex(w http.ResponseWriter, r *http.Request, dir string) bool {
	p := path.Join(dir, "index.html")
	if h.hasSymlink(p) {
		return false
	}

	f, err := h.fsys.Open(toFS(p))
	if err != nil {
		return false
	}
	defer func() {
		_ = f.Close()
	}()

	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	if h.allow(w, r, h.cfg.download, "file:"+p, "Download limit reached for your IP") {
		h.serveContent(w, r, f, fi)
	}

	return true
}

func (h *prettyFileHandler) serveContent(w http.ResponseWriter, r *http.Request, f fs.File, fi fs.FileInfo) {
	hd := w.Header()
	hd.Set("X-Content-Type-Options", "nosniff")
	if h.cfg.cacheControl != "" && hd.Get("Cache-Control") == "" {
		hd.Set("Cache-Control", h.cfg.cacheControl)
	}

	rs, seekable := f.(io.ReadSeeker)
	ct := mime.TypeByExtension(path.Ext(fi.Name()))
	if ct == "" && seekable {
		ct = sniff(rs)
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	hd.Set("Content-Type", ct)
	if h.cfg.sandbox && scriptable(ct) {
		hd.Set("Content-Security-Policy", "sandbox")
	}

	if seekable {
		http.ServeContent(w, r, fi.Name(), fi.ModTime(), rs)
		return
	}

	hd.Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	hd.Set("Last-Modified", fi.ModTime().UTC().Format(http.TimeFormat))
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, f); err != nil && r.Context().Err() == nil {
		h.cfg.log.Warn("prettyfs: send file", "name", fi.Name(), "err", err)
	}
}

func sniff(rs io.ReadSeeker) string {
	var buf [512]byte
	n, _ := io.ReadFull(rs, buf[:])
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return ""
	}
	return http.DetectContentType(buf[:n])
}

func scriptable(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return true
	}
	switch mt {
	case "text/html", "application/xhtml+xml", "image/svg+xml", "text/xml", "application/xml":
		return true
	}
	return false
}

func attachment(filename string) string {
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": filename}); v != "" {
		return v
	}
	return "attachment"
}

// listings
func (h *prettyFileHandler) serveDir(w http.ResponseWriter, r *http.Request, f fs.File, name string) {
	hd := w.Header()
	hd.Set("Content-Type", "text/html; charset=utf-8")
	hd.Set("Cache-Control", "no-cache")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Security-Policy", pageCSP)
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	d, ok := f.(fs.ReadDirFile)
	if !ok {
		h.renderError(w, r, http.StatusInternalServerError, "Could not read this folder")
		return
	}
	entries, truncated, err := readDir(d, h.cfg.maxEntries)
	if err != nil {
		h.cfg.log.Error("prettyfs: readdir", "path", name, "err", err)
		h.renderError(w, r, http.StatusInternalServerError, "Could not read this folder")
		return
	}

	q := r.URL.Query()
	sortBy, order := q.Get("sort"), q.Get("order")
	if sortBy != "size" && sortBy != "date" {
		sortBy = "name"
	}
	if order != "desc" {
		order = "asc"
	}

	data := listing{
		Title:     h.cfg.title,
		Favicon:   template.URL(h.cfg.favicon), //nolint:gosec // G203: favicon comes from server config, not user input
		Path:      name,
		Crumbs:    crumbs(name),
		HasParent: name != "/",
		Zip:       h.cfg.allowZip,
		Truncated: truncated,
		Shown:     h.cfg.maxEntries,
	}
	for _, de := range entries {
		n := de.Name()
		if h.hiddenName(n) {
			continue
		}
		child := path.Join(name, n)
		fi := h.entryInfo(de, child)
		if fi == nil {
			continue
		}

		href := (&url.URL{Path: n}).String()
		e := entry{Name: n, IsDir: fi.IsDir(), ModTime: fi.ModTime()}
		if e.IsDir {
			if h.hasMarker(child) {
				continue
			}
			e.Kind = "folder"
			e.Href = href + "/"
			e.DownloadHref = e.Href + "?zip=1"
			data.TotalDirs++
		} else {
			e.Size = fi.Size()
			e.Kind, e.Ext = fileKind(n)
			e.Href = href
			e.DownloadHref = href + "?download=1"
			data.TotalFiles++
			data.TotalSize += e.Size
		}
		data.Entries = append(data.Entries, e)
	}
	sortEntries(data.Entries, sortBy, order)
	data.Sort = sortLinks(sortBy, order)

	var buf bytes.Buffer
	if err := listTmpl.Execute(&buf, data); err != nil {
		h.cfg.log.Error("prettyfs: template", "path", name, "err", err)
		h.renderError(w, r, http.StatusInternalServerError, "Could not render this folder")
		return
	}
	hd.Set("Content-Length", strconv.Itoa(buf.Len()))
	_, _ = w.Write(buf.Bytes())
}

// readDir reads at most limit entries (limit <= 0: all) and reports truncation.
func readDir(d fs.ReadDirFile, limit int) ([]fs.DirEntry, bool, error) {
	if limit <= 0 {
		es, err := d.ReadDir(-1)
		return es, false, err
	}

	var out []fs.DirEntry
	for len(out) <= limit {
		batch, err := d.ReadDir(256)
		out = append(out, batch...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, false, err
		}
	}
	if len(out) > limit {
		return out[:limit], true, nil
	}

	return out, false, nil
}

type listing struct {
	Title      string
	Favicon    template.URL
	Path       string
	Crumbs     []crumb
	HasParent  bool
	Zip        bool
	Truncated  bool
	Shown      int
	Entries    []entry
	TotalDirs  int
	TotalFiles int
	TotalSize  int64
	Sort       map[string]sortLink
}

type entry struct {
	Name         string
	Href         string
	DownloadHref string
	IsDir        bool
	Size         int64
	ModTime      time.Time
	Kind         string
	Ext          string
}

type crumb struct {
	Name string
	Href string
	Last bool
}

type sortLink struct {
	Href string
	Dir  string
}

func crumbs(name string) []crumb {
	var segs []string
	if name != "/" {
		segs = strings.Split(strings.Trim(name, "/"), "/")
	}

	up := func(n int) string {
		if n == 0 {
			return "./"
		}
		return strings.Repeat("../", n)
	}

	out := []crumb{{Name: "./", Href: up(len(segs)), Last: len(segs) == 0}}
	for i, s := range segs {
		out = append(out, crumb{Name: s + "/", Href: up(len(segs) - 1 - i), Last: i == len(segs)-1})
	}
	return out
}

func sortEntries(es []entry, by, order string) {
	slices.SortStableFunc(es, func(a, b entry) int {
		if a.IsDir != b.IsDir {
			if a.IsDir {
				return -1
			}
			return 1
		}
		c := 0
		switch by {
		case "size":
			c = cmp.Compare(a.Size, b.Size)
		case "date":
			c = a.ModTime.Compare(b.ModTime)
		}
		if c == 0 {
			c = cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		}
		if c == 0 {
			c = cmp.Compare(a.Name, b.Name)
		}
		if order == "desc" {
			c = -c
		}
		return c
	})
}

func sortLinks(by, order string) map[string]sortLink {
	m := make(map[string]sortLink, 3)
	for _, col := range []string{"name", "date", "size"} {
		next, dir := "asc", ""
		if col == by {
			dir = order
			if order == "asc" {
				next = "desc"
			}
		}
		m[col] = sortLink{Href: "?sort=" + col + "&order=" + next, Dir: dir}
	}
	return m
}

var kindByExt = map[string]string{}

func init() {
	groups := map[string]string{
		"image":   ".png .jpg .jpeg .gif .webp .svg .bmp .ico .avif .tiff",
		"video":   ".mp4 .mkv .webm .mov .avi .m4v",
		"audio":   ".mp3 .wav .flac .ogg .m4a .opus",
		"archive": ".zip .tar .gz .tgz .rar .7z .bz2 .xz .zst",
		"code":    ".go .js .ts .jsx .tsx .py .rs .c .h .cpp .java .kt .rb .php .sh .json .yaml .yml .toml .xml .html .css .sql .mod .sum",
		"doc":     ".pdf .doc .docx .txt .md .rtf .odt .xls .xlsx .csv .ppt .pptx .log",
	}
	for kind, exts := range groups {
		for _, e := range strings.Fields(exts) {
			kindByExt[e] = kind
		}
	}
}

func fileKind(name string) (kind, ext string) {
	e := strings.ToLower(path.Ext(name))
	if len(e) < 2 || len(e) > 6 {
		return "file", ""
	}
	if k, ok := kindByExt[e]; ok {
		return k, strings.ToUpper(e[1:])
	}
	return "file", strings.ToUpper(e[1:])
}

// zip
var errZipTooLarge = errors.New("zip too large")

// zipItem is one archive entry; path is empty for directories.
type zipItem struct {
	path  string
	zpath string
	info  fs.FileInfo
}

func (h *prettyFileHandler) serveZip(w http.ResponseWriter, r *http.Request, dir string) {
	if !h.allow(w, r, h.cfg.download, "zip:"+dir, "Download limit reached for your IP") {
		return
	}

	if h.zipSem != nil {
		select {
		case h.zipSem <- struct{}{}:
			defer func() { <-h.zipSem }()
		default:
			w.Header().Set("Retry-After", "10")
			h.writeError(w, r, http.StatusServiceUnavailable, "Too many archives are being built. Try again in 10 s", 10)
			return
		}
	}

	items, err := h.zipPlan(r.Context(), dir)
	switch {
	case errors.Is(err, errZipTooLarge):
		h.renderError(w, r, http.StatusRequestEntityTooLarge, "This folder is too large to download as .zip")
		return
	case err != nil:
		if r.Context().Err() == nil {
			h.cfg.log.Error("prettyfs: zip plan", "path", dir, "err", err)
			h.renderError(w, r, http.StatusInternalServerError, "Could not build the archive")
		}
		return
	}

	base := path.Base(dir)
	if dir == "/" {
		base = "files"
	}
	hd := w.Header()
	hd.Set("Content-Type", "application/zip")
	hd.Set("Content-Disposition", attachment(base+".zip"))
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return
	}

	zw := zip.NewWriter(w)
	for _, it := range items {
		if err := h.zipWrite(zw, it); err != nil {
			if r.Context().Err() == nil {
				h.cfg.log.Error("prettyfs: zip write", "path", dir, "err", err)
			}
			panic(http.ErrAbortHandler)
		}
	}
	if err := zw.Close(); err != nil && r.Context().Err() == nil {
		h.cfg.log.Error("prettyfs: zip close", "path", dir, "err", err)
	}
}

func (h *prettyFileHandler) zipPlan(ctx context.Context, root string) ([]zipItem, error) {
	var (
		items []zipItem
		total int64
	)

	var walk func(dir, prefix string) error
	walk = func(dir, prefix string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := fs.ReadDir(h.fsys, toFS(dir))
		if err != nil {
			return err
		}
		for _, de := range entries {
			n := de.Name()
			if h.hiddenName(n) {
				continue
			}
			child := path.Join(dir, n)
			fi := h.entryInfo(de, child)
			if fi == nil {
				continue
			}

			switch {
			case fi.IsDir():
				// Symlinked dirs are skipped even when following. They can loop.
				if de.Type()&fs.ModeSymlink != 0 || h.hasMarker(child) {
					continue
				}
				items = append(items, zipItem{zpath: prefix + n + "/", info: fi})
				if err := walk(child, prefix+n+"/"); err != nil {
					return err
				}
			case fi.Mode().IsRegular():
				total += fi.Size()
				items = append(items, zipItem{path: child, zpath: prefix + n, info: fi})
			default:
				continue
			}

			if h.cfg.zipMaxFiles > 0 && len(items) > h.cfg.zipMaxFiles {
				return errZipTooLarge
			}
			if h.cfg.zipMaxBytes > 0 && total > h.cfg.zipMaxBytes {
				return errZipTooLarge
			}
		}
		return nil
	}

	if err := walk(root, ""); err != nil {
		return nil, err
	}
	return items, nil
}

var storeExt = map[string]bool{
	".zip": true, ".gz": true, ".tgz": true, ".bz2": true, ".xz": true, ".zst": true, ".7z": true,
	".rar": true, ".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
	".avif": true, ".mp3": true, ".mp4": true, ".mkv": true, ".webm": true, ".mov": true,
	".ogg": true, ".opus": true, ".flac": true, ".m4a": true, ".pdf": true, ".docx": true,
	".xlsx": true, ".pptx": true,
}

func (h *prettyFileHandler) zipWrite(zw *zip.Writer, it zipItem) error {
	if it.path == "" {
		hdr := &zip.FileHeader{Name: it.zpath, Method: zip.Store, Modified: it.info.ModTime()}
		hdr.SetMode(fs.ModeDir | 0o755)
		_, err := zw.CreateHeader(hdr)
		return err
	}

	f, err := h.fsys.Open(toFS(it.path))
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
	}()

	hdr, err := zip.FileInfoHeader(it.info)
	if err != nil {
		return err
	}
	hdr.Name = it.zpath
	hdr.Method = zip.Deflate
	if storeExt[strings.ToLower(path.Ext(it.path))] {
		hdr.Method = zip.Store
	}

	zf, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(zf, f)

	return err
}

// errors, redirects
func (h *prettyFileHandler) openError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		h.notFound(w, r)
	case errors.Is(err, fs.ErrPermission):
		h.renderError(w, r, http.StatusForbidden, "Access denied")
	case errors.Is(err, fs.ErrInvalid):
		h.renderError(w, r, http.StatusBadRequest, "Invalid path")
	default:
		h.cfg.log.Error("prettyfs: open", "path", r.URL.Path, "err", err)
		h.renderError(w, r, http.StatusInternalServerError, "Something went wrong")
	}
}

func (h *prettyFileHandler) notFound(w http.ResponseWriter, r *http.Request) {
	h.renderError(w, r, http.StatusNotFound, "The file or folder doesn't exist")
}

func (h *prettyFileHandler) renderError(w http.ResponseWriter, r *http.Request, code int, msg string) {
	h.writeError(w, r, code, msg, 0)
}

// writeError sends an HTML page to browsers and plain text to everything else.
func (h *prettyFileHandler) writeError(w http.ResponseWriter, r *http.Request, code int, msg string, retryAfter int) {
	hd := w.Header()
	hd.Del("Content-Disposition")
	hd.Del("Last-Modified")
	hd.Del("ETag")
	hd.Set("Cache-Control", "no-store")

	if !strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Error(w, strconv.Itoa(code)+" "+http.StatusText(code)+": "+msg, code)
		return
	}

	var buf bytes.Buffer
	if err := errorTmpl.Execute(&buf, errorPage{
		Title:      h.cfg.title,
		Favicon:    template.URL(h.cfg.favicon), //nolint:gosec // G203: favicon comes from server config, not user input
		Code:       code,
		Status:     http.StatusText(code),
		RetryAfter: retryAfter,
	}); err != nil {
		http.Error(w, http.StatusText(code), code)
		return
	}

	hd.Set("Content-Type", "text/html; charset=utf-8")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Security-Policy", pageCSP)
	hd.Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(code)
	if r.Method != http.MethodHead {
		_, _ = w.Write(buf.Bytes())
	}
}

type errorPage struct {
	Title      string
	Favicon    template.URL
	Code       int
	Status     string
	RetryAfter int
}

func localRedirect(w http.ResponseWriter, r *http.Request, newPath string) {
	if q := r.URL.RawQuery; q != "" {
		newPath += "?" + q
	}
	w.Header().Set("Location", newPath)
	w.WriteHeader(http.StatusMovedPermanently)
}
