package browser

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"fmt"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/headzoo/surf/errors"
	"github.com/headzoo/surf/jar"
)

// Attribute represents a Browser capability.
type Attribute int

// AttributeMap represents a map of Attribute values.
type AttributeMap map[Attribute]bool

// File represents a input type file, that includes the fileName and a io.reader
type File struct {
	fileName string
	data     io.Reader
}

// FileSet represents a map of files used to port multipart
type FileSet map[string]*File

const (
	// SendReferer instructs a Browser to send the Referer header.
	SendReferer Attribute = iota

	// MetaRefreshHandling instructs a Browser to handle the refresh meta tag.
	MetaRefreshHandling

	// FollowRedirects instructs a Browser to follow Location headers.
	FollowRedirects
)

// InitialAssetsSliceSize is the initial size when allocating a slice of page
// assets. Increasing this size may lead to a very small performance increase
// when downloading assets from a page with a lot of assets.
var InitialAssetsSliceSize = 20

// Browsable represents an HTTP web browser.
type Browsable interface {
	// SetUserAgent sets the user agent.
	SetUserAgent(ua string)

	// SetAttribute sets a browser instruction attribute.
	SetAttribute(a Attribute, v bool)

	// SetAttributes is used to set all the browser attributes.
	SetAttributes(a AttributeMap)

	// SetState sets the init browser state.
	SetState(sj *jar.State)

	// State returns the browser state.
	State() *jar.State

	// SetBookmarksJar sets the bookmarks jar the browser uses.
	SetBookmarksJar(bj jar.BookmarksJar)

	// BookmarksJar returns the bookmarks jar the browser uses.
	BookmarksJar() jar.BookmarksJar

	// SetCookieJar is used to set the cookie jar the browser uses.
	SetCookieJar(cj http.CookieJar)

	// CookieJar returns the cookie jar the browser uses.
	CookieJar() http.CookieJar

	// SetHistoryJar is used to set the history jar the browser uses.
	SetHistoryJar(hj jar.History)

	// HistoryJar returns the history jar the browser uses.
	HistoryJar() jar.History

	// SetHeadersJar sets the headers the browser sends with each request.
	SetHeadersJar(h http.Header)

	// SetTimeout sets the timeout for requests.
	SetTimeout(t time.Duration)

	// SetTransport sets the http library transport mechanism for each request.
	SetTransport(rt http.RoundTripper)

	// AddRequestHeader adds a header the browser sends with each request.
	AddRequestHeader(name, value string)

	// Open requests the given URL using the GET method.
	Open(url string) error

	// Open requests the given URL using the HEAD method.
	Head(url string) error

	// OpenForm appends the data values to the given URL and sends a GET request.
	OpenForm(url string, data url.Values) error

	// OpenBookmark calls Get() with the URL for the bookmark with the given name.
	OpenBookmark(name string) error

	// Post requests the given URL using the POST method.
	Post(url string, contentType string, body io.Reader) error

	// PostForm requests the given URL using the POST method with the given data.
	PostForm(url string, data url.Values) error

	// PostMultipart requests the given URL using the POST method with the given data using multipart/form-data format.
	PostMultipart(u string, fields url.Values, files FileSet) error

	// Back loads the previously requested page.
	Back() bool

	// Reload duplicates the last successful request.
	Reload() error

	// Bookmark saves the page URL in the bookmarks with the given name.
	Bookmark(name string) error

	// Click clicks on the page element matched by the given expression.
	Click(expr string) error

	// Form returns the form in the current page that matches the given expr.
	Form(expr string) (Submittable, error)

	// Forms returns an array of every form in the page.
	Forms() []Submittable

	// Links returns an array of every link found in the page.
	Links() []*Link

	// Images returns an array of every image found in the page.
	Images() []*Image

	// Stylesheets returns an array of every stylesheet linked to the document.
	Stylesheets() []*Stylesheet

	// Scripts returns an array of every script linked to the document.
	Scripts() []*Script

	// SiteCookies returns the cookies for the current site.
	SiteCookies() []*http.Cookie

	// ResolveUrl returns an absolute URL for a possibly relative URL.
	ResolveUrl(u *url.URL) *url.URL

	// ResolveStringUrl works just like ResolveUrl, but the argument and return value are strings.
	ResolveStringUrl(u string) (string, error)

	// Download writes the contents of the document to the given writer.
	Download(o io.Writer) (int64, error)

	// Url returns the page URL as a string.
	Url() *url.URL

	// StatusCode returns the response status code.
	StatusCode() int

	// Title returns the page title.
	Title() string

	// ResponseHeaders returns the page headers.
	ResponseHeaders() http.Header

	// Body returns the page body as a string of html.
	Body() string

	// Dom returns the inner *goquery.Selection.
	Dom() *goquery.Selection

	// Find returns the dom selections matching the given expression.
	Find(expr string) *goquery.Selection

	// Create a new Browser instance and inherit the configuration
	// Read more: https://github.com/headzoo/surf/issues/23
	NewTab() (b *Browser)
}

