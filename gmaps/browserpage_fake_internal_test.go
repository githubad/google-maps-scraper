package gmaps

import (
	"errors"
	"sync"
	"time"

	"github.com/gosom/scrapemate"
)

// fakePage is a scripted scrapemate.BrowserPage, so BrowserActions can be tested
// without a browser.
type fakePage struct {
	mu sync.Mutex

	navStatus int
	navURL    string
	navErr    error

	currentURL   string
	feedPresent  bool
	redirectedTo string // set as the current URL when the feed wait fails (single place)
	content      string

	feedWaits    int
	contentCalls int
}

var errNoFeed = errors.New("timeout waiting for the feed")

func (p *fakePage) Goto(string, scrapemate.WaitUntilState) (*scrapemate.PageResponse, error) {
	if p.navErr != nil {
		return nil, p.navErr
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.currentURL == "" {
		p.currentURL = p.navURL
	}

	return &scrapemate.PageResponse{URL: p.navURL, StatusCode: p.navStatus}, nil
}

func (p *fakePage) URL() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.currentURL
}

func (p *fakePage) Content() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.contentCalls++

	return p.content, nil
}

func (*fakePage) Reload(scrapemate.WaitUntilState) error { return nil }

func (*fakePage) Screenshot(bool) ([]byte, error) { return nil, nil }

// Eval answers the cookie banner script (false) and the scroll script (a
// constant height, so scrolling stops after one step).
func (*fakePage) Eval(string, ...any) (any, error) { return float64(100), nil }

func (*fakePage) WaitForURL(string, time.Duration) error { return nil }

func (p *fakePage) WaitForSelector(string, time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.feedWaits++

	if p.feedPresent {
		return nil
	}

	if p.redirectedTo != "" {
		p.currentURL = p.redirectedTo
	}

	return errNoFeed
}

func (*fakePage) WaitForTimeout(time.Duration) {}

func (*fakePage) Locator(string) scrapemate.Locator { return nil }

func (*fakePage) Close() error { return nil }

func (*fakePage) Unwrap() any { return nil }
