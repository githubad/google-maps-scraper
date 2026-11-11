package runner

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// byteOrderMark may start a file saved by some editors; it is ignored.
var byteOrderMark = string(rune(0xFEFF))

// fieldJSON names the pseudo-field of errors about the line itself.
const fieldJSON = "json"

// Task modes.
const (
	ModeList    = "list"
	ModeBrowser = "browser"
)

const (
	maxQueryRunes = 200
	minLat        = -90.0
	maxLat        = 90.0
	minLng        = -180.0
	maxLng        = 180.0
	minZoom       = 3.0
	maxZoom       = 21.0
	minSpanM      = 100.0
	maxSpanM      = 200000.0
	minPages      = 1
	maxPages      = 6
	minDepth      = 1
	maxDepth      = 20
)

var (
	taskIDPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	taskLangPattern = regexp.MustCompile(`^[A-Za-z]{2}$`)
)

// SearchTask is one line of a -input-format jsonl file (docs/contracts.md §2).
type SearchTask struct {
	ID       string
	Query    string
	Mode     string
	Lat      float64
	Lng      float64
	Zoom     float64
	SpanM    *float64
	MaxPages *int
	Depth    *int
	RadiusM  *float64
	Lang     string
}

// ValidationError is one problem in a task file, reported as
// "line <n>: <field>: <reason>".
type ValidationError struct {
	Line   int
	Field  string
	Reason string
}

func (e ValidationError) String() string {
	return fmt.Sprintf("line %d: %s: %s", e.Line, e.Field, e.Reason)
}

// taskFields lists the known keys in the order errors are reported.
var taskFields = []string{
	"id", "query", "mode", "lat", "lng", "zoom", "span_m", "max_pages", "depth", "radius_m", "lang",
}

// ParseTasksJSONL reads a whole task file. It returns the valid tasks and every
// error found; callers must not run anything when there is an error.
func ParseTasksJSONL(r io.Reader) ([]SearchTask, []ValidationError) {
	var (
		tasks    []SearchTask
		errs     []ValidationError
		idLines  = map[string]int{}
		fileMode string
		modeLine int
	)

	reader := bufio.NewReader(r)

	for lineNo := 1; ; lineNo++ {
		raw, readErr := reader.ReadString('\n')

		if lineNo == 1 {
			raw = strings.TrimPrefix(raw, byteOrderMark)
		}

		line := strings.TrimSpace(raw)

		if line != "" {
			task, lineErrs := parseTaskLine(lineNo, line)
			errs = append(errs, lineErrs...)

			// Cross-line rules use the parts of the line that were valid.
			if task.ID != "" && taskIDPattern.MatchString(task.ID) {
				if first, dup := idLines[task.ID]; dup {
					errs = append(errs, ValidationError{lineNo, "id", fmt.Sprintf("duplicate of line %d", first)})
				} else {
					idLines[task.ID] = lineNo
				}
			}

			if task.Mode == ModeList || task.Mode == ModeBrowser {
				switch {
				case fileMode == "":
					fileMode, modeLine = task.Mode, lineNo
				case task.Mode != fileMode:
					errs = append(errs, ValidationError{
						lineNo, "mode",
						fmt.Sprintf("all tasks must share one mode (%s from line %d)", fileMode, modeLine),
					})
				}
			}

			if len(lineErrs) == 0 {
				tasks = append(tasks, task)
			}
		}

		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				errs = append(errs, ValidationError{lineNo, "read", readErr.Error()})
			}

			break
		}
	}

	sort.SliceStable(errs, func(i, j int) bool { return errs[i].Line < errs[j].Line })

	if len(errs) > 0 {
		return nil, errs
	}

	return tasks, nil
}

// parseTaskLine validates one non-blank line. The returned task carries the
// fields that were valid, so cross-line checks can still use id and mode.
func parseTaskLine(lineNo int, line string) (SearchTask, []ValidationError) {
	var decoded any

	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()

	if err := dec.Decode(&decoded); err != nil {
		return SearchTask{}, []ValidationError{{lineNo, fieldJSON, err.Error()}}
	}

	if dec.More() {
		return SearchTask{}, []ValidationError{{lineNo, fieldJSON, "unexpected data after the object"}}
	}

	obj, ok := decoded.(map[string]any)
	if !ok {
		return SearchTask{}, []ValidationError{{lineNo, fieldJSON, "must be an object"}}
	}

	p := &taskParser{line: lineNo, obj: obj}

	var task SearchTask

	task.ID = p.id()
	task.Query = p.query()
	task.Mode = p.mode()
	task.Lat = p.number("lat", true, minLat, maxLat, "must be between -90 and 90")
	task.Lng = p.number("lng", true, minLng, maxLng, "must be between -180 and 180")
	task.Zoom = p.number("zoom", true, minZoom, maxZoom, "must be between 3 and 21")

	listOnly := func(field string) bool { return p.modeSpecific(field, ModeList, task.Mode) }
	browserOnly := func(field string) bool { return p.modeSpecific(field, ModeBrowser, task.Mode) }

	if listOnly("span_m") {
		task.SpanM = p.optionalNumber("span_m", minSpanM, maxSpanM, "must be between 100 and 200000")
	}

	if listOnly("max_pages") {
		task.MaxPages = p.optionalInt("max_pages", minPages, maxPages, "must be an integer between 1 and 6")
	}

	if browserOnly("depth") {
		task.Depth = p.optionalInt("depth", minDepth, maxDepth, "must be an integer between 1 and 20")
	}

	task.RadiusM = p.optionalNumber("radius_m", 0, math.MaxFloat64, "must be 0 or more")
	task.Lang = p.lang()

	p.unknownKeys()

	return task, p.errs
}