// Browser is the default Browser implementation.
type Browser struct {
	// HTTP client
	client *http.Client

	// state is the current browser state.
	state *jar.State

	// userAgent is the User-Agent header value sent with requests.
	userAgent string

	// bookmarks stores the saved bookmarks.
	bookmarks jar.BookmarksJar

	// history stores the visited pages.
	history jar.History

	// headers are additional headers to send with each request.
	headers http.Header

	// attributes is the set browser attributes.
	attributes AttributeMap

	// refresh is a timer used to meta refresh pages.
	refresh *time.Timer

	// refreshMu guards refresh and refreshGen. refreshGen is bumped every
	// time the current page changes (new request, Back or Reload); a refresh
	// goroutine only navigates when its generation is still current, which
	// keeps a timer scheduled by an old page from re-navigating after the
	// browser has moved on.
	refreshMu  sync.Mutex
	refreshGen uint64
}

// buildClient instanciates the *http.Client used by the browser
func (bow *Browser) buildClient() *http.Client {
	return &http.Client{
		CheckRedirect: bow.shouldRedirect,
	}
}

// Open requests the given URL using the GET method.
func (bow *Browser) Open(u string) error {
	ur, err := url.Parse(u)
	if err != nil {
		return err
	}
	return bow.httpGET(ur, nil)
}

// Head requests the given URL using the HEAD method.
func (bow *Browser) Head(u string) error {
	ur, err := url.Parse(u)
	if err != nil {
		return err
	}
	return bow.httpHEAD(ur, nil)
}

// OpenForm appends the data values to the given URL and sends a GET request.
func (bow *Browser) OpenForm(u string, data url.Values) error {
	ul, err := url.Parse(u)
	if err != nil {
		return err
	}
	ul.RawQuery = data.Encode()

	return bow.Open(ul.String())
}

// OpenBookmark calls Open() with the URL for the bookmark with the given name.
func (bow *Browser) OpenBookmark(name string) error {
	url, err := bow.bookmarks.Read(name)
	if err != nil {
		return err
	}
	return bow.Open(url)
}

// Post requests the given URL using the POST method.
func (bow *Browser) Post(u string, contentType string, body io.Reader) error {
	ur, err := url.Parse(u)
	if err != nil {
		return err
	}
	return bow.httpPOST(ur, bow.Url(), contentType, body)
}

// PostForm requests the given URL using the POST method with the given data.
func (bow *Browser) PostForm(u string, data url.Values) error {
	return bow.Post(u, "application/x-www-form-urlencoded", strings.NewReader(data.Encode()))
}

