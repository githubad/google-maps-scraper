package filerunner

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gosom/google-maps-scraper/deduper"
	"github.com/gosom/google-maps-scraper/exiter"
	"github.com/gosom/google-maps-scraper/gmaps"
	"github.com/gosom/google-maps-scraper/grid"
	"github.com/gosom/google-maps-scraper/leadsdb"
	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/runner/resume"
	"github.com/gosom/google-maps-scraper/tlmt"
	"github.com/gosom/scrapemate"
	"github.com/gosom/scrapemate/adapters/writers/csvwriter"
	"github.com/gosom/scrapemate/adapters/writers/jsonwriter"
	"github.com/gosom/scrapemate/scrapemateapp"
)

type fileRunner struct {
	cfg     *runner.Config
	input   io.Reader
	writers []scrapemate.ResultWriter
	app     *scrapemateapp.ScrapemateApp
	outfile *os.File

	// jsonl mode (-input-format jsonl): the validated tasks and their common mode.
	jsonl     bool
	tasks     []runner.SearchTask
	batchMode string

	// tracker follows every task of a jsonl batch and writes its report line;
	// report is the optional -search-report file behind it.
	tracker *gmaps.TaskTracker
	report  *gmaps.ReportWriter
	// gate stops new tasks after -max-blocked blocked tasks; fail remembers a
	// failed write of the results file.
	gate *gmaps.AbortController
	fail writeFailure

	resumeIDs      *resume.IdentitySet
	resumeDedup    *resume.IdentitySet
	resumeState    *resume.State
	resumeProgress *resume.ProgressTracker
}

func New(cfg *runner.Config) (runner.Runner, error) {
	if cfg.RunMode != runner.RunModeFile {
		return nil, fmt.Errorf("%w: %d", runner.ErrInvalidRunMode, cfg.RunMode)
	}

	if err := runner.ValidateInputConfig(cfg); err != nil {
		return nil, err
	}

	if cfg.Resume {
		if cfg.FastMode {
			return nil, fmt.Errorf("-resume does not support fast mode")
		}

		if cfg.ResultsFile == "stdout" {
			return nil, fmt.Errorf("-resume requires -results to be a file path")
		}

		if err := validateResumeFiles(cfg.ResultsFile); err != nil {
			return nil, err
		}

		if cfg.CustomWriter != "" {
			return nil, fmt.Errorf("-resume does not support custom writers")
		}

		if cfg.LeadsDBAPIKey != "" {
			return nil, fmt.Errorf("-resume does not support LeadsDB output")
		}
	}

	ans := &fileRunner{
		cfg: cfg,
	}

	// The task file is read and validated before the results file is created,
	// so an invalid file leaves no output behind (exit code 2).
	if cfg.InputFormat == "jsonl" {
		if err := ans.loadTasks(); err != nil {
			return nil, err
		}

		if err := ans.initReport(); err != nil {
			return nil, err
		}
	} else if err := ans.setInput(); err != nil {
		return nil, err
	}

	if err := ans.setWriters(); err != nil {
		return nil, err
	}

	if err := ans.setApp(); err != nil {
		return nil, err
	}

	return ans, nil
}

