package gmaps

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"
	"uuid"

	"github.com/gosom/google-maps-scraper/exiter"
	"github.com/gosom/scrapemate"
)

type SearchJobOptions func(*SearchJob)

type MapLocation struct {
	Lat     float64
	Lon     float64
	ZoomLvl float64
	Radius  float64
}

type MapSearchParams struct {
	Location  MapLocation
	Query     string
	ViewportW int
	ViewportH int
	Hl        string

	// Offset is the index of the first row of the page (page*20).
	Offset int

	// TaskMode selects the task request format (jsonl list tasks): the
	// altitude comes from SpanM or the zoom, the field of view is fixed, and
	// the viewport is honoured. Stock requests keep upstream's exact string.
	TaskMode bool
	// SpanM is the visible map height in metres; 0 derives it from the zoom.
	SpanM float64
}

// pageSize is the number of rows Google returns per list page.
const pageSize = 20

// taskState is shared by the page jobs of one task: it drops rows already
// seen in an earlier page and hands out ranks.
type taskState struct {
	mu       sync.Mutex
	seen     map[string]struct{}
	nextRank int
}

func newTaskState() *taskState {
	return &taskState{seen: map[string]struct{}{}, nextRank: 1}
}

// dedupKey identifies a business within a task: data_id, then place_id, then
// title and address.
func dedupKey(e *Entry) string {
	switch {
	case e.DataID != "":
		return "d:" + e.DataID
	case e.PlaceID != "":
		return "p:" + e.PlaceID
	default:
		return "t:" + e.Title + "|" + e.Address
	}
}

// admit returns the next rank for e, or false when e was already seen.
func (s *taskState) admit(e *Entry) (rank int, ok bool) {
	key := dedupKey(e)

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, dup := s.seen[key]; dup {
		return 0, false
	}

	s.seen[key] = struct{}{}
	rank = s.nextRank
	s.nextRank++

	return rank, true
}

type SearchJob struct {
	scrapemate.Job

	params                  *MapSearchParams
	ExitMonitor             exiter.Exiter
	WriterManagedCompletion bool

	// TaskID is the id of the search task (-input-format jsonl). When set, it
	// becomes the input_id of every result, results are returned flat (one
	// value per result) and rows keep Google's order; empty keeps upstream's
	// behaviour.
	TaskID string

	// NoRadiusFilter skips the radius filter and the distance sort (a task
	// with radius_m 0 or absent).
	NoRadiusFilter bool

	// Page is the 0-based page of this job; MaxPages is how many pages the
	// task may fetch (0 or 1: one page). State is shared by all pages of a
	// task (set for jobs with a TaskID).
	Page     int
	MaxPages int
	State    *taskState
}

func NewSearchJob(params *MapSearchParams, opts ...SearchJobOptions) *SearchJob {
	const (
		defaultPrio       = scrapemate.PriorityMedium
		defaultMaxRetries = 3
		baseURL           = "https://maps.google.com/search"
	)

	job := SearchJob{
		Job: scrapemate.Job{
			ID:         uuid.NewV4().String(),
			Method:     http.MethodGet,
			URL:        baseURL,
			URLParams:  buildParams(params),
			MaxRetries: defaultMaxRetries,
			Priority:   defaultPrio,
		},
	}

	job.params = params

	for _, opt := range opts {
		opt(&job)
	}

	if job.TaskID != "" && job.State == nil {
		job.State = newTaskState()
	}

	return &job
}

// WithSearchJobMaxPages sets how many pages of 20 the task may fetch.
func WithSearchJobMaxPages(n int) SearchJobOptions {
	return func(j *SearchJob) {
		j.MaxPages = n
	}
}

// WithSearchJobBaseURL replaces the list-mode endpoint (tests only).
func WithSearchJobBaseURL(u string) SearchJobOptions {
	return func(j *SearchJob) {
		if u != "" {
			j.URL = u
		}
	}
}