// PostMultipart requests the given URL using the POST method with the given data using multipart/form-data format.
func (bow *Browser) PostMultipart(u string, fields url.Values, files FileSet) error {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	for k, vs := range fields {
		for _, v := range vs {
			writer.WriteField(k, v)
		}
	}
	for k, file := range files {
		fw, err := writer.CreateFormFile(k, file.fileName)
		if err != nil {
			return err
		}
		if file.data != nil {
			_, err = io.Copy(fw, file.data)
			if err != nil {
				return err
			}
		}
	}
	err := writer.Close()
	if err != nil {
		return err

	}
	return bow.Post(u, writer.FormDataContentType(), body)
}

// Back loads the previously requested page.
//
// Back restores the complete state of the previous page (URL, status code,
// response headers, DOM, downloaded bytes and effective base URL) from the
// history without issuing an HTTP request. Any meta refresh timer scheduled
// by the current page is cancelled, so it cannot navigate after the move.
//
// Returns a boolean value indicating whether a previous page existed, and was
// successfully loaded.
func (bow *Browser) Back() bool {
	if bow.history.Len() == 0 {
		return false
	}
	prev := bow.history.Pop()
	// Only fully loaded pages are pushed by the browser, but defend against
	// hand-built history jars: never install a state that cannot be rendered.
	if prev == nil || prev.Response == nil {
		return false
	}

	// Cancel the refresh timer of the page we are leaving and invalidate any
	// goroutine waiting on it.
	bow.cancelRefresh()
	bow.state = prev
	return true
}

// Reload duplicates the last successful request.
func (bow *Browser) Reload() error {
	if bow.state.Request != nil {
		return bow.httpRequest(bow.state.Request)
	}
	return errors.NewPageNotLoaded("Cannot reload, the previous request failed.")
}

// Bookmark saves the page URL in the bookmarks with the given name.
func (bow *Browser) Bookmark(name string) error {
	return bow.bookmarks.Save(name, bow.ResolveUrl(bow.Url()).String())
}

// Click clicks on the page element matched by the given expression.
//
// Currently this is only useful for click on links, which will cause the browser
// to load the page pointed at by the link. Future versions of Surf may support
// JavaScript and clicking on elements will fire the click event.
func (bow *Browser) Click(expr string) error {
	sel := bow.Find(expr)
	if sel.Length() == 0 {
		return errors.NewElementNotFound(
			"Element not found matching expr '%s'.", expr)
	}
	if !sel.Is("a") {
		return errors.NewElementNotFound(
			"Expr '%s' must match an anchor tag.", expr)
	}

	href, err := bow.attrToResolvedUrl("href", sel)
	if err != nil {
		return err
	}

	return bow.httpGET(href, bow.Url())
}

// Form returns the form in the current page that matches the given expr.
func (bow *Browser) Form(expr string) (Submittable, error) {
	sel := bow.Find(expr)
	if sel.Length() == 0 {
		return nil, errors.NewElementNotFound(
			"Form not found matching expr '%s'.", expr)
	}
	if !sel.Is("form") {
		return nil, errors.NewElementNotFound(
			"Expr '%s' does not match a form tag.", expr)
	}

	return NewForm(bow, sel), nil
}

// Forms returns an array of every form in the page.
func (bow *Browser) Forms() []Submittable {
	sel := bow.Find("form")
	len := sel.Length()
	if len == 0 {
		return nil
	}

	forms := make([]Submittable, len)
	sel.Each(func(_ int, s *goquery.Selection) {
		forms = append(forms, NewForm(bow, s))
	})
	return forms
}

// Links returns an array of every link found in the page.
func (bow *Browser) Links() []*Link {
	links := make([]*Link, 0, InitialAssetsSliceSize)
	bow.Find("a").Each(func(_ int, s *goquery.Selection) {
		href, err := bow.attrToResolvedUrl("href", s)
		if err == nil {
			links = append(links, NewLinkAsset(
				href,
				bow.attrOrDefault("id", "", s),
				s.Text(),
			))
		}
	})

	return links
}

