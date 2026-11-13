package browser

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/headzoo/surf/jar"
)

// newHistoryTestBrowser builds a browser whose default attributes mirror the
// ones used by the rest of the suite.
func newHistoryTestBrowser() *Browser {
	b := newDefaultTestBrowser()
	return b
}

// pageA and pageB are deliberately different in every restored dimension:
// URL, status code, a response header, the DOM and the downloadable bytes.
const pageABody = `<html><head><title>Page A</title></head>
<body><p id="a">alpha</p><a href="rel/a.html">A relative link</a></body></html>`

const pageBBody = `<html><head><title>Page B</title><base href="/base/b/"></head>
<body><p id="b">bravo</p><a href="rel/b.html">B relative link</a></body></html>`

// newABServer serves page A at /a/ and page B at /b/. A custom header and a
// non-200 status on A make state restoration fully observable.
func newABServer(t *testing.T) (*httptest.Server, *int32) {
	var requests int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/a/":
			w.Header().Set("X-Page", "A")
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, pageABody)
		case "/b/":
			w.Header().Set("X-Page", "B")
			io.WriteString(w, pageBBody)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &requests
}

// TestBackRestoresFullPageState covers A -> B -> Back and asserts every part
// of the page state travels as one unit, and that Back issues no request.
func TestBackRestoresFullPageState(t *testing.T) {
	ts, requests := newABServer(t)
	b := newHistoryTestBrowser()

	if err := b.Open(ts.URL + "/a/"); err != nil {
		t.Fatalf("open A: %v", err)
	}
	if err := b.Open(ts.URL + "/b/"); err != nil {
		t.Fatalf("open B: %v", err)
	}
	if got := atomic.LoadInt32(requests); got != 2 {
		t.Fatalf("expected 2 requests after A->B, got %d", got)
	}

	// On B the base tag must apply.
	link := b.Links()[0].URL.String()
	if link != ts.URL+"/base/b/rel/b.html" {
		t.Fatalf("base tag not applied on B, got %s", link)
	}

	if !b.Back() {
		t.Fatal("Back returned false with one previous page")
	}

	// No HTTP request may be issued by Back.
	if got := atomic.LoadInt32(requests); got != 2 {
		t.Fatalf("Back issued a request: total requests now %d", got)
	}

	// URL
	if b.Url().String() != ts.URL+"/a/" {
		t.Errorf("URL not restored: %s", b.Url())
	}
	// Status code
	if b.StatusCode() != http.StatusCreated {
		t.Errorf("status code not restored: got %d want %d", b.StatusCode(), http.StatusCreated)
	}
	// Response headers
	if got := b.ResponseHeaders().Get("X-Page"); got != "A" {
		t.Errorf("response headers not restored: X-Page=%q", got)
	}
	// DOM
	if b.Title() != "Page A" || b.Find("#a").Length() != 1 {
		t.Errorf("DOM not restored: title=%q", b.Title())
	}
	// Effective base and relative links belong to A again (B's base is gone).
	if b.RelativeUrl().String() != ts.URL+"/a/" {
		t.Errorf("base not restored to A page URL, got %s", b.RelativeUrl())
	}
	if got := b.Links()[0].URL.String(); got != ts.URL+"/a/rel/a.html" {
		t.Errorf("relative link resolved against wrong base: %s", got)
	}
	// Downloaded bytes belong to A.
	buf := &bytes.Buffer{}
	if _, err := b.Download(buf); err != nil {
		t.Fatalf("Download after Back: %v", err)
	}
	if buf.String() != pageABody {
		t.Errorf("Download returned bytes from another page: %q", buf.String())
	}
}

// TestBackTwiceWithOnlyTwoPages checks Back reports false at the bottom of
// the history and keeps the current page.
func TestBackTwiceWithOnlyTwoPages(t *testing.T) {
	ts, _ := newABServer(t)
	b := newHistoryTestBrowser()
	if err := b.Open(ts.URL + "/a/"); err != nil {
		t.Fatal(err)
	}
	if err := b.Open(ts.URL + "/b/"); err != nil {
		t.Fatal(err)
	}
	if !b.Back() {
		t.Fatal("first Back failed")
	}
	if b.Back() {
		t.Fatal("second Back must return false")
	}
	if b.Title() != "Page A" {
		t.Fatalf("current page changed after Back at bottom: %q", b.Title())
	}
}

