package jar

import (
	"testing"

	"github.com/headzoo/ut"
)

func TestMemoryHistory(t *testing.T) {
	ut.Run(t)
	stack := NewMemoryHistory()

	page1 := &State{}
	stack.Push(page1)
	ut.AssertEquals(1, stack.Len())
	ut.AssertEquals(page1, stack.Top())

	page2 := &State{}
	stack.Push(page2)
	ut.AssertEquals(2, stack.Len())
	ut.AssertEquals(page2, stack.Top())

	page := stack.Pop()
	ut.AssertEquals(page, page2)
	ut.AssertEquals(1, stack.Len())
	ut.AssertEquals(page1, stack.Top())

	page = stack.Pop()
	ut.AssertEquals(page, page1)
	ut.AssertEquals(0, stack.Len())
}

func TestMemoryHistoryWithMax(t *testing.T) {
	ut.Run(t)
	stack := NewMemoryHistory()
	stack.SetMax(2)

	ut.AssertEquals(2, stack.maxHist)

	page1 := &State{}
	stack.Push(page1)
	ut.AssertEquals(1, stack.Len())
	ut.AssertEquals(page1, stack.Top())

	page2 := &State{}
	stack.Push(page2)
	ut.AssertEquals(2, stack.Len())
	ut.AssertEquals(page2, stack.Top())

	page3 := &State{}
	stack.Push(page3)
	ut.AssertEquals(2, stack.Len())
	ut.AssertEquals(page3, stack.Top())

	page4 := &State{}
	stack.Push(page4)
	ut.AssertEquals(2, stack.Len())
	ut.AssertEquals(page4, stack.Top())

	page := stack.Pop()
	ut.AssertEquals(page, page4)
	ut.AssertEquals(1, stack.Len())
	ut.AssertEquals(page3, stack.Top())

	page = stack.Pop()
	ut.AssertEquals(page, page3)
	ut.AssertEquals(0, stack.Len())
}

func TestMemoryHistoryClear(t *testing.T) {
	ut.Run(t)
	stack := NewMemoryHistory()

	stack.Push(&State{})
	stack.Push(&State{})
	ut.AssertEquals(2, stack.Len())
	stack.Clear()
	ut.AssertEquals(0, stack.Len())
}

// TestMemoryHistoryCapacityOneCanGoBackOnePage pins the capacity behaviour the
// browser relies on: with a max length of 1 the single retained previous page
// can still be popped once, and no further back step is possible.
func TestMemoryHistoryCapacityOneCanGoBackOnePage(t *testing.T) {
	ut.Run(t)
	stack := NewMemoryHistory()
	stack.SetMax(1)

	page1 := &State{}
	page2 := &State{}
	page3 := &State{}
	stack.Push(page1)
	stack.Push(page2)
	ut.AssertEquals(1, stack.Len())
	stack.Push(page3)
	ut.AssertEquals(1, stack.Len())

	page := stack.Pop()
	ut.AssertEquals(page3, page)
	ut.AssertEquals(0, stack.Len())

	if stack.Pop() != nil {
		t.Fatal("popping an empty history must return nil")
	}
}