func (r *fileRunner) Run(ctx context.Context) (err error) {
	var seedJobs []scrapemate.IJob

	t0 := time.Now().UTC()

	defer func() {
		elapsed := time.Now().UTC().Sub(t0)
		params := map[string]any{
			"job_count": len(seedJobs),
			"duration":  elapsed.String(),
		}

		if err != nil {
			params["error"] = err.Error()
		}

		evt := tlmt.NewEvent("file_runner", params)

		_ = runner.Telemetry().Send(ctx, evt)
	}()

	dedup := deduper.New()

	if r.cfg.Resume {
		dedup = r.resumeDedup
	}

	exitMonitor := exiter.New()
	seedOpts := r.seedJobOptions()

	if r.tracker != nil {
		// every task gets exactly one report line, whatever way the run ends
		defer r.tracker.FinalizeAll(gmaps.ErrInterrupted)
	}

	switch {
	case r.jsonl:
		seedJobs, err = runner.CreateJSONLSeedJobs(r.tasks, r.cfg, exitMonitor, dedup, runner.WithTracker(r.tracker), runner.WithAbortGate(r.gate))
		if err == nil && len(seedJobs) == 0 {
			return nil // no tasks: nothing to run, exit 0
		}
	case r.cfg.GridBBox != "":
		if r.cfg.FastMode {
			return fmt.Errorf("-fast-mode cannot be used together with -grid-bbox")
		}

		bbox, bboxErr := grid.ParseBoundingBox(r.cfg.GridBBox)
		if bboxErr != nil {
			return fmt.Errorf("invalid -grid-bbox: %w", bboxErr)
		}

		cellCount := grid.EstimateCellCount(bbox, r.cfg.GridCellKm)
		fmt.Fprintf(os.Stderr, "grid scraping: ~%d cells (%.2f km each)\n", cellCount, r.cfg.GridCellKm)

		seedJobs, err = runner.CreateGridSeedJobs(
			r.cfg.LangCode,
			r.input,
			r.cfg.MaxDepth,
			r.cfg.Email,
			bbox,
			r.cfg.GridCellKm,
			r.cfg.Zoom,
			dedup,
			exitMonitor,
			r.cfg.ExtraReviews,
			seedOpts...,
		)
	default:
		seedJobs, err = runner.CreateSeedJobs(
			r.cfg.FastMode,
			r.cfg.LangCode,
			r.input,
			r.cfg.MaxDepth,
			r.cfg.Email,
			r.cfg.GeoCoordinates,
			r.cfg.Zoom,
			r.cfg.Radius,
			dedup,
			exitMonitor,
			r.cfg.ExtraReviews,
			seedOpts...,
		)
	}

	if err != nil {
		return err
	}

	exitMonitor.SetSeedCount(len(seedJobs))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	exitMonitor.SetCancelFunc(cancel)

	if r.gate != nil {
		r.gate.SetCancel(cancel)
	}

	// a results or report file that cannot be written ends the run (exit code 4)
	r.fail.onFail = cancel
	r.report.SetOnError(cancel)

	go exitMonitor.Run(ctx)

	err = r.app.Start(ctx, seedJobs...)

	if r.jsonl {
		return r.finishJSONL(err)
	}

	return err
}

// finishJSONL closes a jsonl run: it reports every task that has no line yet,
// prints the summary line and turns the outcome into the exit code of
// docs/contracts.md §1.3 (2, 1, 4, 3, 0 in that order of precedence; 2 was
// decided before the run).
func (r *fileRunner) finishJSONL(startErr error) error {
	aborted := r.gate.Aborted()

	reason := gmaps.ErrInterrupted
	if aborted {
		reason = gmaps.ErrAborted
	}

	r.tracker.FinalizeAll(reason)

	writeFailed := r.fail.failed.Load() || r.report.Err() != nil
	fatal := runner.RunFatal(startErr, writeFailed)
	code := runner.ResolveExit(fatal, writeFailed, aborted)

	fmt.Fprintln(os.Stderr, runner.SummaryLine(r.tracker.Counts(), code))

	switch code {
	case 0:
		return nil
	case runner.ExitFatal:
		return &runner.ExitCodeError{Code: code, Err: fatal}
	case runner.ExitWriteFailed:
		return &runner.ExitCodeError{Code: code, Err: fmt.Errorf("could not write the results or the search report")}
	default:
		return &runner.ExitCodeError{Code: code, Err: fmt.Errorf("stopped early: -max-blocked reached")}
	}
}

// writeFailure remembers that writing the results file failed, and stops the
// run when it first does (onFail), since nothing more can be saved.
type writeFailure struct {
	failed atomic.Bool
	onFail func()
}

// failRecorder passes writes through and records a failure. After a failure it
// swallows the data (and reports success to the writer): scrapemate's workers
// block on an unbuffered results channel if the writer stops reading, which
// would hang the run instead of ending it with exit code 4.
type failRecorder struct {
	w io.Writer
	f *writeFailure
}

func (fr *failRecorder) Write(p []byte) (int, error) {
	if fr.f.failed.Load() {
		return len(p), nil
	}

	n, err := fr.w.Write(p)
	if err != nil {
		if fr.f.failed.CompareAndSwap(false, true) && fr.f.onFail != nil {
			fr.f.onFail()
		}

		return len(p), nil
	}

	return n, nil
}

func (r *fileRunner) Close(context.Context) error {
	if r.report != nil {
		_ = r.report.Close()
	}

	if r.app != nil {
		return r.app.Close()
	}

	if r.input != nil {
		if closer, ok := r.input.(io.Closer); ok {
			return closer.Close()
		}
	}

	if r.outfile != nil {
		return r.outfile.Close()
	}

	return nil
}

func (r *fileRunner) seedJobOptions() []runner.SeedJobOption {
	if !r.cfg.Resume {
		return nil
	}

	return []runner.SeedJobOption{
		runner.WithDeterministicSeedIDs(),
		runner.WithCompletedInputSkipper(r.resumeState.IsInputCompleted),
		runner.WithCompletionTracker(r.resumeProgress),
	}
}