// Images returns an array of every image found in the page.
func (bow *Browser) Images() []*Image {
	images := make([]*Image, 0, InitialAssetsSliceSize)
	bow.Find("img").Each(func(_ int, s *goquery.Selection) {
		src, err := bow.attrToResolvedUrl("src", s)
		if err == nil {
			images = append(images, NewImageAsset(
				src,
				bow.attrOrDefault("id", "", s),
				bow.attrOrDefault("alt", "", s),
				bow.attrOrDefault("title", "", s),
			))
		}
	})

	return images
}

// Stylesheets returns an array of every stylesheet linked to the document.
func (bow *Browser) Stylesheets() []*Stylesheet {
	stylesheets := make([]*Stylesheet, 0, InitialAssetsSliceSize)
	bow.Find("link").Each(func(_ int, s *goquery.Selection) {
		rel, ok := s.Attr("rel")
		if ok && rel == "stylesheet" {
			href, err := bow.attrToResolvedUrl("href", s)
			if err == nil {
				stylesheets = append(stylesheets, NewStylesheetAsset(
					href,
					bow.attrOrDefault("id", "", s),
					bow.attrOrDefault("media", "all", s),
					bow.attrOrDefault("type", "text/css", s),
				))
			}
		}
	})

	return stylesheets
}

// Scripts returns an array of every script linked to the document.
func (bow *Browser) Scripts() []*Script {
	scripts := make([]*Script, 0, InitialAssetsSliceSize)
	bow.Find("script").Each(func(_ int, s *goquery.Selection) {
		src, err := bow.attrToResolvedUrl("src", s)
		if err == nil {
			scripts = append(scripts, NewScriptAsset(
				src,
				bow.attrOrDefault("id", "", s),
				bow.attrOrDefault("type", "text/javascript", s),
			))
		}
	})

	return scripts
}

// SiteCookies returns the cookies for the current site.
func (bow *Browser) SiteCookies() []*http.Cookie {
	if bow.client == nil {
		bow.client = bow.buildClient()
	}
	return bow.client.Jar.Cookies(bow.Url())
}

// SetState sets the browser state.
func (bow *Browser) SetState(sj *jar.State) {
	bow.state = sj
}

// State returns the browser state.
func (bow *Browser) State() *jar.State {
	return bow.state
}

// SetCookieJar is used to set the cookie jar the browser uses.
func (bow *Browser) SetCookieJar(cj http.CookieJar) {
	if bow.client == nil {
		bow.client = bow.buildClient()
	}
	bow.client.Jar = cj
}

// CookieJar returns the cookie jar the browser uses.
func (bow *Browser) CookieJar() http.CookieJar {
	if bow.client == nil {
		bow.client = bow.buildClient()
	}
	return bow.client.Jar
}

// SetUserAgent sets the user agent.
func (bow *Browser) SetUserAgent(userAgent string) {
	bow.userAgent = userAgent
}

// SetAttribute sets a browser instruction attribute.
func (bow *Browser) SetAttribute(a Attribute, v bool) {
	bow.attributes[a] = v
}

// SetAttributes is used to set all the browser attributes.
func (bow *Browser) SetAttributes(a AttributeMap) {
	bow.attributes = a
}

// SetBookmarksJar sets the bookmarks jar the browser uses.
func (bow *Browser) SetBookmarksJar(bj jar.BookmarksJar) {
	bow.bookmarks = bj
}

// BookmarksJar returns the bookmarks jar the browser uses.
func (bow *Browser) BookmarksJar() jar.BookmarksJar {
	return bow.bookmarks
}

// SetHistoryJar is used to set the history jar the browser uses.
func (bow *Browser) SetHistoryJar(hj jar.History) {
	bow.history = hj
}

// HistoryJar returns the history jar the browser uses.
func (bow *Browser) HistoryJar() jar.History {
	return bow.history
}

// SetHeadersJar sets the headers the browser sends with each request.
func (bow *Browser) SetHeadersJar(h http.Header) {
	bow.headers = h
}

// SetTransport sets the http library transport mechanism for each request.
// SetTimeout sets the timeout for requests.
func (bow *Browser) SetTimeout(t time.Duration) {
	if bow.client == nil {
		bow.client = bow.buildClient()
	}
	bow.client.Timeout = t
}

