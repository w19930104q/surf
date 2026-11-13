package browser

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTabTestServer spins up a local HTTP server that models a login session,
// filter pages, form echo endpoints, a redirect and a meta refresh page.
func newTabTestServer(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var refreshHits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loggedIn := func() bool {
			for _, c := range r.Cookies() {
				if c.Name == "session" && c.Value == "logged-in" {
					return true
				}
			}
			return false
		}

		switch r.URL.Path {
		case "/login":
			if r.Method != http.MethodPost {
				http.Error(w, "POST required", http.StatusMethodNotAllowed)
				return
			}
			body, _ := ioutil.ReadAll(r.Body)
			values, _ := url.ParseQuery(string(body))
			if values.Get("user") != "alice" || values.Get("pass") != "secret" {
				http.Error(w, "bad credentials", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "logged-in", Path: "/"})
			fmt.Fprint(w, `<html><head><title>Home</title></head><body><a id="filters" href="/filters">Filters</a></body></html>`)

		case "/filters":
			if !loggedIn() {
				http.Error(w, "login required", http.StatusForbidden)
				return
			}
			fmt.Fprintf(w, `<html><head><title>Filters</title></head><body>
<p id="who">%s</p>
<div id="slot">parent-value</div>
<form id="f" action="/search" method="post">
	<input type="text" name="q" value="default-q">
	<input type="checkbox" name="only_open" value="1">
	<input type="submit" name="go" value="1">
</form>
<a href="/page1">Page 1</a>
<a href="/page2">Page 2</a>
</body></html>`, r.Header.Get("X-Tab"))

		case "/search":
			if !loggedIn() {
				http.Error(w, "login required", http.StatusForbidden)
				return
			}
			body, _ := ioutil.ReadAll(r.Body)
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<html><head><title>Results</title></head><body>
<p id="payload">%s</p><p id="tab-header">%s</p></body></html>`, string(body), r.Header.Get("X-Tab"))

		case "/page1", "/page2":
			if !loggedIn() {
				http.Error(w, "login required", http.StatusForbidden)
				return
			}
			fmt.Fprintf(w, `<html><head><title>%s</title></head><body><h1>%s</h1>
<a href="/filters">back to filters</a></body></html>`, r.URL.Path, r.URL.Path)

		case "/go302":
			http.Redirect(w, r, "/page1", http.StatusFound)

		case "/refresh":
			// Every (re)load of this page is counted, so the test can tell
			// whether the meta-refresh timer keeps firing after Close.
			atomic.AddInt32(&refreshHits, 1)
			fmt.Fprint(w, `<html><head>
<meta http-equiv="refresh" content="1">
<title>Refresh</title></head><body>refresh me</body></html>`)

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &refreshHits
}

func loginBrowser(t *testing.T, ts *httptest.Server) *Browser {
	t.Helper()
	bow := newDefaultTestBrowser()
	values := url.Values{"user": {"alice"}, "pass": {"secret"}}
	if err := bow.PostForm(ts.URL+"/login", values); err != nil {
		t.Fatalf("post login: %v", err)
	}
	if bow.Title() != "Home" {
		t.Fatalf("login did not reach the home page, title=%q", bow.Title())
	}
	if err := bow.Open(ts.URL + "/filters"); err != nil {
		t.Fatalf("open filters: %v", err)
	}
	return bow
}

// TestIsolatedTabLoginSession verifies the whole regression: one login, then
// multiple independent collection branches that share the session cookie but
// own their DOM, forms, headers, attributes, history, timers and clients.
func TestIsolatedTabLoginSession(t *testing.T) {
	ts, _ := newTabTestServer(t)
	parent := loginBrowser(t, ts)

	tabA := parent.NewTabFromSnapshot()
	tabB := parent.NewTabFromSnapshot()

	// Every tab starts from the parent page snapshot.
	for name, tab := range map[string]*Browser{"A": tabA, "B": tabB} {
		if tab.Title() != "Filters" {
			t.Fatalf("tab %s did not start from the filters snapshot, title=%q", name, tab.Title())
		}
		if tab.Url().String() != ts.URL+"/filters" {
			t.Fatalf("tab %s snapshot URL mismatch: %s", name, tab.Url())
		}
	}

	// The cookie jar is shared, so the tabs are authenticated without a
	// second login.
	for name, tab := range map[string]*Browser{"A": tabA, "B": tabB} {
		if err := tab.Open(ts.URL + "/page1"); err != nil {
			t.Fatalf("tab %s request without shared session: %v", name, err)
		}
	}
	if parent.CookieJar() != tabA.CookieJar() || parent.CookieJar() != tabB.CookieJar() {
		t.Fatal("cookie jar must be shared between parent and tabs")
	}

	// Interleaved navigation: each tab keeps its own history.
	if err := tabB.Open(ts.URL + "/page2"); err != nil {
		t.Fatalf("tab B navigation: %v", err)
	}
	if !tabA.Back() {
		t.Fatal("tab A should be able to go back to its own snapshot")
	}
	if tabA.Title() != "Filters" {
		t.Fatalf("tab A back landed on %q, want the filters snapshot", tabA.Title())
	}
	if !tabB.Back() {
		t.Fatal("tab B should be able to go back to its previous page")
	}
	if tabB.Title() != "/page1" {
		t.Fatalf("tab B first back landed on %q, want /page1", tabB.Title())
	}
	if !tabB.Back() {
		t.Fatal("tab B should be able to go back to its own snapshot")
	}
	if tabB.Title() != "Filters" {
		t.Fatalf("tab B back landed on %q, want the filters snapshot", tabB.Title())
	}
	// A tab cannot walk back past its own snapshot into the parent history.
	if tabA.Back() {
		t.Fatal("tab A must not back into the parent's navigation history")
	}
	if tabB.Back() {
		t.Fatal("tab B must not back into the parent's navigation history")
	}
	// The parent is still sitting on its own page, untouched. Its history
	// reflects only its own two navigations (login, then the filters page).
	if parent.Title() != "Filters" || parent.HistoryJar().Len() != 2 {
		t.Fatalf("parent state was polluted by tab navigation: title=%q history=%d",
			parent.Title(), parent.HistoryJar().Len())
	}
	// After Back the snapshot becomes the current page again, so the stack
	// behind it is empty: there is nothing older than the tab's own snapshot.
	if tabA.HistoryJar().Len() != 0 {
		t.Fatalf("tab A history length = %d, want 0 after returning to snapshot", tabA.HistoryJar().Len())
	}
	if tabA.HistoryJar() == parent.HistoryJar() || tabA.HistoryJar() == tabB.HistoryJar() {
		t.Fatal("tabs must own independent history jars")
	}
}

// TestIsolatedTabDomAndFormMutations verifies DOM and form edits stay local.
func TestIsolatedTabDomAndFormMutations(t *testing.T) {
	ts, _ := newTabTestServer(t)
	parent := loginBrowser(t, ts)
	tab := parent.NewTabFromSnapshot()

	// DOM mutation in the tab does not reach the parent.
	tab.Find("#slot").SetAttr("id", "slot").SetText("tab-value")
	tab.Find("#slot").SetAttr("data-tab", "yes")
	if got := tab.Find("#slot").Text(); got != "tab-value" {
		t.Fatalf("tab DOM edit failed, got %q", got)
	}
	if got := parent.Find("#slot").Text(); got != "parent-value" {
		t.Fatalf("parent DOM was mutated by the tab, got %q", got)
	}
	if _, exists := parent.Find("#slot").Attr("data-tab"); exists {
		t.Fatal("parent DOM picked up an attribute added in the tab")
	}

	// Filling and submitting a form only changes the tab's own form values.
	form, err := tab.Form("#f")
	if err != nil {
		t.Fatalf("tab form: %v", err)
	}
	if err := form.Input("q", "tab-query"); err != nil {
		t.Fatalf("form input: %v", err)
	}
	if err := form.Click("go"); err != nil {
		t.Fatalf("form submit: %v", err)
	}
	if !strings.Contains(tab.Find("#payload").Text(), "q=tab-query") {
		t.Fatalf("tab search payload mismatch: %q", tab.Find("#payload").Text())
	}

	parentForm, err := parent.Form("#f")
	if err != nil {
		t.Fatalf("parent form: %v", err)
	}
	v, err := parentForm.Value("q")
	if err != nil || v != "default-q" {
		t.Fatalf("parent form value leaked from tab, got %q (err=%v)", v, err)
	}
	if parent.Title() != "Filters" {
		t.Fatalf("parent navigated when the tab submitted a form: %q", parent.Title())
	}
}

// TestIsolatedTabHeaders verifies headers and their backing slices are copied.
func TestIsolatedTabHeaders(t *testing.T) {
	ts, _ := newTabTestServer(t)
	parent := loginBrowser(t, ts)
	parent.AddRequestHeader("X-Tab", "parent")

	tab := parent.NewTabFromSnapshot()
	tab.AddRequestHeader("X-Tab", "child")
	tab.AddRequestHeader("X-Child-Only", "1")

	if got := parent.headers.Get("X-Tab"); got != "parent" {
		t.Fatalf("parent header overwritten by tab, got %q", got)
	}
	if _, exists := parent.headers["X-Child-Only"]; exists {
		t.Fatal("parent received a header added only in the tab")
	}

	if err := tab.Open(ts.URL + "/filters"); err != nil {
		t.Fatalf("tab request: %v", err)
	}
	if got := tab.Find("#who").Text(); got != "child" {
		t.Fatalf("server did not see the tab header, got %q", got)
	}
	if err := parent.Open(ts.URL + "/filters"); err != nil {
		t.Fatalf("parent request: %v", err)
	}
	if got := parent.Find("#who").Text(); got != "parent" {
		t.Fatalf("server did not keep seeing the parent header, got %q", got)
	}
}

// TestIsolatedTabAttributes verifies attributes and the redirect policy are
// independent once a tab has been created.
func TestIsolatedTabAttributes(t *testing.T) {
	ts, _ := newTabTestServer(t)
	parent := loginBrowser(t, ts)
	tab := parent.NewTabFromSnapshot()

	tab.SetAttribute(FollowRedirects, false)
	if !parent.attributes[FollowRedirects] {
		t.Fatal("disabling redirects in the tab changed the parent attribute")
	}
	err := tab.Open(ts.URL + "/go302")
	if err == nil || !strings.Contains(err.Error(), "Redirects are disabled") {
		t.Fatalf("tab expected a redirect-disabled error, got %v", err)
	}
	// Failed navigation must not replace the snapshot or touch the parent.
	if tab.Title() != "Filters" {
		t.Fatalf("failed navigation replaced the tab snapshot with title %q", tab.Title())
	}
	if err := parent.Open(ts.URL + "/go302"); err != nil {
		t.Fatalf("parent redirect policy leaked from tab: %v", err)
	}
	if parent.Title() != "/page1" {
		t.Fatalf("parent did not follow the redirect, title=%q", parent.Title())
	}
}

// TestIsolatedTabClientConfig verifies HTTP client config is independent:
// the initial transport is shared, but replacing it or changing the timeout
// in a tab never affects the parent.
func TestIsolatedTabClientConfig(t *testing.T) {
	ts, _ := newTabTestServer(t)
	parent := loginBrowser(t, ts)
	parentTransport := parent.client.Transport
	parent.SetTimeout(5 * time.Second)

	tab := parent.NewTabFromSnapshot()
	if tab.client == parent.client {
		t.Fatal("tabs must own a separate *http.Client instance")
	}
	if tab.client.Transport != parentTransport {
		t.Fatal("the initial transport should be shared with the parent")
	}
	if tab.client.Timeout != 5*time.Second {
		t.Fatalf("tab timeout = %s, want the parent's initial timeout", tab.client.Timeout)
	}
	if tab.CookieJar() != parent.CookieJar() {
		t.Fatal("the new client must still use the shared cookie jar")
	}

	tab.SetTransport(http.DefaultTransport)
	tab.SetTimeout(50 * time.Millisecond)
	if parent.client.Transport != parentTransport {
		t.Fatal("tab SetTransport replaced the parent's transport")
	}
	if parent.client.Timeout != 5*time.Second {
		t.Fatalf("tab SetTimeout changed the parent timeout to %s", parent.client.Timeout)
	}
	// The parent still works after the tab reconfigured its own client.
	if err := parent.Open(ts.URL + "/page1"); err != nil {
		t.Fatalf("parent stopped working after tab client reconfiguration: %v", err)
	}
}

// TestIsolatedTabNavigationFailure verifies a failed request in one tab does
// not pollute the other tab or the parent.
func TestIsolatedTabNavigationFailure(t *testing.T) {
	ts, _ := newTabTestServer(t)
	parent := loginBrowser(t, ts)
	tabA := parent.NewTabFromSnapshot()
	tabB := parent.NewTabFromSnapshot()

	// A 404 is a valid HTTP response in net/http, so use a server that is
	// closed up front to produce a deterministic connection failure.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	before := tabB.HistoryJar().Len()
	if err := tabA.Open(deadURL + "/unreachable"); err == nil {
		t.Fatal("expected a network error navigating to a closed server")
	}
	// The tab keeps its snapshot after the failure.
	if tabA.Title() != "Filters" {
		t.Fatalf("failed navigation overwrote tab A snapshot: %q", tabA.Title())
	}
	if tabA.HistoryJar().Len() != 1 {
		t.Fatalf("failed navigation altered tab A history: len=%d", tabA.HistoryJar().Len())
	}
	if tabB.HistoryJar().Len() != before || tabB.Title() != "Filters" {
		t.Fatal("tab B was polluted by tab A's failed request")
	}
	if parent.Title() != "Filters" {
		t.Fatal("parent was polluted by a tab's failed request")
	}
	// The failing tab recovers and keeps working.
	if err := tabA.Open(ts.URL + "/page2"); err != nil {
		t.Fatalf("tab A could not recover after a failed request: %v", err)
	}
	if !tabA.Back() || tabA.Title() != "Filters" {
		t.Fatal("tab A lost its snapshot after recovering")
	}
}

// TestIsolatedTabWithoutCurrentPage verifies tabs can be created and
// navigated even when no page has been loaded yet.
func TestIsolatedTabWithoutCurrentPage(t *testing.T) {
	ts, _ := newTabTestServer(t)
	empty := newDefaultTestBrowser()
	tab := empty.NewTabFromSnapshot()

	if tab.HistoryJar().Len() != 1 {
		t.Fatalf("tab from an empty browser must start with one seed state, got %d", tab.HistoryJar().Len())
	}
	if tab.Url() != nil {
		t.Fatalf("tab from an empty browser must have no URL, got %s", tab.Url())
	}
	if tab.CookieJar() != empty.CookieJar() {
		t.Fatal("the cookie jar should still be shared even without a page")
	}

	values := url.Values{"user": {"alice"}, "pass": {"secret"}}
	if err := tab.PostForm(ts.URL+"/login", values); err != nil {
		t.Fatalf("login from an empty tab: %v", err)
	}
	if tab.Title() != "Home" {
		t.Fatalf("empty tab did not navigate, title=%q", tab.Title())
	}
	// The seed state moves into the history behind the first loaded page.
	if tab.HistoryJar().Len() != 1 {
		t.Fatalf("history after first navigation = %d, want 1", tab.HistoryJar().Len())
	}
	if !tab.Back() {
		t.Fatal("the empty tab should be able to return to its blank seed state")
	}
	if tab.Url() != nil {
		t.Fatalf("back did not restore the blank state, url=%s", tab.Url())
	}
	// The parent browser is still blank.
	if empty.Url() != nil {
		t.Fatal("navigating the empty tab polluted its parent browser")
	}
}

// TestIsolatedTabClose verifies Close only stops the tab's own refresh timer,
// never closes the shared transport or clears the shared cookies.
func TestIsolatedTabClose(t *testing.T) {
	ts, refreshHits := newTabTestServer(t)
	parent := loginBrowser(t, ts)
	tab := parent.NewTabFromSnapshot()

	// Opening the refresh page arms a one-second meta refresh, which should
	// fire once as a control before we close the tab.
	if err := tab.Open(ts.URL + "/refresh"); err != nil {
		t.Fatalf("open refresh page: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	if atomic.LoadInt32(refreshHits) < 2 {
		t.Fatalf("meta refresh reload never fired, hits=%d", atomic.LoadInt32(refreshHits))
	}

	tab.Close()
	tab.Close() // must be safe to call repeatedly
	snapshot := atomic.LoadInt32(refreshHits)
	time.Sleep(1500 * time.Millisecond)
	if got := atomic.LoadInt32(refreshHits); got != snapshot {
		t.Fatalf("Close did not stop the tab refresh timer: hits %d -> %d", snapshot, got)
	}

	// Shared cookies survive closing the tab.
	u, _ := url.Parse(ts.URL)
	if cookies := parent.CookieJar().Cookies(u); len(cookies) == 0 {
		t.Fatal("closing the tab cleared the shared cookie jar")
	}
	// The parent keeps browsing through the shared transport.
	if err := parent.Open(ts.URL + "/page2"); err != nil {
		t.Fatalf("parent stopped working after the tab closed: %v", err)
	}
	if parent.Title() != "/page2" {
		t.Fatalf("parent navigation after tab close failed, title=%q", parent.Title())
	}
}

// TestNewTabKeepsLegacyBehaviour guards the existing shallow-copy NewTab.
func TestNewTabKeepsLegacyBehaviour(t *testing.T) {
	parent := newDefaultTestBrowser()
	parent.SetUserAgent("legacy-ua")
	tab := parent.NewTab()

	if tab == parent {
		t.Fatal("NewTab must return a new instance")
	}
	if tab.userAgent != "legacy-ua" {
		t.Fatal("NewTab must copy the user agent")
	}
	if tab.CookieJar() != parent.CookieJar() {
		t.Fatal("legacy NewTab must keep sharing the cookie jar")
	}
	if tab.HistoryJar() != parent.HistoryJar() {
		t.Fatal("legacy NewTab must keep sharing the history jar")
	}
	if tab.client != parent.client {
		t.Fatal("legacy NewTab must keep sharing the HTTP client")
	}
}
