package gmaps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/gosom/scrapemate"
)

// processTracked is Process for a job that reports to a TaskTracker (jsonl
// runs). Order: raw capture, classification of the response, parsing, dedup and
// rank, then the page is recorded and either the next page is scheduled or the
// task is finished and its report line written.
func (j *SearchJob) processTracked(ctx context.Context, resp *scrapemate.Response) (any, []scrapemate.IJob, error) {
	defer func() {
		resp.Document = nil
		resp.Body = nil
		resp.Meta = nil
	}()

	// the exact bytes are saved first, blocked and HTTP-error pages included, so
	// a classifier or parser fix can be replayed offline; a failure is only logged
	if j.RawDir != "" && len(resp.Body) > 0 {
		if err := writeRaw(j.RawDir, j.TaskID, j.Page, resp.Body); err != nil {
			log.Printf("warning: raw capture of task %q page %d failed: %s", j.TaskID, j.Page, maskSecrets(err.Error()))
		}
	}

	// the run is being cancelled and no response came back: the task is left for
	// FinalizeAll, which reports it as interrupted
	if resp.StatusCode == 0 && ctx.Err() != nil {
		return nil, nil, nil
	}

	page := PageResult{Page: j.Page, Status: resp.StatusCode, Bytes: len(resp.Body)}
	page.Outcome, page.Reason = ClassifyListResponse(resp.StatusCode, resp.Headers.Get("Location"), resp.URL, resp.Body)

	if page.Outcome == PageNetworkError {
		page.Err = "no response"
		if resp.Error != nil {
			page.Err = resp.Error.Error()
		}
	}

	if page.Outcome == PageHTTPError {
		page.Err = fmt.Sprintf("http status %d", resp.StatusCode)
	}

	if page.Outcome != PageOK {
		return j.endTask(ctx, &page)
	}

	entries, err := ParseSearchResults(removeFirstLine(bytes.TrimLeft(resp.Body, " \t\r\n")))

	switch {
	case errors.Is(err, ErrEmptyBusinessList):
		entries = nil // a well-formed page without businesses
	case err != nil:
		page.Outcome = PageParseError
		page.Err = "failed to parse search results: " + err.Error()

		return j.endTask(ctx, &page)
	}

	kept, rawRows := j.keepTaskRows(entries)
	page.Rows, page.Results = rawRows, len(kept)

	// a full page (raw rows, before dedup) means Google may have more
	more := rawRows == pageSize && j.Page+1 < j.MaxPages

	j.Tracker.PageDone(j.TaskID, page)

	var next []scrapemate.IJob

	if more {
		next = []scrapemate.IJob{j.nextPage()}
	} else {
		j.Tracker.Finish(j.TaskID)
	}

	// the line is written; now the pause, then the exit monitor hears about the
	// page (it may cancel the run, which must not cut the last pause short)
	j.pause(ctx)
	j.account(len(kept), !more)

	// flat results: the stock JSON writer then writes one object per line
	flat := make([]any, 0, len(kept))
	for _, entry := range kept {
		flat = append(flat, entry)
	}

	return flat, next, nil
}

// keepTaskRows applies the task rules to the parsed rows: task id, link, radius
// filter, within-task dedup and rank. It returns the rows to write and the raw
// row count.
func (j *SearchJob) keepTaskRows(entries []*Entry) (kept []*Entry, rawRows int) {
	rawRows = len(entries)

	for _, entry := range entries {
		entry.ID = j.TaskID

		if entry.Link == "" && entry.PlaceID != "" {
			entry.Link = "https://www.google.com/maps/place/?q=place_id:" + entry.PlaceID
		}
	}

	if !j.NoRadiusFilter {
		entries = filterWithinRadius(entries, j.params.Location.Lat, j.params.Location.Lon, j.params.Location.Radius)
	}

	if j.State == nil {
		j.State = newTaskState()
	}

	kept = entries[:0]

	for _, entry := range entries {
		if rank, ok := j.State.admit(entry); ok {
			entry.Page, entry.Rank = j.Page, rank
			kept = append(kept, entry)
		}
	}

	return kept, rawRows
}

// endTask records a failed page, finishes the task and completes its seed.
func (j *SearchJob) endTask(ctx context.Context, page *PageResult) (any, []scrapemate.IJob, error) {
	j.Tracker.PageDone(j.TaskID, *page)
	j.Tracker.Finish(j.TaskID)

	if page.Status != 0 { // no response, nothing to be polite about
		j.pause(ctx)
	}

	j.account(0, true)

	return nil, nil, nil
}

// pause is the worker's random delay after a page response.
func (j *SearchJob) pause(ctx context.Context) {
	_ = sleepJitter(ctx, j.DelayMin, j.DelayMax)
}

// account tells the exit monitor about the rows of this page; last is true when
// the task has no further page (its seed is then complete).
func (j *SearchJob) account(rows int, last bool) {
	if j.ExitMonitor == nil {
		return
	}

	j.ExitMonitor.IncrPlacesFound(rows)

	if last {
		j.ExitMonitor.IncrSeedCompleted(1)
	}

	if !j.WriterManagedCompletion {
		j.ExitMonitor.IncrPlacesCompleted(rows)
	}
}
