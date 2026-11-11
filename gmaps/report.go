package gmaps

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// ContractVersion is the version of docs/contracts.md this engine was built
// against. It is written into every search report line. Bump it in the same
// change as the header of docs/contracts.md; TestContractVersionMatchesDocs
// fails otherwise.
const ContractVersion = "1.2.5"

// Task modes, as in the task file (docs/contracts.md §2).
const (
	ModeList    = "list"
	ModeBrowser = "browser"
)

// Reasons written into the error field of a task that was cut off.
const (
	ErrInterrupted = "interrupted"
	ErrAborted     = "aborted: max-blocked"
)

// Status is the final state of a task (docs/contracts.md §4.1).
type Status string

// Task statuses, exactly as written in the search report.
const (
	StatusOK         Status = "ok"
	StatusEmpty      Status = "empty"
	StatusPartial    Status = "partial"
	StatusBlocked    Status = "blocked"
	StatusParseError Status = "parse_error"
	StatusError      Status = "error"
	StatusSkipped    Status = "skipped"
)

const (
	maxErrorRunes = 500
	timeLayout    = "2006-01-02T15:04:05.000Z"
)

// SearchReport is one line of the search report (docs/contracts.md §4). The
// pointer fields are the nullable ones.
type SearchReport struct {
	Contract       string  `json:"contract"`
	ID             string  `json:"id"`
	Mode           string  `json:"mode"`
	Status         Status  `json:"status"`
	BlockReason    *string `json:"block_reason"`
	Error          *string `json:"error"`
	MaxPages       *int    `json:"max_pages"`
	PagesRequested int     `json:"pages_requested"`
	PagesOK        int     `json:"pages_ok"`
	LastPageFull   bool    `json:"last_page_full"`
	RowsSeen       int     `json:"rows_seen"`
	Results        int     `json:"results"`
	HTTPStatuses   []int   `json:"http_statuses"`
	BytesIn        *int    `json:"bytes_in"`
	LinksFound     *int    `json:"links_found"`
	LinksNew       *int    `json:"links_new"`
	PlacesOK       *int    `json:"places_ok"`
	PlacesFailed   *int    `json:"places_failed"`
	StartedAt      string  `json:"started_at"`
	FinishedAt     string  `json:"finished_at"`
	DurationMS     int64   `json:"duration_ms"`
	EngineVersion  string  `json:"engine_version"`
}

// SanitizeError makes an error text safe for the report: credentials in URLs
// are masked as ://***@, line breaks become spaces, and the text is cut to 500
// runes.
func SanitizeError(s string) string {
	s = maskSecrets(s)
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}

		return r
	}, s)

	if runes := []rune(s); len(runes) > maxErrorRunes {
		s = string(runes[:maxErrorRunes])
	}

	return s
}

// ReportWriter appends search report lines to a file, one JSON object per line,
// flushing and syncing after each line. A nil *ReportWriter is valid and writes
// nothing. It is safe for concurrent use.
type ReportWriter struct {
	mu           sync.Mutex
	f            *os.File
	enc          *json.Encoder
	written      map[string]bool
	err          error
	syncEachLine bool
	onError      func()
}

// NewReportWriter creates (or replaces) the report file at path.
func NewReportWriter(path string) (*ReportWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}

	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)

	return &ReportWriter{f: f, enc: enc, written: map[string]bool{}, syncEachLine: true}, nil
}