// nextPage returns the job for the following page of the same task.
func (j *SearchJob) nextPage() *SearchJob {
	next := *j
	next.ID = uuid.NewV4().String()
	next.Priority = scrapemate.PriorityHigh
	next.Page = j.Page + 1

	params := *j.params
	params.Offset = next.Page * pageSize
	next.params = &params
	next.URLParams = buildParams(&params)

	return &next
}

func WithSearchJobExitMonitor(exitMonitor exiter.Exiter) SearchJobOptions {
	return func(j *SearchJob) {
		j.ExitMonitor = exitMonitor
	}
}

// WithSearchJobNoRadiusFilter keeps every row, in Google's order.
func WithSearchJobNoRadiusFilter() SearchJobOptions {
	return func(j *SearchJob) {
		j.NoRadiusFilter = true
	}
}

// WithSearchJobTaskID sets the task id used as input_id of the results.
func WithSearchJobTaskID(id string) SearchJobOptions {
	return func(j *SearchJob) {
		j.TaskID = id
	}
}

func WithSearchJobWriterManagedCompletion() SearchJobOptions {
	return func(j *SearchJob) {
		j.WriterManagedCompletion = true
	}
}

func (j *SearchJob) ProcessOnFetchError() bool {
	return true
}

func (j *SearchJob) Process(_ context.Context, resp *scrapemate.Response) (any, []scrapemate.IJob, error) {
	defer func() {
		resp.Document = nil
		resp.Body = nil
		resp.Meta = nil
	}()

	if resp.Error != nil {
		if j.ExitMonitor != nil {
			j.ExitMonitor.IncrSeedCompleted(1)
		}

		return nil, nil, resp.Error
	}

	body := removeFirstLine(resp.Body)
	if len(body) == 0 {
		if j.ExitMonitor != nil {
			j.ExitMonitor.IncrSeedCompleted(1)
		}

		return nil, nil, fmt.Errorf("empty response body")
	}

	entries, err := ParseSearchResults(body)
	if err != nil {
		if j.ExitMonitor != nil {
			j.ExitMonitor.IncrSeedCompleted(1)
		}

		return nil, nil, fmt.Errorf("failed to parse search results: %w", err)
	}

	rawRows := len(entries)

	for _, entry := range entries {
		if j.TaskID != "" {
			entry.ID = j.TaskID
		}

		if entry.Link == "" && entry.PlaceID != "" {
			entry.Link = "https://www.google.com/maps/place/?q=place_id:" + entry.PlaceID
		}
	}

	switch {
	case j.NoRadiusFilter:
		// keep every row in Google's order
	case j.TaskID != "":
		entries = filterWithinRadius(entries,
			j.params.Location.Lat,
			j.params.Location.Lon,
			j.params.Location.Radius,
		)
	default:
		entries = filterAndSortEntriesWithinRadius(entries,
			j.params.Location.Lat,
			j.params.Location.Lon,
			j.params.Location.Radius,
		)
	}

	var (
		next     []scrapemate.IJob
		lastPage = true
	)

	if j.TaskID != "" {
		// drop rows seen on an earlier page of this task and rank the rest
		kept := entries[:0]

		for _, entry := range entries {
			if rank, ok := j.State.admit(entry); ok {
				entry.Page, entry.Rank = j.Page, rank
				kept = append(kept, entry)
			}
		}

		entries = kept

		// a full page (raw rows, before dedup) means Google may have more
		if rawRows == pageSize && j.Page+1 < j.MaxPages {
			lastPage = false

			next = []scrapemate.IJob{j.nextPage()}
		}
	}

	if j.ExitMonitor != nil {
		j.ExitMonitor.IncrPlacesFound(len(entries))

		// a task's seed completes on its last page; pages are not seeds
		if lastPage {
			j.ExitMonitor.IncrSeedCompleted(1)
		}

		if !j.WriterManagedCompletion {
			j.ExitMonitor.IncrPlacesCompleted(len(entries))
		}
	}

	if j.TaskID != "" {
		// flat results: the stock JSON writer then writes one object per line
		flat := make([]any, 0, len(entries))
		for _, entry := range entries {
			flat = append(flat, entry)
		}

		return flat, next, nil
	}

	return entries, nil, nil
}

