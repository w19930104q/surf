package browser

import (
	"net/http"
	"net/url"

	"github.com/PuerkitoBio/goquery"
	"github.com/headzoo/surf/jar"
)

// NewTabFromSnapshot creates a new, explicitly isolated browser tab.
//
// The returned tab starts from a snapshot of the current page: the page DOM,
// request, response and body are deep-copied, so form filling, DOM mutations
// and attribute changes made in the tab never reach the parent tab.
//
// The cookie jar and the bookmarks jar are shared, which is what keeps a login
// session alive across tabs. Everything else is owned by the new tab:
//
//   - DOM, page body and form modifications (deep-copied snapshot)
//   - request headers and their underlying slices (deep-copied)
//   - attributes and the redirect policy (independent AttributeMap)
//   - navigation history (a new history seeded only with the snapshot)
//   - meta-refresh timers (a tab closed with Close only stops its own timer)
//   - the HTTP client (a new *http.Client; the initial Transport and the
//     CookieJar are shared, but calling SetTransport/SetTimeout on the tab
//     never mutates the parent client)
//
// The method works even when no page has been loaded yet. The returned tab
// starts with a blank snapshot and a single blank history entry, and it can
// be navigated with Open and the other request methods straight away.
func (bow *Browser) NewTabFromSnapshot() *Browser {
	// Compute the snapshot once: the same state both seeds the history and
	// becomes the tab's current page.
	snapshot := bow.snapshotState()

	tab := &Browser{
		// userAgent is a string and therefore copied by value.
		userAgent: bow.userAgent,

		// Session-level jars stay shared so the login session is visible to
		// every tab.
		bookmarks: bow.bookmarks,

		// Independent request headers, including the backing slices.
		headers: cloneHeader(bow.headers),

		// Independent attributes / redirect policy.
		attributes: cloneAttributes(bow.attributes),

		// Independent navigation history seeded with the page snapshot.
		history: newSeededHistory(bow.history, snapshot),

		// Snapshot of the current page. Deep-copied.
		state: snapshot,

		// Independent raw page body (used by Download).
		body: cloneBytes(bow.body),

		// relativeUrl is parsed from a string; copy the pointer so later
		// <base> handling in a tab does not mutate the parent's URL.
		relativeUrl: cloneURL(bow.relativeUrl),

		// Fresh cancellation channel for this tab's refresh goroutines.
		closed: make(chan struct{}),

		// Enables Back-to-snapshot semantics for this tab.
		snapshotTab: true,
	}

	tab.client = tab.buildIsolatedClient(bow.client)
	return tab
}

// Close stops the tab.
//
// Only the tab's own meta-refresh timer (if any) is stopped. The shared
// HTTP Transport is not closed, and the shared cookie jar is not cleared, so
// other tabs keep working and retain the login session. Close is safe to
// call multiple times and on tabs that never loaded a page.
func (bow *Browser) Close() {
	bow.closeOnce.Do(func() {
		if bow.closed != nil {
			close(bow.closed)
		}
		if bow.refresh != nil {
			bow.refresh.Stop()
		}
	})
}

// done returns the channel closed when the tab is closed. A nil channel
// blocks forever, which is the correct behaviour for legacy tabs created
// through NewTab that never received an explicit cancellation channel.
func (bow *Browser) done() <-chan struct{} {
	return bow.closed
}

// InitCloseChannel creates the cancellation channel used by Close and by
// meta-refresh goroutines. It is called by surf.NewBrowser and by
// NewTabFromSnapshot; calling it again replaces the channel, which is only
// useful for browsers assembled manually through the jar setters.
func (bow *Browser) InitCloseChannel() {
	if bow.closed == nil {
		bow.closed = make(chan struct{})
	}
}

// snapshotState returns a deep copy of the current page state. A tab working
// on the copy can never mutate the parent DOM, request or response.
func (bow *Browser) snapshotState() *jar.State {
	if bow.state == nil {
		return &jar.State{}
	}
	return &jar.State{
		Request:  cloneRequest(bow.state.Request),
		Response: cloneResponse(bow.state.Response),
		Dom:      cloneDom(bow.state.Dom),
	}
}

// buildIsolatedClient creates the tab-owned HTTP client. The initial
// Transport and CookieJar are shared with the parent, but the client itself,
// its Timeout and its CheckRedirect callback are independent, so replacing
// the transport or the timeout on the tab cannot affect the parent.
func (bow *Browser) buildIsolatedClient(parent *http.Client) *http.Client {
	client := bow.buildClient()
	if parent == nil {
		return client
	}
	client.Transport = parent.Transport
	client.Jar = parent.Jar
	client.Timeout = parent.Timeout
	return client
}

// newSeededHistory creates an independent history carrying a single entry:
// the snapshot the tab starts from. An empty browser yields a tab whose
// history holds an empty state, mirroring the way a fresh browser treats its
// first request. The parent history limit is preserved.
func newSeededHistory(parent jar.History, snapshot *jar.State) jar.History {
	history := jar.NewMemoryHistory()
	// Preserve the parent's history limit when it supports reading it back,
	// without requiring every custom History implementation to expose Max.
	if maxed, ok := parent.(interface{ Max() int }); ok {
		history.SetMax(maxed.Max())
	}
	history.Push(snapshot)
	return history
}

func cloneAttributes(src AttributeMap) AttributeMap {
	dst := make(AttributeMap, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneBytes(src []byte) []byte {
	if src == nil {
		return nil
	}
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}

func cloneURL(src *url.URL) *url.URL {
	if src == nil {
		return nil
	}
	copy := *src
	return &copy
}

// cloneHeader returns a deep copy of an http.Header, including the backing
// string slices, so AddRequestHeader/DelRequestHeader in one tab never leak
// into another tab.
func cloneHeader(src http.Header) http.Header {
	if src == nil {
		return nil
	}
	dst := make(http.Header, len(src))
	for k, vs := range src {
		dst[k] = append([]string(nil), vs...)
	}
	return dst
}

// cloneRequest returns a copy of req with independent headers.
func cloneRequest(req *http.Request) *http.Request {
	if req == nil {
		return nil
	}
	copy := req.Clone(req.Context())
	copy.Header = cloneHeader(req.Header)
	return copy
}

// cloneResponse returns a shallow-independent copy of resp: a new response
// value and header map sharing only the already-consumed Body field, which
// surf never reads again after a page has been loaded.
func cloneResponse(resp *http.Response) *http.Response {
	if resp == nil {
		return nil
	}
	copy := *resp
	copy.Header = cloneHeader(resp.Header)
	if resp.Request != nil {
		copy.Request = cloneRequest(resp.Request)
	}
	return &copy
}

// cloneDom returns an independent goquery document whose selection tree is
// detached from the source, so DOM/attribute mutations stay inside the tab.
func cloneDom(dom *goquery.Document) *goquery.Document {
	if dom == nil {
		return nil
	}
	return goquery.NewDocumentFromNode(dom.Selection.Clone().Nodes[0])
}