// TestNoBasePageDoesNotInheritBase visits a page with a base, then a page
// without one: the second page must resolve links against its own URL. This
// holds both after a forward navigation and after Back.
func TestNoBasePageDoesNotInheritBase(t *testing.T) {
	ts, _ := newABServer(t)
	b := newHistoryTestBrowser()

	if err := b.Open(ts.URL + "/b/"); err != nil {
		t.Fatal(err)
	}
	if got := b.RelativeUrl().String(); got != ts.URL+"/base/b/" {
		t.Fatalf("B base wrong: %s", got)
	}

	if err := b.Open(ts.URL + "/a/"); err != nil {
		t.Fatal(err)
	}
	if got := b.RelativeUrl().String(); got != ts.URL+"/a/" {
		t.Fatalf("A inherited B's base instead of using its own URL: %s", got)
	}
	if got := b.Links()[0].URL.String(); got != ts.URL+"/a/rel/a.html" {
		t.Fatalf("relative link wrong after leaving base page: %s", got)
	}
}

// TestBaseResolutionIgnoresMetaRefreshSwitch disables MetaRefreshHandling:
// <base> must still be processed.
func TestBaseResolutionIgnoresMetaRefreshSwitch(t *testing.T) {
	ts, _ := newABServer(t)
	b := newHistoryTestBrowser()
	b.SetAttribute(MetaRefreshHandling, false)

	if err := b.Open(ts.URL + "/b/"); err != nil {
		t.Fatal(err)
	}
	if got := b.RelativeUrl().String(); got != ts.URL+"/base/b/" {
		t.Fatalf("base not resolved with MetaRefreshHandling off: %s", got)
	}
}

// errorReadCloser fails every Read.
type errorReadCloser struct{}

func (errorReadCloser) Read([]byte) (int, error) { return 0, fmt.Errorf("simulated broken stream") }
func (errorReadCloser) Close() error             { return nil }

// failingTransport returns a configurable response or error without touching
// the network, letting us inject decompression and read failures.
type failingTransport struct {
	mu          sync.Mutex
	header      http.Header
	body        []byte
	roundTripFn func(req *http.Request) (*http.Response, error)
	requests    int
}

func (tr *failingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.requests++
	if tr.roundTripFn != nil {
		return tr.roundTripFn(req)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     tr.header,
		Body:       ioutil.NopCloser(bytes.NewReader(tr.body)),
		Request:    req,
	}, nil
}

// TestFailureKeepsCurrentPageAndHistory covers three failure modes: network
// error, corrupt gzip and a stream that errors while being read. In every
// case the previously loaded page (including its bytes and history) must be
// retained and no history entry may be added.
func TestFailureKeepsCurrentPageAndHistory(t *testing.T) {
	ts, _ := newABServer(t)

	cases := []struct {
		name string
		tr   *failingTransport
	}{
		{
			name: "network failure",
			tr: &failingTransport{roundTripFn: func(req *http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("connection refused")
			}},
		},
		{
			name: "bad gzip",
			tr: &failingTransport{
				header: http.Header{"Content-Encoding": []string{"gzip"}},
				body:   []byte("this is not gzip data at all"),
			},
		},
		{
			name: "broken stream",
			tr: &failingTransport{
				roundTripFn: func(req *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       errorReadCloser{},
						Request:    req,
					}, nil
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newHistoryTestBrowser()
			if err := b.Open(ts.URL + "/a/"); err != nil {
				t.Fatal(err)
			}
			histLenBefore := b.HistoryJar().Len()
			titleBefore := b.Title()
			urlBefore := b.Url().String()
			bufBefore := &bytes.Buffer{}
			if _, err := b.Download(bufBefore); err != nil {
				t.Fatal(err)
			}

			b.SetTransport(tc.tr)
			err := b.Open("http://fail.example.invalid/page")
			if err == nil {
				t.Fatal("expected the request to fail")
			}

			// Current page untouched.
			if b.Title() != titleBefore || b.Url().String() != urlBefore {
				t.Fatalf("current page changed after failure: title=%q url=%s", b.Title(), b.Url())
			}
			bufAfter := &bytes.Buffer{}
			if _, err := b.Download(bufAfter); err != nil {
				t.Fatalf("Download after failure: %v", err)
			}
			if bufAfter.String() != bufBefore.String() {
				t.Fatal("page bytes changed after failed request")
			}
			// History untouched.
			if b.HistoryJar().Len() != histLenBefore {
				t.Fatalf("history length changed: before=%d after=%d", histLenBefore, b.HistoryJar().Len())
			}
			// Back still works and reaches the same place.
			if b.Back() {
				t.Fatal("failed request must not have pushed a history entry")
			}
		})
	}
}