// initReport creates the tracker of a jsonl batch and, with -search-report, the
// report file (replacing an old one). It runs after the task file is validated
// and before any request; a file that cannot be created is exit code 4.
func (r *fileRunner) initReport() error {
	if r.cfg.SearchReport != "" {
		w, err := gmaps.NewReportWriter(r.cfg.SearchReport)
		if err != nil {
			return &runner.ExitCodeError{Code: runner.ExitWriteFailed, Err: fmt.Errorf("cannot create the search report file: %w", err)}
		}

		r.report = w
	}

	r.tracker = gmaps.NewTaskTracker(r.report, runner.EngineVersion(), nil)

	if r.cfg.MaxBlocked > 0 {
		r.gate = gmaps.NewAbortController(r.cfg.AbortGrace)
		r.tracker.SetBlockedThreshold(r.cfg.MaxBlocked, r.gate.Trigger)
	}

	return nil
}

// loadTasks reads the whole task file (-input-format jsonl) and keeps the tasks.
// Problems are returned as *runner.InputError (exit code 2).
func (r *fileRunner) loadTasks() error {
	var in io.Reader

	if r.cfg.InputFile == "stdin" {
		in = os.Stdin
	} else {
		f, err := os.Open(r.cfg.InputFile)
		if err != nil {
			return err
		}

		defer f.Close()

		in = f
	}

	tasks, errs := runner.ParseTasksJSONL(in)
	if len(errs) > 0 {
		lines := make([]string, 0, len(errs))
		for _, e := range errs {
			lines = append(lines, e.String())
		}

		return &runner.InputError{Lines: lines}
	}

	r.jsonl = true
	r.tasks = tasks
	r.batchMode = runner.ModeList

	if len(tasks) > 0 {
		r.batchMode = tasks[0].Mode
	}

	return nil
}

func (r *fileRunner) setInput() error {
	switch r.cfg.InputFile {
	case "stdin":
		r.input = os.Stdin
	default:
		f, err := os.Open(r.cfg.InputFile)
		if err != nil {
			return err
		}

		r.input = f
	}

	return nil
}

func (r *fileRunner) setWriters() error {
	switch {
	case r.cfg.CustomWriter != "":
		parts := strings.Split(r.cfg.CustomWriter, ":")
		if len(parts) != 2 {
			return fmt.Errorf("invalid custom writer format: %s", r.cfg.CustomWriter)
		}

		dir, pluginName := parts[0], parts[1]

		customWriter, err := runner.LoadCustomWriter(dir, pluginName)
		if err != nil {
			return err
		}

		r.writers = append(r.writers, customWriter)
	case r.cfg.LeadsDBAPIKey != "":
		r.writers = append(r.writers, leadsdb.New(r.cfg.LeadsDBAPIKey))
	default:
		if !r.cfg.Resume {
			if err := removeResumeState(r.cfg.ResultsFile); err != nil {
				return err
			}
		}

		var resultsWriter io.Writer

		switch r.cfg.ResultsFile {
		case "stdout":
			resultsWriter = os.Stdout
		default:
			f, err := r.openResultsFile()
			if err != nil {
				if r.jsonl {
					return &runner.ExitCodeError{Code: runner.ExitWriteFailed, Err: fmt.Errorf("cannot create the results file: %w", err)}
				}

				return err
			}

			r.outfile = f

			resultsWriter = r.outfile
		}

		if r.jsonl {
			resultsWriter = &failRecorder{w: resultsWriter, f: &r.fail}
		}

		switch {
		case r.cfg.Resume:
			if err := r.initResume(); err != nil {
				return err
			}

			if r.cfg.JSON {
				r.writers = append(r.writers, resume.NewJSONLAppendWriter(
					resultsWriter,
					r.resumeIDs,
					r.resumeProgress,
					r.outfile.Sync,
				))
			} else {
				writeHeader, headerErr := r.shouldWriteCSVHeader()
				if headerErr != nil {
					return headerErr
				}

				r.writers = append(r.writers, resume.NewCSVAppendWriter(
					csv.NewWriter(resultsWriter),
					writeHeader,
					r.resumeIDs,
					r.resumeProgress,
					r.outfile.Sync,
				))
			}
		case r.cfg.JSON:
			r.writers = append(r.writers, jsonwriter.NewJSONWriter(resultsWriter))
		default:
			csvWriter := csvwriter.NewCsvWriter(csv.NewWriter(resultsWriter))
			r.writers = append(r.writers, csvWriter)
		}
	}

	return nil
}