// SetTransport sets the http library transport mechanism for each request.
func (bow *Browser) SetTransport(rt http.RoundTripper) {
	if bow.client == nil {
		bow.client = bow.buildClient()
	}
	bow.client.Transport = rt
}

// AddRequestHeader sets a header the browser sends with each request.
func (bow *Browser) AddRequestHeader(name, value string) {
	bow.headers.Set(name, value)
}

// DelRequestHeader deletes a header so the browser will not send it with future requests.
func (bow *Browser) DelRequestHeader(name string) {
	bow.headers.Del(name)
}

// ResolveUrl returns an absolute URL for a possibly relative URL.
func (bow *Browser) ResolveUrl(u *url.URL) *url.URL {
	return bow.RelativeUrl().ResolveReference(u)
}

// ResolveStringUrl works just like ResolveUrl, but the argument and return value are strings.
func (bow *Browser) ResolveStringUrl(u string) (string, error) {
	pu, err := url.Parse(u)
	if err != nil {
		return "", err
	}
	pu = bow.RelativeUrl().ResolveReference(pu)
	return pu.String(), nil
}

// RelativeUrl returns URL relative to which all the others work.
//
// The effective base URL is part of the loaded page state, so navigating
// Back restores it. A page without a usable <base> tag resolves relative
// links against its own URL rather than inheriting the base of a previously
// visited page.
func (bow *Browser) RelativeUrl() *url.URL {
	if bow.state != nil && bow.state.Base != nil {
		return bow.state.Base
	}
	return bow.Url()
}

// Download writes the contents of the document to the given writer.
//
// The bytes returned are the decoded response body captured when the current
// page was loaded, so Download after Back serves the historical page rather
// than any page visited since.
func (bow *Browser) Download(o io.Writer) (int64, error) {
	if bow.state == nil || bow.state.Body == nil {
		return 0, errors.NewPageNotLoaded(
			"The raw contents of this page are not available. States created with " +
				"SetState do not store response bytes, so Download cannot serve them.")
	}
	buff := bytes.NewBuffer(bow.state.Body)
	return io.Copy(o, buff)
}

// Url returns the page URL as a string.
func (bow *Browser) Url() *url.URL {
	if bow.state.Response == nil {
		// there is a possibility that we issued a request, but for
		// whatever reason the request failed.
		if bow.state.Request != nil {
			return bow.state.Request.URL
		}
		return nil
	}

	return bow.state.Response.Request.URL
}

// StatusCode returns the response status code.
func (bow *Browser) StatusCode() int {
	return bow.state.Response.StatusCode
}

// Title returns the page title.
func (bow *Browser) Title() string {
	return bow.state.Dom.Find("title").Text()
}

// ResponseHeaders returns the page headers.
func (bow *Browser) ResponseHeaders() http.Header {
	return bow.state.Response.Header
}

// Body returns the page body as a string of html.
func (bow *Browser) Body() string {
	body, _ := bow.state.Dom.Find("body").Html()
	return body
}

// Dom returns the inner *goquery.Selection.
func (bow *Browser) Dom() *goquery.Selection {
	return bow.state.Dom.First()
}

// Find returns the dom selections matching the given expression.
func (bow *Browser) Find(expr string) *goquery.Selection {
	return bow.state.Dom.Find(expr)
}

func (bow *Browser) NewTab() (b *Browser) {
	// Copy the configuration and shared jars field by field. The mutex is
	// deliberately not copied: the tab gets its own guard and starts without
	// a pending refresh timer.
	b = &Browser{
		client:     bow.client,
		state:      bow.state,
		userAgent:  bow.userAgent,
		bookmarks:  bow.bookmarks,
		history:    bow.history,
		headers:    bow.headers,
		attributes: bow.attributes,
		refresh:    nil,
	}

	return b
}

