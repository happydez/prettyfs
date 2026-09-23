package pretty

import (
	"archive/zip"
	"bytes"
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func testFS() fs.FS {
	mt := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	return fstest.MapFS{
		"hello.txt":              {Data: []byte("hello, world"), ModTime: mt},
		"привет.txt":             {Data: []byte("hi"), ModTime: mt},
		"page.html":              {Data: []byte("<script>alert(1)</script>")},
		".env":                   {Data: []byte("TOKEN=1")},
		"docs/readme.md":         {Data: []byte("# readme")},
		"secret/.private":        {},
		"secret/key.txt":         {Data: []byte("top secret")},
		"secret/nested/deep.txt": {Data: []byte("deep")},
		"pub/a.txt":              {Data: []byte("aaa")},
		"pub/inner/.private":     {},
		"pub/inner/x.txt":        {Data: []byte("x")},
		"site/index.html":        {Data: []byte("<h1>site</h1>")},
		"assets.zip":             {Data: []byte("PK\x03\x04zip"), ModTime: mt},
		"main.go":                {Data: []byte("package main\n"), ModTime: mt},
		"style.css":              {Data: []byte("body{}"), ModTime: mt},
		"bin/tool":               {Data: []byte("\x7fELF\x00\x01\x02\x00binary"), ModTime: mt},
	}
}

func do(h http.Handler, method, target string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, target, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}

	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	return rec
}

func doFrom(h http.Handler, ip, target string) int {
	req := httptest.NewRequestWithContext(context.Background(), "GET", target, nil)
	req.RemoteAddr = ip + ":1234"

	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	return rec.Code
}

func zipNames(t *testing.T, body []byte) map[string]bool {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}

	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}

	return names
}

func newHandler(t *testing.T, opts ...Option) *prettyFileHandler {
	t.Helper()
	h, ok := FileServer(testFS(), opts...).(*prettyFileHandler)
	if !ok {
		t.Fatal("FileServer did not return *prettyFileHandler")
	}
	return h
}

func TestListing(t *testing.T) {
	h := FileServer(testFS())
	rec := do(h, "GET", "/")
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}

	body := rec.Body.String()

	for _, want := range []string{"hello.txt", "docs/", "pub/", "?zip=1", "?download=1"} {
		if !strings.Contains(body, want) {
			t.Errorf("listing has no %q", want)
		}
	}

	for _, bad := range []string{"secret", ".env", ".private"} {
		if strings.Contains(body, bad) {
			t.Errorf("listing leaks %q", bad)
		}
	}

	if strings.Contains(do(h, "GET", "/pub/").Body.String(), "inner") {
		t.Error("private subfolder shown in listing")
	}

	for _, s := range []string{"name", "size", "date"} {
		if c := do(h, "GET", "/?sort="+s+"&order=desc").Code; c != 200 {
			t.Errorf("sort=%s: code %d", s, c)
		}
	}
}

func TestMaxEntries(t *testing.T) {
	body := do(FileServer(testFS(), WithMaxEntries(2)), "GET", "/").Body.String()
	if !strings.Contains(body, "showing the first 2 entries") {
		t.Error("no truncation notice")
	}
}

func TestHead(t *testing.T) {
	rec := do(FileServer(testFS()), "HEAD", "/")
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("code=%d body=%d bytes", rec.Code, rec.Body.Len())
	}
}

func TestPrivate(t *testing.T) {
	h := FileServer(testFS())
	ps := []string{
		"/secret", "/secret/", "/secret/key.txt", "/secret/nested/",
		"/secret/nested/deep.txt", "/secret/.private", "/secret/.PRIVATE",
		"/secret/?zip=1", "/pub/inner/", "/pub/inner/x.txt",
	}
	for _, p := range ps {
		if c := do(h, "GET", p).Code; c != 404 {
			t.Errorf("%s: code = %d, want 404", p, c)
		}
	}
}

func TestTraversal(t *testing.T) {
	h := FileServer(testFS())
	for _, p := range []string{"/../hello.txt", "/docs/../../etc/passwd", "/docs/%2e%2e/hello.txt", "/docs/..%2fhello.txt", "/a%5cb"} {
		if c := do(h, "GET", p).Code; c != 400 {
			t.Errorf("%s: code = %d, want 400", p, c)
		}
	}
}