// SetOnError sets a function called once, after the first write failure.
func (w *ReportWriter) SetOnError(fn func()) {
	if w == nil {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	w.onError = fn
}

// Write appends one line. A second line for the same task id is refused.
func (w *ReportWriter) Write(r SearchReport) error { //nolint:gocritic // a value keeps the call sites simple; one copy per task
	if w == nil {
		return nil
	}

	notify, err := w.write(&r)
	if notify != nil {
		notify()
	}

	return err
}

// write appends the line under the lock; the returned callback (the first
// failure's onError) is for the caller to run outside the lock.
func (w *ReportWriter) write(r *SearchReport) (notify func(), err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.written[r.ID] {
		return nil, fmt.Errorf("search report line for task %q already written", r.ID)
	}

	err = w.enc.Encode(r)
	if err == nil && w.syncEachLine {
		err = w.f.Sync()
	}

	if err != nil {
		if w.err == nil {
			w.err = err
			notify = w.onError
		}

		return notify, err
	}

	w.written[r.ID] = true

	return nil, nil
}

// Err returns the first write failure, or nil.
func (w *ReportWriter) Err() error {
	if w == nil {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	return w.err
}

// Close closes the file.
func (w *ReportWriter) Close() error {
	if w == nil {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	return w.f.Close()
}

// PageResult is what one list-mode page request produced.
type PageResult struct {
	Page    int
	Status  int // HTTP status; 0 when the request got no response
	Bytes   int // body length
	Rows    int // raw business rows on the page (before dedup)
	Results int // records handed to the writer
	Outcome PageOutcome
	Reason  BlockReason
	Err     string
}

// Summary counts the tasks of a run per status.
type Summary struct {
	Tasks      int
	OK         int
	Empty      int
	Partial    int
	Blocked    int
	ParseError int
	Error      int
	Skipped    int
}

type taskReport struct {
	id        string
	mode      string
	maxPages  *int
	started   bool
	startedAt time.Time
	pages     []PageResult
	firstFail *PageResult
	finished  bool
}

// TaskTracker knows every task of a batch from the moment its seed job is
// created, follows its pages, and writes exactly one report line per task.
// It is safe for concurrent use.
type TaskTracker struct {
	mu      sync.Mutex
	tasks   map[string]*taskReport
	order   []string
	counts  map[Status]int
	writer  *ReportWriter
	version string
	now     func() time.Time

	// -max-blocked: when this many tasks have ended blocked, onBlocked is called once.
	maxBlocked int
	onBlocked  func()
}

// NewTaskTracker returns a tracker that writes through w (which may be nil).
// version is the engine version for the report; now defaults to time.Now.
func NewTaskTracker(w *ReportWriter, version string, now func() time.Time) *TaskTracker {
	if now == nil {
		now = time.Now
	}

	return &TaskTracker{
		tasks:   map[string]*taskReport{},
		counts:  map[Status]int{},
		writer:  w,
		version: version,
		now:     now,
	}
}

// SetBlockedThreshold makes fn run once, right after the line of the n-th task
// that ended `blocked` was written. Only `blocked` counts, not `partial`; n <= 0
// disables it.
func (t *TaskTracker) SetBlockedThreshold(n int, fn func()) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.maxBlocked, t.onBlocked = n, fn
}

// Register adds a task before it runs. maxPages is nil for browser tasks.
func (t *TaskTracker) Register(id, mode string, maxPages *int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if _, ok := t.tasks[id]; ok {
		return
	}

	t.tasks[id] = &taskReport{id: id, mode: mode, maxPages: maxPages}
	t.order = append(t.order, id)
}

// FetchStarted records that the request for a page is being issued. Only the
// first call for page 0 sets the task's start time.
func (t *TaskTracker) FetchStarted(id string, page int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	r := t.tasks[id]
	if r == nil || r.finished || page != 0 || r.started {
		return
	}

	r.started = true
	r.startedAt = t.now()
}

// startedAt returns the recorded start time of a task (tests).
func (t *TaskTracker) startedAt(id string) string {
	t.mu.Lock()
	defer t.mu.Unlock()

	r := t.tasks[id]
	if r == nil || r.startedAt.IsZero() {
		return ""
	}

	return r.startedAt.UTC().Format(timeLayout)
}

// PageDone records the outcome of one page request.
func (t *TaskTracker) PageDone(id string, p PageResult) { //nolint:gocritic // small value type, copied once per page
	t.mu.Lock()
	defer t.mu.Unlock()

	r := t.tasks[id]
	if r == nil || r.finished {
		return
	}

	if !r.started {
		r.started = true
		r.startedAt = t.now()
	}

	r.pages = append(r.pages, p)

	if p.Outcome != PageOK && r.firstFail == nil {
		failed := p
		r.firstFail = &failed
	}
}

// Finish computes the task's status and writes its line, once.
func (t *TaskTracker) Finish(id string) {
	t.mu.Lock()

	r := t.tasks[id]
	if r == nil || r.finished {
		t.mu.Unlock()

		return
	}

	rep := t.finishLocked(r, "")
	fire := rep.Status == StatusBlocked && t.maxBlocked > 0 && t.counts[StatusBlocked] == t.maxBlocked
	onBlocked := t.onBlocked

	t.mu.Unlock()

	_ = t.writer.Write(rep)

	if fire && onBlocked != nil {
		onBlocked()
	}
}

// FinalizeAll writes a line for every task that has none: skipped if it never
// started, partial if its first page succeeded, error otherwise. reason goes
// into the error field.
func (t *TaskTracker) FinalizeAll(reason string) {
	t.mu.Lock()

	var reps []SearchReport

	for _, id := range t.order {
		if r := t.tasks[id]; !r.finished {
			reps = append(reps, t.finishLocked(r, reason))
		}
	}

	t.mu.Unlock()

	for i := range reps {
		_ = t.writer.Write(reps[i])
	}
}

// Counts returns the number of tasks per status so far; tasks without a line
// are not counted in any status.
func (t *TaskTracker) Counts() Summary {
	t.mu.Lock()
	defer t.mu.Unlock()

	return Summary{
		Tasks:      len(t.order),
		OK:         t.counts[StatusOK],
		Empty:      t.counts[StatusEmpty],
		Partial:    t.counts[StatusPartial],
		Blocked:    t.counts[StatusBlocked],
		ParseError: t.counts[StatusParseError],
		Error:      t.counts[StatusError],
		Skipped:    t.counts[StatusSkipped],
	}
}

// finishLocked marks r finished and builds its line. cutoff is non-empty when
// the task is being closed because the run ended.
func (t *TaskTracker) finishLocked(r *taskReport, cutoff string) SearchReport {
	r.finished = true

	now := t.now()
	status, reason, errText := computeStatus(r, cutoff)
	t.counts[status]++

	rep := SearchReport{
		Contract:      ContractVersion,
		ID:            r.id,
		Mode:          r.mode,
		Status:        status,
		MaxPages:      r.maxPages,
		HTTPStatuses:  []int{},
		EngineVersion: t.version,
	}

	if reason != "" {
		rep.BlockReason = strPtr(string(reason))
	}

	if errText != "" {
		rep.Error = strPtr(SanitizeError(errText))
	}

	var bytesIn int

	for i := range r.pages {
		p := &r.pages[i]

		rep.HTTPStatuses = append(rep.HTTPStatuses, p.Status)
		bytesIn += p.Bytes

		if p.Outcome == PageOK {
			rep.PagesOK++
			rep.RowsSeen += p.Rows
			rep.Results += p.Results
			rep.LastPageFull = p.Rows == pageSize
		}
	}

	rep.PagesRequested = len(r.pages)

	if r.mode == ModeBrowser {
		rep.LinksFound, rep.LinksNew, rep.PlacesOK, rep.PlacesFailed = intPtr(0), intPtr(0), intPtr(0), intPtr(0)
	} else {
		rep.BytesIn = intPtr(bytesIn)
	}

	started := r.startedAt
	if status == StatusSkipped || started.IsZero() {
		started = now
	}

	rep.StartedAt = started.UTC().Format(timeLayout)
	rep.FinishedAt = now.UTC().Format(timeLayout)

	if d := now.Sub(started).Milliseconds(); d > 0 {
		rep.DurationMS = d
	}

	return rep
}

// computeStatus implements the status table of docs/contracts.md §4.1 for a
// list-mode task. cutoff is non-empty when the run ended before the task did.
func computeStatus(r *taskReport, cutoff string) (Status, BlockReason, string) {
	var pagesOK, results int

	for i := range r.pages {
		if r.pages[i].Outcome == PageOK {
			pagesOK++
			results += r.pages[i].Results
		}
	}

	if f := r.firstFail; f != nil {
		return failureStatus(f)
	}

	switch {
	case cutoff != "":
		switch {
		case !r.started && len(r.pages) == 0:
			return StatusSkipped, "", cutoff
		case pagesOK >= 1:
			return StatusPartial, "", cutoff
		default:
			return StatusError, "", cutoff
		}
	case pagesOK == 0:
		return StatusError, "", "task finished without a page"
	case results >= 1:
		return StatusOK, "", ""
	default:
		return StatusEmpty, "", ""
	}
}

// failureStatus maps the first failed page of a task to its final status:
// page 0 decides blocked / error / parse_error; a later page makes it partial.
func failureStatus(f *PageResult) (Status, BlockReason, string) {
	if f.Page > 0 {
		if f.Outcome == PageBlocked {
			return StatusPartial, f.Reason, ""
		}

		return StatusPartial, "", f.Err
	}

	switch f.Outcome {
	case PageBlocked:
		return StatusBlocked, f.Reason, ""
	case PageParseError:
		return StatusParseError, "", f.Err
	case PageOK, PageHTTPError, PageNetworkError:
		return StatusError, "", f.Err
	default:
		return StatusError, "", f.Err
	}
}

func intPtr(v int) *int { return &v }

func strPtr(v string) *string { return &v }