// buildRequest creates and returns a *http.Request type.
// Sets any headers that need to be sent with the request.
func (bow *Browser) buildRequest(method, url string, ref *url.URL, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header = copyHeaders(bow.headers)

	if host := req.Header.Get("Host"); host != "" {
		req.Host = host
	}
	req.Header.Set("User-Agent", bow.userAgent)
	if bow.attributes[SendReferer] && ref != nil {
		req.Header.Set("Referer", ref.String())
	}
	if os.Getenv("SURF_DEBUG_HEADERS") != "" {
		d, _ := httputil.DumpRequest(req, false)
		fmt.Fprintln(os.Stderr, "===== [DUMP] =====\n", string(d))
	}

	return req, nil
}

func copyHeaders(h http.Header) http.Header {
	if h == nil {
		return nil
	}
	h2 := make(http.Header, len(h))
	for k, v := range h {
		h2[k] = v
	}
	return h2
}

// httpGET makes an HTTP GET request for the given URL.
// When via is not nil, and AttributeSendReferer is true, the Referer header will
// be set to ref.
func (bow *Browser) httpGET(u *url.URL, ref *url.URL) error {
	req, err := bow.buildRequest("GET", u.String(), ref, nil)
	if err != nil {
		return err
	}
	return bow.httpRequest(req)
}

// httpHEAD makes an HTTP HEAD request for the given URL.
// When via is not nil, and AttributeSendReferer is true, the Referer header will
// be set to ref.
func (bow *Browser) httpHEAD(u *url.URL, ref *url.URL) error {
	req, err := bow.buildRequest("HEAD", u.String(), ref, nil)
	if err != nil {
		return err
	}
	return bow.httpRequest(req)
}

// httpPOST makes an HTTP POST request for the given URL.
// When via is not nil, and AttributeSendReferer is true, the Referer header will
// be set to ref.
func (bow *Browser) httpPOST(u *url.URL, ref *url.URL, contentType string, body io.Reader) error {
	req, err := bow.buildRequest("POST", u.String(), ref, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)

	return bow.httpRequest(req)
}

// send uses the given *http.Request to make an HTTP request.
//
// On any network, decompression or read failure the current state, body and
// history are left untouched: the browser keeps showing the previous page and
// the failed request is not recorded. Only once the whole response has been
// read and parsed is the previous page pushed onto the history and the new
// page state installed atomically.
func (bow *Browser) httpRequest(req *http.Request) error {
	if bow.client == nil {
		bow.client = bow.buildClient()
	}
	bow.preSend()
	resp, err := bow.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var reader io.ReadCloser
	switch resp.Header.Get("Content-Encoding") {
	case "gzip":
		gzReader, gzErr := gzip.NewReader(resp.Body)
		if gzErr != nil {
			return gzErr
		}
		reader = gzReader
	case "deflate":
		// flate.NewReader never returns nil; stream corruption is reported
		// through Read and the Close checked below.
		reader = flate.NewReader(resp.Body)
	default:
		reader = &nopReadCloser{resp.Body}
	}

	body, err := ioutil.ReadAll(reader)
	// Flate reports corrupt streams through the reader's Close, so check both
	// Read and Close errors instead of silently swallowing bad compression.
	if closeErr := reader.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return err
	}

	dom, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return err
	}

	base := bow.resolveBase(resp.Request.URL, dom)

	// Everything above succeeded: only now mutate the browser state.
	if bow.state != nil && bow.state.Response != nil {
		bow.history.Push(bow.state)
	}
	bow.state = jar.NewPageState(req, resp, dom, body, base)
	bow.postSend()

	return nil
}

// nopReadCloser wraps an io.Reader with a no-op Close so every decoding
// branch exposes the same io.ReadCloser interface.
type nopReadCloser struct {
	io.Reader
}

func (nopReadCloser) Close() error { return nil }