func validateResumeFiles(resultsPath string) error {
	if _, err := os.Stat(resultsPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	if _, err := os.Stat(resume.DefaultStatePath(resultsPath)); err == nil {
		return fmt.Errorf("resume state exists but results file is missing")
	} else if !os.IsNotExist(err) {
		return err
	}

	return nil
}

func removeResumeState(resultsPath string) error {
	err := os.Remove(resume.DefaultStatePath(resultsPath))
	if os.IsNotExist(err) {
		return nil
	}

	return err
}

func (r *fileRunner) openResultsFile() (*os.File, error) {
	if r.cfg.Resume {
		return os.OpenFile(r.cfg.ResultsFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	}

	return os.Create(r.cfg.ResultsFile)
}

func (r *fileRunner) initResume() error {
	ids, err := resume.LoadResultIdentities(r.cfg.ResultsFile, r.cfg.JSON)
	if err != nil {
		return err
	}

	state, err := resume.LoadState(resume.DefaultStatePath(r.cfg.ResultsFile))
	if err != nil {
		return err
	}

	r.resumeIDs = ids
	r.resumeDedup = ids.Clone()
	r.resumeState = state
	r.resumeProgress = resume.NewProgressTracker(state)

	return nil
}

func (r *fileRunner) shouldWriteCSVHeader() (bool, error) {
	info, err := os.Stat(r.cfg.ResultsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}

		return false, err
	}

	return info.Size() == 0, nil
}

func (r *fileRunner) setApp() error {
	opts := []func(*scrapemateapp.Config) error{
		// scrapemateapp.WithCache("leveldb", "cache"),
		scrapemateapp.WithConcurrency(r.cfg.Concurrency),
		scrapemateapp.WithExitOnInactivity(r.cfg.ExitOnInactivityDuration),
	}

	if len(r.cfg.Proxies) > 0 {
		opts = append(opts,
			scrapemateapp.WithProxies(r.cfg.Proxies),
		)
	}

	// certificate verification is part of the jsonl contract (docs/contracts.md §1,
	// ADR-0013); stock lines mode keeps upstream's behaviour
	opts = append(opts, tlsOptions(r.jsonl)...)

	switch {
	case r.jsonl:
		opts = append(opts, jsonlFetcherOptions(fetcherKind(r.batchMode, r.cfg.Debug))...)

		if r.gate != nil {
			opts = append(opts, scrapemateapp.WithProvider(gmaps.NewAbortGate(r.gate)))
		}
	case !r.cfg.FastMode:
		if r.cfg.Debug {
			opts = append(opts, scrapemateapp.WithJS(
				scrapemateapp.Headfull(),
				scrapemateapp.DisableImages(),
			))
		} else {
			opts = append(opts, scrapemateapp.WithJS(scrapemateapp.DisableImages()))
		}
	default:
		opts = append(opts, scrapemateapp.WithStealth("firefox"))
	}

	opts = runner.AppendBrowserCapacityOptions(opts, r.cfg)

	if !r.cfg.DisablePageReuse {
		opts = append(opts,
			scrapemateapp.WithPageReuseLimit(2),
			scrapemateapp.WithBrowserReuseLimit(200),
		)
	}

	matecfg, err := scrapemateapp.NewConfig(
		r.writers,
		opts...,
	)
	if err != nil {
		return err
	}

	r.app, err = scrapemateapp.NewScrapeMateApp(matecfg)
	if err != nil {
		return err
	}

	return nil
}

// Fetcher kinds for -input-format jsonl, chosen from the batch mode.
const (
	fetcherStealth     = "stealth"
	fetcherJS          = "js"
	fetcherJSHeadfull  = "js-headfull"
	stealthFingerprint = "firefox"
)

// fetcherKind picks the fetcher of a jsonl batch: list tasks use the stealth
// HTTP client (as stock fast mode), browser tasks use Playwright (as stock).
func fetcherKind(batchMode string, debug bool) string {
	switch {
	case batchMode != runner.ModeBrowser:
		return fetcherStealth
	case debug:
		return fetcherJSHeadfull
	default:
		return fetcherJS
	}
}

// tlsOptions returns the scrapemate options that make the fetchers verify the
// server certificate: on for every jsonl run (list and browser mode, with or
// without a proxy), nothing for the other modes, which stay as upstream.
func tlsOptions(jsonl bool) []func(*scrapemateapp.Config) error {
	if !jsonl {
		return nil
	}

	return []func(*scrapemateapp.Config) error{scrapemateapp.WithVerifyTLS(true)}
}

func jsonlFetcherOptions(kind string) []func(*scrapemateapp.Config) error {
	switch kind {
	case fetcherJSHeadfull:
		return []func(*scrapemateapp.Config) error{
			scrapemateapp.WithJS(scrapemateapp.Headfull(), scrapemateapp.DisableImages()),
		}
	case fetcherJS:
		return []func(*scrapemateapp.Config) error{scrapemateapp.WithJS(scrapemateapp.DisableImages())}
	default:
		return []func(*scrapemateapp.Config) error{scrapemateapp.WithStealth(stealthFingerprint)}
	}
}
