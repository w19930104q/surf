# Overview
Start by creating a new \*browser.Browser and making a GET request to golang.org.

```go
bow := surf.NewBrowser()
err := bow.Open("http://golang.org")
if err != nil {
	panic(err)
}

// Outputs: "The Go Programming Language"
fmt.Println(bow.Title())
```

If you need to you can add additional request headers.

```go
bow := surf.NewBrowser()
bow.AddRequestHeader("Accept", "text/html")
bow.AddRequestHeader("Accept-Charset", "utf8")

err := bow.Open("http://golang.org")
if err != nil {
	panic(err)
}

fmt.Println(bow.Title())
```

It's important to note that `Browser.Open()` does not return any kind of response object. Rather, the "state" of
the browser changes to reflect the current page. Calling `Open()` is analogous to typing an URL into
your web browser address bar. The "state" of the browser changes after requesting the new page.

When we open a new page, the state changes to reflection the current page.

```go
err := bow.Open("http://reddit.com")
if err != nil {
	panic(err)
}

// Outputs: "reddit: the front page of the internet"
fmt.Println(bow.Title())
```

Just like a real web browser, Surf maintains a history that you can move back through. You can also
bookmark pages and come back to them later.

```go
// Bookmark the page so we can come back to it later.
err = bow.Bookmark("reddit")
if err != nil {
	panic(err)
}

// Now move back to the golang.org site.
bow.Back()

// And then back to reddit using our bookmark.
bow.OpenBookmark("reddit")
```