func TestHiddenFiles(t *testing.T) {
	if c := do(FileServer(testFS()), "GET", "/.env").Code; c != 404 {
		t.Errorf("dotfile: code = %d, want 404", c)
	}
	if c := do(FileServer(testFS(), WithHiddenFiles(true)), "GET", "/.env").Code; c != 200 {
		t.Errorf("dotfile with WithHiddenFiles: code = %d, want 200", c)
	}
}

func TestSymlinks(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, data string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("secret/.private", "")
	write("secret/key.txt", "top secret")
	write("pub/a.txt", "aaa")

	if err := os.Symlink(filepath.Join("..", "secret"), filepath.Join(dir, "pub", "leak")); err != nil {
		t.Skip("symlinks not supported:", err)
	}
	if err := os.Symlink(filepath.Join("..", "secret", "key.txt"), filepath.Join(dir, "pub", "key.txt")); err != nil {
		t.Skip("symlinks not supported:", err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = root.Close()
	})

	h := FileServer(root.FS())

	for _, p := range []string{"/pub/leak/", "/pub/leak/key.txt", "/pub/key.txt"} {
		if c := do(h, "GET", p).Code; c != 404 {
			t.Errorf("%s: code = %d, want 404", p, c)
		}
	}

	if b := do(h, "GET", "/pub/").Body.String(); strings.Contains(b, "leak") || strings.Contains(b, "key.txt") {
		t.Error("symlinks shown in listing")
	}

	names := zipNames(t, do(h, "GET", "/pub/?zip=1").Body.Bytes())
	if !names["a.txt"] || names["key.txt"] || names["leak/"] {
		t.Errorf("zip entries = %v", names)
	}
}

func TestFileAndRange(t *testing.T) {
	h := FileServer(testFS())
	rec := do(h, "GET", "/hello.txt", "Range", "bytes=0-4")
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "hello" {
		t.Fatalf("range: code=%d body=%q", rec.Code, rec.Body.String())
	}
	if cr := rec.Header().Get("Content-Range"); cr != "bytes 0-4/12" {
		t.Errorf("Content-Range = %q", cr)
	}

	rec = do(h, "GET", "/hello.txt?download=1")
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, "hello.txt") {
		t.Errorf("Content-Disposition = %q", cd)
	}

	rec = do(h, "GET", "/%D0%BF%D1%80%D0%B8%D0%B2%D0%B5%D1%82.txt?download=1")
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "filename*=") {
		t.Errorf("non-ASCII Content-Disposition = %q", cd)
	}
}

func TestSandbox(t *testing.T) {
	h := FileServer(testFS())
	if csp := do(h, "GET", "/page.html").Header().Get("Content-Security-Policy"); csp != "sandbox" {
		t.Errorf("html CSP = %q, want sandbox", csp)
	}
	if csp := do(h, "GET", "/hello.txt").Header().Get("Content-Security-Policy"); csp != "" {
		t.Errorf("txt CSP = %q, want none", csp)
	}
	if csp := do(FileServer(testFS(), WithSandbox(false)), "GET", "/page.html").Header().Get("Content-Security-Policy"); csp != "" {
		t.Errorf("CSP with WithSandbox(false) = %q", csp)
	}
}

func TestRedirectsAndIndex(t *testing.T) {
	h := FileServer(testFS())
	cases := map[string]string{
		"/docs":            "docs/",
		"/hello.txt/":      "../hello.txt",
		"/site/index.html": "./",
		"/docs?sort=size":  "docs/?sort=size",
	}
	for p, loc := range cases {
		rec := do(h, "GET", p)
		if rec.Code != 301 || rec.Header().Get("Location") != loc {
			t.Errorf("%s: code=%d Location=%q, want 301 %q", p, rec.Code, rec.Header().Get("Location"), loc)
		}
	}
	if b := do(h, "GET", "/site/").Body.String(); b != "<h1>site</h1>" {
		t.Errorf("index.html not served: %q", b)
	}
	if b := do(FileServer(testFS(), WithIndexHTML(false)), "GET", "/site/").Body.String(); !strings.Contains(b, "index.html") {
		t.Error("expected listing with WithIndexHTML(false)")
	}
}

func TestZip(t *testing.T) {
	rec := do(FileServer(testFS()), "GET", "/?zip=1")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("code=%d type=%q", rec.Code, rec.Header().Get("Content-Type"))
	}

	names := zipNames(t, rec.Body.Bytes())
	for _, want := range []string{"hello.txt", "docs/readme.md", "pub/a.txt"} {
		if !names[want] {
			t.Errorf("zip has no %q (got %v)", want, names)
		}
	}
	for name := range names {
		if strings.HasPrefix(name, "secret") || strings.HasPrefix(name, "pub/inner") || strings.Contains(name, ".env") {
			t.Errorf("zip leaks %q", name)
		}
	}
	if ct := do(FileServer(testFS(), WithZip(false)), "GET", "/?zip=1").Header().Get("Content-Type"); ct == "application/zip" {
		t.Error("zip served although disabled")
	}
}