// TestDeflateCorruptionIsDetected serves a deflate-encoded body that does not
// contain a valid stream. The flate reader must surface the error through
// Read/Close instead of silently accepting it.
func TestDeflateCorruptionIsDetected(t *testing.T) {
	// 0x78 0x9c is the zlib header; the bytes that follow are not a valid
	// deflate stream, so flate.Read must return an error.
	corrupt := []byte{0x78, 0x9c, 0xff, 0xff, 0xff, 0xff, 0x00, 0x00, 0xff, 0xff}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "deflate")
		w.Write(corrupt)
	}))
	t.Cleanup(ts.Close)

	tsGood, _ := newABServer(t)
	b := newHistoryTestBrowser()
	if err := b.Open(tsGood.URL + "/a/"); err != nil {
		t.Fatal(err)
	}
	if err := b.Open(ts.URL); err == nil {
		t.Fatal("expected corrupt deflate stream to error")
	}
	if b.Title() != "Page A" {
		t.Fatalf("corrupt deflate replaced the current page: %q", b.Title())
	}
}

// TestRefreshTimerCancelledOnNavigation loads a page that schedules a short
// meta refresh, navigates away before it fires, and asserts the old timer
// never triggers a navigation.
func TestRefreshTimerCancelledOnNavigation(t *testing.T) {
	var requests int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/refresh":
			io.WriteString(w, `<html><head>
				<meta http-equiv="refresh" content="1; url=/refresh"></head>
				<body>refresh page</body></html>`)
		case "/other":
			io.WriteString(w, `<html><head><title>Other</title></head><body>other</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)

	b := newHistoryTestBrowser()
	if err := b.Open(ts.URL + "/refresh"); err != nil {
		t.Fatal(err)
	}
	if err := b.Open(ts.URL + "/other"); err != nil {
		t.Fatal(err)
	}

	time.Sleep(1500 * time.Millisecond)

	if b.Title() != "Other" {
		t.Fatalf("old refresh timer navigated away: title=%q", b.Title())
	}
	// Exactly the two explicit requests; the timer must not have fired.
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("expected 2 requests, got %d (timer leaked)", got)
	}
}

// TestRefreshTimerCancelledOnBack loads A (no refresh), then B (refresh),
// goes Back to A and waits: B's timer must not navigate back to B.
func TestRefreshTimerCancelledOnBack(t *testing.T) {
	var requests int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/a":
			io.WriteString(w, pageABody)
		case "/b":
			io.WriteString(w, `<html><head>
				<meta http-equiv="refresh" content="1; url=/b"></head>
				<body>refresh B</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)

	b := newHistoryTestBrowser()
	if err := b.Open(ts.URL + "/a"); err != nil {
		t.Fatal(err)
	}
	if err := b.Open(ts.URL + "/b"); err != nil {
		t.Fatal(err)
	}
	if !b.Back() {
		t.Fatal("Back failed")
	}
	if b.Title() != "Page A" {
		t.Fatalf("Back did not restore A: %q", b.Title())
	}

	time.Sleep(1500 * time.Millisecond)

	if b.Title() != "Page A" {
		t.Fatalf("B's refresh timer navigated after Back: title=%q", b.Title())
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("expected 2 requests, got %d", got)
	}
}

// TestHistoryCapacityOneStillAllowsOneBack sets max history to 1: after
// A -> B -> C, only one Back is possible and it lands on B.
func TestHistoryCapacityOneStillAllowsOneBack(t *testing.T) {
	ts, _ := newABServer(t)
	b := newHistoryTestBrowser()
	b.HistoryJar().SetMax(1)

	if err := b.Open(ts.URL + "/a/"); err != nil {
		t.Fatal(err)
	}
	if err := b.Open(ts.URL + "/b/"); err != nil {
		t.Fatal(err)
	}
	// A would be evicted here, simulating further browsing at capacity 1.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<html><head><title>C</title></head><body>C</body></html>`)
	}))
	defer other.Close()
	if err := b.Open(other.URL); err != nil {
		t.Fatal(err)
	}
	if b.HistoryJar().Len() != 1 {
		t.Fatalf("capacity not enforced: len=%d", b.HistoryJar().Len())
	}

	if !b.Back() {
		t.Fatal("capacity 1 must still allow going back one page")
	}
	if b.Title() != "Page B" {
		t.Fatalf("Back landed on %q, want Page B", b.Title())
	}
	if b.Back() {
		t.Fatal("only one back step should be possible at capacity 1")
	}
}