By default the bookmarks are kept in memory, and will disappear when your \*browser.Browser instance
is destroyed. See the [settings](settings/#storage-jars) for information on saving your bookmarks to a file.


# Working With the DOM
Interacting with elements on pages is done through jQuery style CSS selectors using the
[goquery](https://github.com/PuerkitoBio/goquery) library. In this next example
the `Click()` method is called with the CSS selector `a.new` which finds an anchor tag with the class `new`.

```go
bow := surf.NewBrowser()
err := bow.Open("http://reddit.com")
if err != nil {
	panic(err)
}


err = bow.Click("a.new")
if err != nil {
	panic(err)
}

// Outputs: "newest submissions: reddit.com"
fmt.Println(bow.Title())
```

The underlying document is exposed via the Dom() method, which can be used to parse values from the
body. In this example we print the "title" attribute of each link on the page by finding each element
matching the selector `a.title`.

```go
bow := surf.NewBrowser()
bow.Open("http://reddit.com")
bow.Dom().Find("a.title").Each(func(_ int, s *goquery.Selection) {
    fmt.Println(s.Text())
})

// The most common Dom() methods can be called directly from the browser.
// The need to find elements on the page is common enough that the above could
// be written like this.
bow.Find("a.title").Each(func(_ int, s *goquery.Selection) {
    fmt.Println(s.Text())
})
```

# Submitting Forms
Submitting forms using the POST method is easy, and begins by requesting the document containing the form,
using a selector to find the form, filling out the form values, and finally submitting the form.

```go
bow := surf.NewBrowser()
bow.Open("http://reddit.com")

fm, err := bow.Form("form.login-form")
if err != nil {
	panic(err)
}

fm.Input("user", "JoeRedditor")
fm.Input("passwd", "d234rlkasd")
err = fm.Submit()
if err != nil {
	panic(err)
}
```

Note how we use the selector `form.login-form` to find the form with the class `login-form`. The `Form()` method
returns an instance of `browser.Submittable`, which contains methods for setting values on the form elements.
In the example above the call `fm.Input("user", "JoeRedditor")` finds the input element named "user", and
`fm.Input("passwd", "d234rlkasd")` finds the input element named "passwd".


# Downloading
Surf makes it easy to download page assets, such as images, stylesheets, and scripts. They can even be downloaded
asynchronously.

```go
bow := surf.NewBrowser()
err := bow.Open("http://www.reddit.com")
if err != nil { panic(err) }

// Download the images on the page and write them to files.
for _, image := range bow.Images() {
    filename := "/home/joe/Pictures" + image.URL.Path()
    fout, err := os.Create(filename)
    if err != nil {
    	log.Printf(
    	    "Error creating file '%s'.", filename)
    	continue
    }
    defer fout.Close()
    
    _, err = image.Download(fout)
    if err != nil {
    	log.Printf(
    	    "Error downloading file '%s'.", filename)
    }
}

// Downloading assets asynchronously takes a little more work, but isn't difficult.
// The DownloadAsync() method takes an io.Writer just like the Download() method,
// plus an instance of AsyncDownloadChannel. The DownloadAsync() method will send
// an instance of browser.AsyncDownloadResult to the channel when the download is
// complete.
ch := make(AsyncDownloadChannel, 1)
queue := 0
for _, image := range bow.Images() {
    filename := "/home/joe/Pictures" + image.URL.Path()
	fout, err := os.Create(filename)
	if err != nil {
		log.Printf(
			"Error creating file '%s'.", filename)
		continue
	}
	
	image.DownloadAsync(fout, ch)
	queue++
}

// Now we wait for each download to complete.
for {
	select {
	case result := <- ch:
	    // result is the instance of browser.AsyncDownloadResult sent by the
	    // DownloadAsync() method. It contains the writer which you need to
	    // close. It also contains the asset itself, and an error instance if
	    // there was an error.
		result.Writer.Close()
		if result.Error != nil {
		    log.Printf("Error download '%s'. %s\n", result.Asset.Url(), result.Error)
		} else {
		    log.Printf("Downloaded '%s'.\n", result.Asset.Url())
		}
		
		queue--
		if queue == 0 {
			goto FINISHED
		}
	}
}
	
FINISHED:
close(ch)
log.Println("Downloads complete!")
```

When downloading assets asynchronously, you should keep in mind the potentially large number of assets embedded
into a typical web page. For that reason you should setup a queue that downloads only a few at a time.

## Isolated tabs

A common scraping pattern is logging in once and then handing the same
filtered listing page to several independent collection branches. Surf
supports this with `NewTabFromSnapshot()`, which opens an explicitly isolated
browser tab from the current page.

```go
bow := surf.NewBrowser()

// Log in once. Every tab below reuses this session through the shared
// cookie jar.
err := bow.PostForm("https://example.com/login", url.Values{
    "user": {"alice"},
    "pass": {"secret"},
})
if err != nil {
    panic(err)
}
if err := bow.Open("https://example.com/filters"); err != nil {
    panic(err)
}

// Two independent collectors working from the same listing page.
branchA := bow.NewTabFromSnapshot()
branchB := bow.NewTabFromSnapshot()
defer branchA.Close()
defer branchB.Close()
```

### What is shared, what is isolated

The tab starts from a deep-copied snapshot of the current page. The following
resources are **shared** on purpose:

* The cookie jar, so the login session is visible in every tab.
* The bookmarks jar.
* The initial HTTP `Transport` (connection pooling keeps working).

Everything that belongs to a single browsing context is **owned by the tab**:

* The page DOM, raw body and form modifications - edits never reach the
  parent or sibling tabs.
* Request headers and their underlying slices (`AddRequestHeader`,
  `DelRequestHeader` stay local).
* Browser attributes and the redirect policy (`SetAttribute`, including
  `FollowRedirects`).
* Navigation history. `Back()` can only reach the tab's own snapshot and the
  pages the tab itself visited; it can never walk into the parent's history.
* Meta-refresh timers. Calling `Close()` stops only the closing tab's timer.
* The `*http.Client`. The initial transport is shared, but calling
  `SetTransport()` or `SetTimeout()` on a tab never mutates the parent
  client.

```go
branchA.AddRequestHeader("X-Branch", "a")
branchB.AddRequestHeader("X-Branch", "b")

// Interleaved navigation is safe: each tab keeps its own DOM and history.
_ = branchA.Open("https://example.com/page/1")
_ = branchB.Open("https://example.com/page/2")
branchA.Back() // returns to branchA's snapshot of /filters

// Reconfiguring a tab's client cannot affect the parent browser.
branchA.SetTimeout(5 * time.Second)
branchA.SetTransport(http.DefaultTransport)

// Closing a tab stops its refresh timer only. The shared transport is not
// closed and the shared cookies are not cleared.
branchA.Close()
```

A failed navigation leaves the tab on its snapshot and never pollutes another
tab or the parent. Tabs can also be created before any page has been loaded;
they simply start with a blank snapshot and can navigate immediately.

```go
blank := surf.NewBrowser()
tab := blank.NewTabFromSnapshot()
err := tab.Open("https://example.com/login") // works without a current page
```

### NewTab versus NewTabFromSnapshot

The legacy `NewTab()` method is unchanged for backwards compatibility. It
performs a shallow copy, so the clone keeps sharing the same state pointer,
header map, attribute map, history and HTTP client with the parent. Use
`NewTabFromSnapshot()` whenever the branches need to be isolated.