func TestZipLimits(t *testing.T) {
	if c := do(FileServer(testFS(), WithZipLimit(10, 0)), "GET", "/?zip=1").Code; c != http.StatusRequestEntityTooLarge {
		t.Errorf("size limit: code = %d, want 413", c)
	}
	if c := do(FileServer(testFS(), WithZipLimit(0, 2)), "GET", "/?zip=1").Code; c != http.StatusRequestEntityTooLarge {
		t.Errorf("files limit: code = %d, want 413", c)
	}
	if c := do(FileServer(testFS(), WithZipLimit(0, 0)), "GET", "/?zip=1").Code; c != 200 {
		t.Errorf("no limits: code = %d, want 200", c)
	}
}

func TestZipBusy(t *testing.T) {
	h := newHandler(t, WithMaxConcurrentZips(1))
	h.zipSem <- struct{}{} // one zip "in progress"
	rec := do(h, "GET", "/?zip=1")
	<-h.zipSem
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("code=%d Retry-After=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if c := do(h, "GET", "/?zip=1").Code; c != 200 {
		t.Fatalf("after release: code = %d", c)
	}
}

func TestErrorNegotiation(t *testing.T) {
	h := FileServer(testFS())

	rec := do(h, "GET", "/nope.txt")
	if rec.Code != 404 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("plain: code=%d type=%q", rec.Code, rec.Header().Get("Content-Type"))
	}

	rec = do(h, "GET", "/nope.txt", "Accept", "text/html,*/*")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "Not Found") {
		t.Fatalf("html: code=%d", rec.Code)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	rec := do(FileServer(testFS()), "POST", "/")
	if rec.Code != 405 || rec.Header().Get("Allow") == "" {
		t.Fatalf("code=%d Allow=%q", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestCrumbs(t *testing.T) {
	c := crumbs("/static/css")
	if len(c) != 3 || c[0].Name != "./" || c[0].Href != "../../" || c[2].Name != "css/" || !c[2].Last {
		t.Fatalf("crumbs = %+v", c)
	}
}

func TestRateLimit(t *testing.T) {
	h := FileServer(testFS(), WithRateLimit(2, time.Minute))
	do(h, "GET", "/")
	do(h, "GET", "/")
	rec := do(h, "GET", "/")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("code=%d Retry-After=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if b := rec.Body.String(); !strings.Contains(b, "from your IP. Try again in ") {
		t.Errorf("plain body = %q", b)
	}

	h = FileServer(testFS(), WithDownloadRateLimit(1, time.Minute))
	for i := 0; i < 5; i++ {
		if c := do(h, "GET", "/").Code; c != 200 {
			t.Fatalf("listing limited by download limiter: %d", c)
		}
	}

	for i := 0; i < 5; i++ {
		if c := do(h, "GET", "/docs/readme.md").Code; c != 200 {
			t.Fatalf("inline view %d limited by download limiter: %d", i, c)
		}
	}

	if c := do(h, "GET", "/hello.txt?download=1").Code; c != 200 {
		t.Fatalf("first download: %d", c)
	}

	if c := do(h, "GET", "/hello.txt?download=1", "Range", "bytes=5-").Code; c != http.StatusPartialContent {
		t.Fatalf("range of same file: %d, want 206", c)
	}

	if c := do(h, "GET", "/hello.txt?download=1").Code; c != 200 {
		t.Fatalf("same file again: %d", c)
	}

	if c := do(h, "GET", "/docs/readme.md?download=1").Code; c != 429 {
		t.Fatalf("second file: %d, want 429", c)
	}

	if b := do(h, "GET", "/docs/readme.md?download=1", "Accept", "text/html").Body.String(); !strings.Contains(b, "try again") {
		t.Error("html 429 page has no retry button")
	}
}

func TestBlockedExtensions(t *testing.T) {
	h := FileServer(testFS(), WithBlockedExtensions("ZIP", ".Go"))

	for _, p := range []string{"/assets.zip", "/assets.zip?download=1", "/main.go"} {
		if c := do(h, "GET", p).Code; c != http.StatusForbidden {
			t.Errorf("%s: code = %d, want 403", p, c)
		}
	}

	body := do(h, "GET", "/").Body.String()
	for _, n := range []string{"assets.zip", "main.go"} {
		if !strings.Contains(body, n) {
			t.Errorf("%s must stay listed", n)
		}
		if strings.Contains(body, `href="`+n+`"`) {
			t.Errorf("%s must be listed without a link", n)
		}
	}
	if !strings.Contains(body, `href="style.css"`) {
		t.Error("other files must keep their links")
	}
	if c := do(h, "GET", "/style.css").Code; c != 200 {
		t.Errorf("style.css: %d", c)
	}

	if names := zipNames(t, do(h, "GET", "/?zip=1").Body.Bytes()); names["assets.zip"] || names["main.go"] {
		t.Errorf("zip carries blocked files: %v", names)
	}

	h = FileServer(testFS(), WithBlockedExtensions(".zip"), WithBlockedExtensions())
	if c := do(h, "GET", "/assets.zip").Code; c != 200 {
		t.Errorf("after clearing: %d, want 200", c)
	}
}

func TestNonInlineCountsAsDownload(t *testing.T) {
	h := FileServer(testFS(), WithDownloadRequestRateLimit(2, time.Minute))

	for i := 0; i < 2; i++ {
		if c := do(h, "GET", "/assets.zip").Code; c != 200 {
			t.Fatalf("archive %d: %d", i, c)
		}
	}
	if c := do(h, "GET", "/assets.zip").Code; c != 429 {
		t.Fatalf("third archive: %d, want 429", c)
	}

	for i := 0; i < 5; i++ {
		if c := do(h, "GET", "/docs/readme.md").Code; c != 200 {
			t.Fatalf("inline view %d: %d", i, c)
		}
	}
}

func TestZipVsDownload(t *testing.T) {
	zipped := func(opts ...Option) bool {
		rec := do(FileServer(testFS(), opts...), "GET", "/?zip=1")
		if rec.Code != 200 {
			t.Fatalf("?zip=1: code = %d", rec.Code)
		}
		return strings.HasPrefix(rec.Header().Get("Content-Type"), "application/zip")
	}

	cases := []struct {
		name string
		opts []Option
		want bool
	}{
		{"default", nil, true},
		{"downloads off", []Option{WithDownload(false)}, false},
		{"downloads off, zip asked for", []Option{WithDownload(false), WithZip(true)}, true},
		{"zip asked for, downloads off", []Option{WithZip(true), WithDownload(false)}, true},
		{"downloads off, zip refused", []Option{WithDownload(false), WithZip(false)}, false},
		{"zip refused", []Option{WithZip(false)}, false},
	}
	for _, c := range cases {
		if got := zipped(c.opts...); got != c.want {
			t.Errorf("%s: archive served = %v, want %v", c.name, got, c.want)
		}
	}

	h := FileServer(testFS(), WithDownload(false), WithZip(true))
	if c := do(h, "GET", "/assets.zip").Code; c != http.StatusForbidden {
		t.Errorf("single file: code = %d, want 403", c)
	}
	if b := do(h, "GET", "/").Body.String(); !strings.Contains(b, "?zip=1") {
		t.Error("zip button must be back in the listing")
	}
}

func TestNoDownload(t *testing.T) {
	h := FileServer(testFS(), WithDownload(false))

	body := do(h, "GET", "/").Body.String()
	if strings.Contains(body, "?download=1") {
		t.Error("listing still has a download button")
	}
	if strings.Contains(body, "?zip=1") {
		t.Error("listing still offers folder archives")
	}

	rec := do(h, "GET", "/hello.txt?download=1")
	if rec.Code != 200 || rec.Body.String() != "hello, world" {
		t.Fatalf("file still served inline: code=%d body=%q", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != "" {
		t.Errorf("Content-Disposition = %q, want none", cd)
	}

	if loc := do(h, "GET", "/site/index.html?download=1").Header().Get("Location"); loc != "./?download=1" {
		t.Errorf("index.html?download=1: Location = %q", loc)
	}

	for _, p := range []string{"/assets.zip", "/assets.zip?download=1", "/bin/tool"} {
		if c := do(h, "GET", p).Code; c != http.StatusForbidden {
			t.Errorf("%s: code = %d, want 403", p, c)
		}
	}
	if strings.Contains(body, `href="assets.zip"`) {
		t.Error("listing links a file that cannot be opened")
	}
	if !strings.Contains(body, "assets.zip") {
		t.Error("the file must still be listed by name")
	}

	for _, n := range []string{"main.go", "style.css"} {
		if c := do(h, "GET", "/"+n).Code; c != 200 {
			t.Errorf("%s: code = %d, want 200", n, c)
		}
		if !strings.Contains(body, `href="`+n+`"`) {
			t.Errorf("%s: listed without a link", n)
		}
	}

	if b := do(FileServer(testFS()), "GET", "/").Body.String(); !strings.Contains(b, "?download=1") {
		t.Error("download button missing by default")
	}
}

func TestDownloadRequestRateLimit(t *testing.T) {
	h := FileServer(testFS(), WithDownloadRequestRateLimit(2, time.Minute))

	for i := 0; i < 5; i++ {
		if c := do(h, "GET", "/").Code; c != 200 {
			t.Fatalf("listing limited by download limiter: %d", c)
		}
		if c := do(h, "GET", "/hello.txt").Code; c != 200 {
			t.Fatalf("inline view %d limited by download limiter: %d", i, c)
		}
	}

	if c := do(h, "GET", "/hello.txt?download=1").Code; c != 200 {
		t.Fatalf("first download: %d", c)
	}
	if c := do(h, "GET", "/hello.txt?download=1").Code; c != 200 {
		t.Fatalf("second download: %d", c)
	}
	if c := do(h, "GET", "/hello.txt?download=1").Code; c != 429 {
		t.Fatalf("third download of the same file: %d, want 429", c)
	}

	h = FileServer(testFS(), WithDownloadRequestRateLimit(2, time.Minute))
	for i := 0; i < 2; i++ {
		if c := do(h, "GET", "/hello.txt?download=1", "Range", "bytes=0-1").Code; c != http.StatusPartialContent {
			t.Fatalf("range chunk %d: %d, want 206", i, c)
		}
	}
	if c := do(h, "GET", "/hello.txt?download=1", "Range", "bytes=0-1").Code; c != 429 {
		t.Fatalf("third range chunk: %d, want 429", c)
	}
	if c := do(h, "GET", "/docs/?zip=1", "Range", "bytes=0-").Code; c != 429 {
		t.Fatalf("zip with Range: %d, want 429", c)
	}
}

func TestGlobalRateLimit(t *testing.T) {
	h := FileServer(testFS(), WithGlobalRateLimit(2, time.Minute), WithRateLimit(100, time.Minute))
	if doFrom(h, "10.0.0.1", "/") != 200 || doFrom(h, "10.0.0.2", "/") != 200 {
		t.Fatal("first two requests must pass")
	}
	if c := doFrom(h, "10.0.0.3", "/"); c != 429 {
		t.Fatalf("third client: %d, want 429", c)
	}
}

func TestGlobalDownloadRateLimit(t *testing.T) {
	h := FileServer(testFS(), WithGlobalDownloadRateLimit(2, time.Minute))
	if doFrom(h, "10.0.0.1", "/hello.txt?download=1") != 200 || doFrom(h, "10.0.0.2", "/hello.txt?download=1") != 200 {
		t.Fatal("two clients, same file: both must pass")
	}
	if c := doFrom(h, "10.0.0.1", "/hello.txt?download=1"); c != 200 {
		t.Fatalf("repeat by same client: %d", c)
	}
	if c := doFrom(h, "10.0.0.3", "/hello.txt?download=1"); c != 429 {
		t.Fatalf("third client: %d, want 429", c)
	}
	if c := doFrom(h, "10.0.0.3", "/hello.txt"); c != 200 {
		t.Fatalf("inline view: %d", c)
	}
	if c := doFrom(h, "10.0.0.3", "/"); c != 200 {
		t.Fatalf("listing: %d", c)
	}
}

func TestDisabledLimits(t *testing.T) {
	h := FileServer(testFS(),
		WithRateLimit(0, time.Minute),
		WithDownloadRateLimit(0, time.Minute),
		WithGlobalRateLimit(0, time.Minute),
		WithGlobalDownloadRateLimit(0, time.Minute),
	)
	for i := 0; i < 5; i++ {
		if c := do(h, "GET", "/hello.txt").Code; c != 200 {
			t.Fatalf("request %d: %d", i, c)
		}
	}
}

func TestWindowLimiter(t *testing.T) {
	now := time.Now()
	l := newWindowLimiter(2, time.Minute)
	l.now = func() time.Time { return now }

	for i := 0; i < 2; i++ {
		if ok, _ := l.Allow("ip", ""); !ok {
			t.Fatalf("hit %d must pass", i)
		}
	}
	if ok, retry := l.Allow("ip", ""); ok || retry != time.Minute {
		t.Fatalf("third hit: ok=%v retry=%v", ok, retry)
	}

	now = now.Add(30 * time.Second)
	if ok, retry := l.Allow("ip", ""); ok || retry != 30*time.Second {
		t.Fatalf("mid-window: ok=%v retry=%v, want false and 30s", ok, retry)
	}

	now = now.Add(31 * time.Second)
	if ok, _ := l.Allow("ip", ""); !ok {
		t.Error("window must slide")
	}

	l = newWindowLimiter(1, time.Minute)
	base := now
	l.now = func() time.Time { return base }
	if ok, _ := l.Allow("ip", "file:a"); !ok {
		t.Fatal("first item must pass")
	}
	for i := 0; i < 5; i++ {
		base = base.Add(50 * time.Second)
		if ok, _ := l.Allow("ip", "file:a"); !ok {
			t.Fatalf("repeat %d must stay free", i)
		}
	}
	base = base.Add(10 * time.Second)
	if ok, _ := l.Allow("ip", "file:b"); !ok {
		t.Error("a hot item must not hold the only slot forever")
	}
}

func TestClientIP(t *testing.T) {
	h := newHandler(t)
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 1.2.3.4")

	for hops, want := range map[int]string{0: "127.0.0.1", 1: "1.2.3.4", 2: "6.6.6.6", 3: "127.0.0.1"} {
		h.cfg.proxyHops = hops
		if got := h.clientIP(req); got != want {
			t.Errorf("hops=%d: %q, want %q", hops, got, want)
		}
	}

	if limitKey("2001:db8::1") != limitKey("2001:db8::ffff") {
		t.Error("same /64 must share a key")
	}

	if limitKey("2001:db8::1") == limitKey("2001:db8:0:1::1") {
		t.Error("different /64 must not share a key")
	}

	if got := limitKey("::ffff:1.2.3.4"); got != "1.2.3.4" {
		t.Errorf("mapped IPv4 = %q", got)
	}
}

func TestLimiterWindow(t *testing.T) {
	l := newWindowLimiter(3, time.Minute)
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("a", ""); !ok {
			t.Fatalf("request %d denied", i+1)
		}
	}
	if ok, retry := l.Allow("a", ""); ok || retry != time.Minute {
		t.Fatalf("4th: ok=%v retry=%v, want denied / 1m", ok, retry)
	}

	now = now.Add(20 * time.Second)
	if ok, retry := l.Allow("a", ""); ok || retry != 40*time.Second {
		t.Fatalf("after 20s: ok=%v retry=%v, want denied / 40s", ok, retry)
	}

	now = now.Add(40 * time.Second)
	if ok, _ := l.Allow("a", ""); !ok {
		t.Fatal("window did not slide")
	}
}

func TestLimiterSameItem(t *testing.T) {
	l := newWindowLimiter(2, time.Minute)
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }

	l.Allow("a", "video")
	l.Allow("a", "other")

	for i := 0; i < 10; i++ {
		now = now.Add(10 * time.Second)
		if ok, _ := l.Allow("a", "video"); !ok {
			t.Fatalf("seek %d denied", i)
		}
	}

	if ok, _ := l.Allow("a", "third"); !ok {
		t.Fatal(`"other" should have expired`)
	}

	if ok, _ := l.Allow("a", "fourth"); ok {
		t.Fatal("limit exceeded")
	}
}

func TestLimiterMaxKeys(t *testing.T) {
	l := newWindowLimiter(1, time.Minute)
	l.maxKeys = 2
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }

	l.Allow("a", "")
	l.Allow("b", "")

	if ok, _ := l.Allow("c", ""); ok {
		t.Fatal("new key accepted over the cap")
	}

	if ok, _ := l.Allow("a", ""); ok {
		t.Fatal("known key must still be limited normally")
	}

	now = now.Add(61 * time.Second)
	if ok, _ := l.Allow("c", ""); !ok {
		t.Fatal("expired keys were not swept")
	}
}

func TestTokenBucket(t *testing.T) {
	b := newTokenBucket(2, time.Minute)
	b.Allow("", "")
	b.Allow("", "")
	if ok, retry := b.Allow("", ""); ok || retry <= 0 {
		t.Fatalf("ok=%v retry=%v, want denied with wait", ok, retry)
	}
}