type taskParser struct {
	line int
	obj  map[string]any
	errs []ValidationError
}

func (p *taskParser) fail(field, reason string) {
	p.errs = append(p.errs, ValidationError{p.line, field, reason})
}

// present returns the raw value and true when the key exists and is not null.
func (p *taskParser) present(field string) (any, bool) {
	v, ok := p.obj[field]
	if !ok || v == nil {
		return nil, false
	}

	return v, true
}

func (p *taskParser) str(field string, required bool) (string, bool) {
	v, ok := p.present(field)
	if !ok {
		if required {
			p.fail(field, "is required")
		}

		return "", false
	}

	s, isStr := v.(string)
	if !isStr {
		p.fail(field, "must be a string")

		return "", false
	}

	return s, true
}

func (p *taskParser) id() string {
	s, ok := p.str("id", true)
	if !ok {
		return ""
	}

	if !taskIDPattern.MatchString(s) {
		p.fail("id", "must match ^[A-Za-z0-9_-]{1,64}$")
	}

	return s
}

func (p *taskParser) query() string {
	s, ok := p.str("query", true)
	if !ok {
		return ""
	}

	if n := utf8.RuneCountInString(s); n < 1 || n > maxQueryRunes {
		p.fail("query", "must be 1 to 200 characters")

		return s
	}

	if strings.IndexFunc(s, unicode.IsControl) >= 0 {
		p.fail("query", "must not contain control characters")
	}

	return s
}

func (p *taskParser) mode() string {
	s, ok := p.str("mode", false)
	if !ok {
		return ModeList
	}

	if s != ModeList && s != ModeBrowser {
		p.fail("mode", "must be list or browser")

		return s
	}

	return s
}

func (p *taskParser) lang() string {
	s, ok := p.str("lang", false)
	if ok && !taskLangPattern.MatchString(s) {
		p.fail("lang", "must be 2 letters")
	}

	return s
}

// modeSpecific reports whether a mode-specific field should be parsed. When the
// field is present in a task of the other mode it is an error.
func (p *taskParser) modeSpecific(field, wantMode, taskMode string) bool {
	if _, ok := p.present(field); !ok {
		return false
	}

	if taskMode == ModeList || taskMode == ModeBrowser {
		if taskMode != wantMode {
			p.fail(field, "only valid in "+wantMode+" mode")

			return false
		}
	}

	return true
}

func (p *taskParser) float(field string) (float64, bool) {
	v, _ := p.present(field)

	n, ok := v.(json.Number)
	if !ok {
		p.fail(field, "must be a number")

		return 0, false
	}

	f, err := n.Float64()
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		p.fail(field, "must be a number")

		return 0, false
	}

	return f, true
}

func (p *taskParser) number(field string, required bool, lo, hi float64, reason string) float64 {
	if _, ok := p.present(field); !ok {
		if required {
			p.fail(field, "is required")
		}

		return 0
	}

	f, ok := p.float(field)
	if !ok {
		return 0
	}

	if f < lo || f > hi {
		p.fail(field, reason)
	}

	return f
}

func (p *taskParser) optionalNumber(field string, lo, hi float64, reason string) *float64 {
	if _, ok := p.present(field); !ok {
		return nil
	}

	f, ok := p.float(field)
	if !ok {
		return nil
	}

	if f < lo || f > hi {
		p.fail(field, reason)

		return nil
	}

	return &f
}

func (p *taskParser) optionalInt(field string, lo, hi int, reason string) *int {
	if _, ok := p.present(field); !ok {
		return nil
	}

	f, ok := p.float(field)
	if !ok {
		return nil
	}

	if f != math.Trunc(f) || f < float64(lo) || f > float64(hi) {
		p.fail(field, reason)

		return nil
	}

	n := int(f)

	return &n
}

// unknownKeys reports keys that are neither known nor "x_" metadata, sorted.
func (p *taskParser) unknownKeys() {
	known := make(map[string]struct{}, len(taskFields))
	for _, f := range taskFields {
		known[f] = struct{}{}
	}

	var unknown []string

	for k := range p.obj {
		if _, ok := known[k]; ok || strings.HasPrefix(k, "x_") {
			continue
		}

		unknown = append(unknown, k)
	}

	sort.Strings(unknown)

	for _, k := range unknown {
		p.fail(k, "unknown field")
	}
}