// resolveBase determines the effective base URL of a freshly parsed page.
//
// Base resolution is performed for every page regardless of the
// MetaRefreshHandling attribute: the two features are independent. When the
// document has no usable <base href> the page URL is returned, which means a
// page without a base never inherits the base of a previously visited page.
func (bow *Browser) resolveBase(pageURL *url.URL, dom *goquery.Document) *url.URL {
	base := pageURL
	sel := dom.Find("base[href]").First()
	if sel.Length() > 0 {
		if href, ok := sel.Attr("href"); ok {
			if baseURL, err := url.Parse(href); err == nil {
				base = pageURL.ResolveReference(baseURL)
			}
		}
	}
	return base
}

// preSend sets browser state before sending a request.
//
// The refresh timer of the page being left is cancelled and its generation
// invalidated, so its goroutine can never navigate away from the new page.
func (bow *Browser) preSend() {
	bow.cancelRefresh()
}

// cancelRefresh stops any pending meta refresh timer and bumps the refresh
// generation, invalidating goroutines still waiting on an older timer.
func (bow *Browser) cancelRefresh() {
	bow.refreshMu.Lock()
	defer bow.refreshMu.Unlock()
	if bow.refresh != nil {
		bow.refresh.Stop()
		bow.refresh = nil
	}
	bow.refreshGen++
}

// postSend sets browser state after sending a request.
//
// The <base> tag is handled separately during state construction in
// resolveBase, so it is independent of MetaRefreshHandling. Only the meta
// refresh timer itself is gated by the attribute.
func (bow *Browser) postSend() {
	if !(isContentTypeHtml(bow.state.Response) && bow.attributes[MetaRefreshHandling]) {
		return
	}
	sel := bow.Find("meta[http-equiv='refresh']")
	if sel.Length() == 0 {
		return
	}
	attr, ok := sel.Attr("content")
	if !ok {
		return
	}

	// Accept both the bare form ("5") and the URL form ("5; url=/next").
	durPart := attr
	if semi := strings.Index(attr, ";"); semi >= 0 {
		durPart = strings.TrimSpace(attr[:semi])
	}
	dur, err := time.ParseDuration(durPart + "s")
	if err != nil {
		return
	}

	bow.refreshMu.Lock()
	bow.refresh = time.NewTimer(dur)
	timer := bow.refresh
	gen := bow.refreshGen
	bow.refreshMu.Unlock()

	go func() {
		<-timer.C
		bow.refreshMu.Lock()
		current := bow.refreshGen
		bow.refreshMu.Unlock()
		// Only the timer of the still-current page may navigate. After a
		// Back or a jump to another page the generation has changed.
		if current != gen {
			return
		}
		bow.Reload()
	}()
}

// shouldRedirect is used as the value to http.Client.CheckRedirect.
func (bow *Browser) shouldRedirect(req *http.Request, _ []*http.Request) error {
	if bow.attributes[FollowRedirects] {
		req.Header.Set("User-Agent", bow.userAgent)
		return nil
	}
	return errors.NewLocation(
		"Redirects are disabled. Cannot follow '%s'.", req.URL.String())
}

// attributeToUrl reads an attribute from an element and returns a url.
func (bow *Browser) attrToResolvedUrl(name string, sel *goquery.Selection) (*url.URL, error) {
	src, ok := sel.Attr(name)
	if !ok {
		return nil, errors.NewAttributeNotFound(
			"Attribute '%s' not found.", name)
	}
	ur, err := url.Parse(src)
	if err != nil {
		return nil, err
	}

	return bow.ResolveUrl(ur), nil
}

// attributeOrDefault reads an attribute and returns it or the default value when it's empty.
func (bow *Browser) attrOrDefault(name, def string, sel *goquery.Selection) string {
	a, ok := sel.Attr(name)
	if ok {
		return a
	}
	return def
}

// isContentTypeHtml returns true when the given response sent the "text/html" content type.
func isContentTypeHtml(res *http.Response) bool {
	if res != nil {
		ct := res.Header.Get("Content-Type")
		return ct == "" || strings.Contains(ct, "text/html")
	}
	return false
}