func removeFirstLine(data []byte) []byte {
	if len(data) == 0 {
		return data
	}

	index := bytes.IndexByte(data, '\n')
	if index == -1 {
		return []byte{}
	}

	return data[index+1:]
}

func buildGoogleMapsParams(params *MapSearchParams) map[string]string {
	params.ViewportH = 800
	params.ViewportW = 600

	ans := map[string]string{
		"tbm":              "map",
		"authuser":         "0",
		languageQueryParam: params.Hl,
		"q":                params.Query,
	}

	pb := fmt.Sprintf("!4m12!1m3!1d3826.902183192154!2d%.4f!3d%.4f!2m3!1f0!2f0!3f0!3m2!1i%d!2i%d!4f%.1f!7i20!8i%d"+
		"!10b1!12m22!1m3!18b1!30b1!34e1!2m3!5m1!6e2!20e3!4b0!10b1!12b1!13b1!16b1!17m1!3e1!20m3!5e2!6b1!14b1!46m1!1b0"+
		"!96b1!19m4!2m3!1i360!2i120!4i8",
		params.Location.Lon,
		params.Location.Lat,
		params.ViewportW,
		params.ViewportH,
		params.Location.ZoomLvl,
		params.Offset,
	)

	ans["pb"] = pb

	return ans
}

// Task request constants (docs/contracts.md §2, ADR-0003).
const (
	halfFieldOfViewDeg = 6.55 // !4f is 13.1 degrees: the vertical field of view
	minSpan            = 100.0
	maxSpan            = 200000.0
	metresPerPixelZ0   = 156543.03392 // Web Mercator, equator, zoom 0
	defaultViewportW   = 600
	defaultViewportH   = 800
)

// buildParams picks the request builder: the task format for jsonl list tasks,
// upstream's for everything else.
func buildParams(params *MapSearchParams) map[string]string {
	if params.TaskMode {
		return buildTaskParams(params)
	}

	return buildGoogleMapsParams(params)
}

// altitudeFromSpan converts the visible map height (m) into the camera altitude (m).
func altitudeFromSpan(spanM float64) float64 {
	return spanM / (2 * math.Tan(halfFieldOfViewDeg*math.Pi/180))
}

// derivedSpanM is the visible height implied by a zoom: Web Mercator metres per
// pixel times the viewport height, limited to 100..200000.
func derivedSpanM(lat, zoom float64, viewportH int) float64 {
	span := metresPerPixelZ0 * math.Cos(lat*math.Pi/180) * float64(viewportH) / math.Pow(2, zoom)

	return math.Min(math.Max(span, minSpan), maxSpan)
}

func buildTaskParams(params *MapSearchParams) map[string]string {
	w, h := params.ViewportW, params.ViewportH
	if w <= 0 || h <= 0 {
		w, h = defaultViewportW, defaultViewportH
	}

	span := params.SpanM
	if span <= 0 {
		span = derivedSpanM(params.Location.Lat, params.Location.ZoomLvl, h)
	}

	altitude := strconv.FormatFloat(altitudeFromSpan(span), 'f', 2, 64)

	pb := fmt.Sprintf("!4m12!1m3!1d%s!2d%.4f!3d%.4f!2m3!1f0!2f0!3f0!3m2!1i%d!2i%d!4f13.1!7i20!8i%d"+
		"!10b1!12m22!1m3!18b1!30b1!34e1!2m3!5m1!6e2!20e3!4b0!10b1!12b1!13b1!16b1!17m1!3e1!20m3!5e2!6b1!14b1!46m1!1b0"+
		"!96b1!19m4!2m3!1i360!2i120!4i8",
		altitude,
		params.Location.Lon,
		params.Location.Lat,
		w,
		h,
		params.Offset,
	)

	return map[string]string{
		"tbm":              "map",
		"authuser":         "0",
		languageQueryParam: params.Hl,
		"q":                params.Query,
		"pb":               pb,
	}
}