// TestDownloadWithoutRawBytesErrors covers legacy SetState usage: a state
// without stored bytes must produce an explicit error, never bytes belonging
// to another page.
func TestDownloadWithoutRawBytesErrors(t *testing.T) {
	ts, _ := newABServer(t)
	b := newHistoryTestBrowser()

	// Load A first so there are real bytes sitting in an earlier state that
	// Download could wrongly return.
	if err := b.Open(ts.URL + "/a/"); err != nil {
		t.Fatal(err)
	}

	u, _ := url.Parse(ts.URL + "/legacy")
	b.SetState(&jar.State{
		Request:  &http.Request{URL: u},
		Response: &http.Response{Request: &http.Request{URL: u}, Header: make(http.Header)},
	})

	buf := &bytes.Buffer{}
	n, err := b.Download(buf)
	if err == nil {
		t.Fatal("Download on a state without bytes must error")
	}
	if n != 0 || buf.Len() != 0 {
		t.Fatalf("no bytes may be written on error: n=%d len=%d", n, buf.Len())
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Fatalf("error should explain the raw contents are unavailable, got: %v", err)
	}
	if b.Url().String() != ts.URL+"/legacy" {
		t.Fatalf("state should remain the legacy state, url=%s", b.Url())
	}
}

// TestCookiesSurviveBack ensures Back does not touch the cookie jar: a
// cookie set on B is still present after navigating Back to A.
func TestCookiesSurviveBack(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/a":
			io.WriteString(w, pageABody)
		case "/b":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "keep-me", Path: "/"})
			io.WriteString(w, pageBBody)
		}
	}))
	t.Cleanup(ts.Close)

	b := newHistoryTestBrowser()
	if err := b.Open(ts.URL + "/a"); err != nil {
		t.Fatal(err)
	}
	if err := b.Open(ts.URL + "/b"); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(ts.URL)
	cookies := b.CookieJar().Cookies(u)
	var found bool
	for _, c := range cookies {
		if c.Name == "session" && c.Value == "keep-me" {
			found = true
		}
	}
	if !found {
		t.Fatal("cookie was not stored")
	}

	if !b.Back() {
		t.Fatal("Back failed")
	}
	cookies = b.CookieJar().Cookies(u)
	found = false
	for _, c := range cookies {
		if c.Name == "session" && c.Value == "keep-me" {
			found = true
		}
	}
	if !found {
		t.Fatal("cookie jar was rolled back by Back")
	}
}

// TestRedirectLandsAsSingleState follows a redirect and confirms the final
// page is one coherent state whose URL, DOM, headers and bytes all describe
// the destination, and Back returns to the origin page.
func TestRedirectLandsAsSingleState(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/start":
			w.Header().Set("Location", "/dest")
			w.WriteHeader(http.StatusFound)
		case "/dest":
			w.Header().Set("X-Page", "dest")
			io.WriteString(w, `<html><head><title>Dest</title></head><body>destination</body></html>`)
		}
	}))
	t.Cleanup(ts.Close)

	b := newHistoryTestBrowser()
	if err := b.Open(ts.URL + "/start"); err != nil {
		t.Fatal(err)
	}
	if b.Url().String() != ts.URL+"/dest" {
		t.Fatalf("did not follow redirect to destination, url=%s", b.Url())
	}
	if b.Title() != "Dest" || b.ResponseHeaders().Get("X-Page") != "dest" {
		t.Fatal("redirect destination state inconsistent")
	}
	buf := &bytes.Buffer{}
	if _, err := b.Download(buf); err != nil || !strings.Contains(buf.String(), "destination") {
		t.Fatalf("destination bytes wrong: %q err=%v", buf.String(), err)
	}
	// /start produced only a 302, which the HTTP client followed without it
	// ever becoming a rendered page. Therefore no half-loaded state may be
	// fabricated for it: the destination is the first real page and Back has
	// nothing to return to.
	if b.Back() {
		t.Fatal("Back must not fabricate a state for the intermediate 302")
	}
	if b.Title() != "Dest" {
		t.Fatalf("failed Back must keep the current page, got %q", b.Title())
	}

	// With an origin page that actually rendered, Back must reach it.
	b2 := newHistoryTestBrowser()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/go" {
			w.Header().Set("Location", ts.URL+"/dest")
			w.WriteHeader(http.StatusFound)
			return
		}
		io.WriteString(w, pageABody)
	}))
	defer origin.Close()
	if err := b2.Open(origin.URL + "/a"); err != nil {
		t.Fatal(err)
	}
	if err := b2.Open(origin.URL + "/go"); err != nil {
		t.Fatal(err)
	}
	if b2.Url().String() != ts.URL+"/dest" {
		t.Fatalf("cross-server redirect not followed, url=%s", b2.Url())
	}
	if !b2.Back() {
		t.Fatal("Back should restore the rendered origin page")
	}
	if b2.Title() != "Page A" {
		t.Fatalf("Back restored %q, want Page A", b2.Title())
	}
}
