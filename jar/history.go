package jar

import (
	"container/list"
	"net/http"
	"net/url"

	"github.com/PuerkitoBio/goquery"
)

// State represents a point in time.
//
// A State is an immutable snapshot of a single loaded page: the request that
// produced it, the response (URL, status code and headers), the parsed DOM,
// the raw response bytes used by Browser.Download and the effective base URL
// used to resolve relative links. All of these fields travel together through
// the history so navigating Back restores the whole page without issuing a new
// request.
type State struct {
	Request  *http.Request
	Response *http.Response
	Dom      *goquery.Document

	// Body holds the decoded (post content-encoding) response bytes of the
	// page. It may be nil for states created directly via SetState, in which
	// case Browser.Download returns an explicit error.
	Body []byte

	// Base is the effective base URL of the page: the href of the first
	// <base> tag when one resolves, otherwise the page URL itself.
	Base *url.URL
}

// NewHistoryState creates and returns a new *State type.
//
// The signature is kept for backwards compatibility. States created this way
// have no raw body bytes, so Browser.Download cannot serve their contents.
// Prefer NewPageState for states produced by an actual page load.
func NewHistoryState(req *http.Request, resp *http.Response, dom *goquery.Document) *State {
	return &State{
		Request:  req,
		Response: resp,
		Dom:      dom,
	}
}

// NewPageState creates and returns a *State holding a complete page snapshot,
// including the decoded response bytes and the effective base URL.
func NewPageState(req *http.Request, resp *http.Response, dom *goquery.Document, body []byte, base *url.URL) *State {
	return &State{
		Request:  req,
		Response: resp,
		Dom:      dom,
		Body:     body,
		Base:     base,
	}
}

// History is a type that records browser state.
type History interface {
	Clear()
	SetMax(max int)
	Len() int
	Push(p *State) int
	Pop() *State
	Top() *State
}

// Node holds stack values and points to the next element.
type Node struct {
	Value *State
	Next  *Node
}

// MemoryHistory is an in-memory implementation of the History interface.
type MemoryHistory struct {
	list    *list.List
	maxHist int
}

// NewMemoryHistory creates and returns a new *StateHistory type.
func NewMemoryHistory() *MemoryHistory {
	return &MemoryHistory{list: list.New()}
}

// Len returns the number of states in the history.
func (his *MemoryHistory) Len() int {
	return his.list.Len()
}

// SetMax sets the max history length.  Setting values
// to 0 will disable history trimming, keeping a infinite list.
func (his *MemoryHistory) SetMax(max int) {
	his.maxHist = max
}

// Clear removes all history.
func (his *MemoryHistory) Clear() {
	his.list.Init()
}

// Push adds a new State at the front of the history.
func (his *MemoryHistory) Push(p *State) int {
	his.list.PushFront(p)

	// Trim history if maxHist is set
	if his.maxHist > 0 {
		if l := his.list.Len(); l > his.maxHist {
			for i := 0; i < l-his.maxHist; i++ {
				his.list.Remove(his.list.Back())
			}
		}
	}
	return his.list.Len()
}

// Pop removes and returns the State at the front of the history.
func (his *MemoryHistory) Pop() *State {
	if his.list.Len() > 0 {
		return his.list.Remove(his.list.Front()).(*State)
	}
	return nil
}

// Top returns the State at the front of the history without removing it.
func (his *MemoryHistory) Top() *State {
	if his.list.Len() == 0 {
		return nil
	}
	return his.list.Front().Value.(*State)
}
