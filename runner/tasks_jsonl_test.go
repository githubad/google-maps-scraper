package runner_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/runner"
)

func parseJSONL(t *testing.T, input string) (tasks []runner.SearchTask, rendered []string) {
	t.Helper()

	tasks, errs := runner.ParseTasksJSONL(strings.NewReader(input))

	rendered = make([]string, 0, len(errs))
	for _, e := range errs {
		rendered = append(rendered, e.String())
	}

	return tasks, rendered
}

// taskLine marshals a task object from base plus overrides; a nil override deletes the key.
func taskLine(t *testing.T, base, over map[string]any) string {
	t.Helper()

	m := map[string]any{}
	for k, v := range base {
		m[k] = v
	}

	for k, v := range over {
		if v == nil {
			if _, isNullable := nullable[k]; !isNullable {
				delete(m, k)

				continue
			}
		}

		m[k] = v
	}

	raw, err := json.Marshal(m)
	require.NoError(t, err)

	return string(raw)
}

// nullable lists override keys that must be written as JSON null instead of deleted.
var nullable = map[string]struct{}{}

var listBase = map[string]any{"id": "t_1", "query": "roofer", "lat": 30.0, "lng": -97.0, "zoom": 14.0}

var browserBase = map[string]any{
	"id": "t_1", "query": "roofer", "mode": "browser", "lat": 30.0, "lng": -97.0, "zoom": 13.0,
}

func TestParseTasksJSONL_Valid(t *testing.T) {
	t.Parallel()

	t.Run("list tasks with every optional field", func(t *testing.T) {
		t.Parallel()

		input := "\xef\xbb\xbf" +
			`{"id":"t_1","query":"roofing contractor","lat":30.2672,"lng":-97.7431,"zoom":14}` + "\r\n" +
			"\r\n" +
			`{"id":"t-2","query":"techos en Bogotá","mode":"list","lat":4.711,"lng":-74.0721,"zoom":13.5,` +
			`"span_m":5000,"max_pages":3,"radius_m":2500,"lang":"es","x_note":{"a":[1,2]},"x_n":1}` + "\n"

		tasks, errs := parseJSONL(t, input)
		require.Empty(t, errs)
		require.Len(t, tasks, 2)

		first := tasks[0]
		require.Equal(t, "t_1", first.ID)
		require.Equal(t, "roofing contractor", first.Query)
		require.Equal(t, "list", first.Mode, "mode defaults to list")
		require.InDelta(t, 30.2672, first.Lat, 1e-9)
		require.InDelta(t, -97.7431, first.Lng, 1e-9)
		require.InDelta(t, 14, first.Zoom, 1e-9)
		require.Nil(t, first.SpanM)
		require.Nil(t, first.MaxPages)
		require.Nil(t, first.Depth)
		require.Nil(t, first.RadiusM)
		require.Empty(t, first.Lang)

		second := tasks[1]
		require.Equal(t, "t-2", second.ID)
		require.Equal(t, "techos en Bogotá", second.Query)
		require.InDelta(t, 13.5, second.Zoom, 1e-9)
		require.NotNil(t, second.SpanM)
		require.InDelta(t, 5000, *second.SpanM, 1e-9)
		require.NotNil(t, second.MaxPages)
		require.Equal(t, 3, *second.MaxPages)
		require.NotNil(t, second.RadiusM)
		require.InDelta(t, 2500, *second.RadiusM, 1e-9)
		require.Equal(t, "es", second.Lang)
	})

	t.Run("browser tasks", func(t *testing.T) {
		t.Parallel()

		input := taskLine(t, browserBase, map[string]any{"depth": 5}) + "\n" +
			taskLine(t, browserBase, map[string]any{"id": "t_2", "x_span_m": 5000}) + "\n"

		tasks, errs := parseJSONL(t, input)
		require.Empty(t, errs)
		require.Len(t, tasks, 2)
		require.Equal(t, "browser", tasks[0].Mode)
		require.NotNil(t, tasks[0].Depth)
		require.Equal(t, 5, *tasks[0].Depth)
		require.Nil(t, tasks[1].Depth)
	})

	t.Run("limits are accepted", func(t *testing.T) {
		t.Parallel()

		longQuery := strings.Repeat("é", 200)
		longLine := taskLine(t, listBase, map[string]any{
			"id": strings.Repeat("a", 64), "query": longQuery, "x_note": strings.Repeat("n", 70000),
		})

		tasks, errs := parseJSONL(t, longLine+"\n")
		require.Empty(t, errs)
		require.Len(t, tasks, 1)
		require.Equal(t, longQuery, tasks[0].Query)
	})
}

func TestParseTasksJSONL_FieldRules(t *testing.T) {
	t.Parallel()

	const (
		idRule    = "line 1: id: must match ^[A-Za-z0-9_-]{1,64}$"
		queryLen  = "line 1: query: must be 1 to 200 characters"
		queryCtrl = "line 1: query: must not contain control characters"
	)

	tests := []struct {
		name string
		base map[string]any
		over map[string]any
		want string
	}{
		{"lat too high", listBase, map[string]any{"lat": 95.0}, "line 1: lat: must be between -90 and 90"},
		{"lat too low", listBase, map[string]any{"lat": -90.5}, "line 1: lat: must be between -90 and 90"},
		{"lng too high", listBase, map[string]any{"lng": 181.0}, "line 1: lng: must be between -180 and 180"},
		{"zoom too low", listBase, map[string]any{"zoom": 2.0}, "line 1: zoom: must be between 3 and 21"},
		{"zoom too high", listBase, map[string]any{"zoom": 22.0}, "line 1: zoom: must be between 3 and 21"},
		{"span_m too low", listBase, map[string]any{"span_m": 99.0}, "line 1: span_m: must be between 100 and 200000"},
		{"span_m too high", listBase, map[string]any{"span_m": 200001.0}, "line 1: span_m: must be between 100 and 200000"},
		{"max_pages zero", listBase, map[string]any{"max_pages": 0}, "line 1: max_pages: must be an integer between 1 and 6"},
		{"max_pages seven", listBase, map[string]any{"max_pages": 7}, "line 1: max_pages: must be an integer between 1 and 6"},
		{"max_pages fraction", listBase, map[string]any{"max_pages": 3.5}, "line 1: max_pages: must be an integer between 1 and 6"},
		{"depth zero", browserBase, map[string]any{"depth": 0}, "line 1: depth: must be an integer between 1 and 20"},
		{"depth 21", browserBase, map[string]any{"depth": 21}, "line 1: depth: must be an integer between 1 and 20"},
		{"depth fraction", browserBase, map[string]any{"depth": 2.5}, "line 1: depth: must be an integer between 1 and 20"},
		{"radius negative", listBase, map[string]any{"radius_m": -1.0}, "line 1: radius_m: must be 0 or more"},
		{"lang three letters", listBase, map[string]any{"lang": "eng"}, "line 1: lang: must be 2 letters"},
		{"lang with digit", listBase, map[string]any{"lang": "e1"}, "line 1: lang: must be 2 letters"},
		{"id with space", listBase, map[string]any{"id": "has space"}, idRule},
		{"id too long", listBase, map[string]any{"id": strings.Repeat("a", 65)}, idRule},
		{"id empty", listBase, map[string]any{"id": ""}, idRule},
		{"query empty", listBase, map[string]any{"query": ""}, queryLen},
		{"query 201 characters", listBase, map[string]any{"query": strings.Repeat("x", 201)}, queryLen},
		{"query newline", listBase, map[string]any{"query": "a\nb"}, queryCtrl},
		{"query NUL", listBase, map[string]any{"query": "a\u0000b"}, queryCtrl},
		{"mode unknown", listBase, map[string]any{"mode": "grid"}, "line 1: mode: must be list or browser"},
		{"id missing", listBase, map[string]any{"id": nil}, "line 1: id: is required"},
		{"query missing", listBase, map[string]any{"query": nil}, "line 1: query: is required"},
		{"lat missing", listBase, map[string]any{"lat": nil}, "line 1: lat: is required"},
		{"lng missing", listBase, map[string]any{"lng": nil}, "line 1: lng: is required"},
		{"zoom missing", listBase, map[string]any{"zoom": nil}, "line 1: zoom: is required"},
		{"unknown key", listBase, map[string]any{"frobnicate": 1}, "line 1: frobnicate: unknown field"},
		{"lat as string", listBase, map[string]any{"lat": "30"}, "line 1: lat: must be a number"},
		{"query as number", listBase, map[string]any{"query": 5}, "line 1: query: must be a string"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tasks, errs := parseJSONL(t, taskLine(t, tc.base, tc.over)+"\n")
			require.Equal(t, []string{tc.want}, errs)
			require.Empty(t, tasks)
		})
	}

	t.Run("null on a required field is required", func(t *testing.T) {
		t.Parallel()

		_, errs := parseJSONL(t, `{"id":"t_1","query":"q","lat":null,"lng":-97,"zoom":14}`+"\n")
		require.Equal(t, []string{"line 1: lat: is required"}, errs)
	})

	t.Run("null on optional fields means absent", func(t *testing.T) {
		t.Parallel()

		line := `{"id":"t_1","query":"q","lat":30,"lng":-97,"zoom":14,` +
			`"span_m":null,"max_pages":null,"radius_m":null,"lang":null,"mode":null,"depth":null}`

		tasks, errs := parseJSONL(t, line+"\n")
		require.Empty(t, errs)
		require.Len(t, tasks, 1)
		require.Equal(t, "list", tasks[0].Mode)
		require.Nil(t, tasks[0].SpanM)
	})

	t.Run("bad json", func(t *testing.T) {
		t.Parallel()

		_, errs := parseJSONL(t, "{bad\n")
		require.Len(t, errs, 1)
		require.True(t, strings.HasPrefix(errs[0], "line 1: json: "), errs[0])
	})

	t.Run("not an object", func(t *testing.T) {
		t.Parallel()

		for _, line := range []string{`[1,2]`, `"text"`, `5`, `null`} {
			_, errs := parseJSONL(t, line+"\n")
			require.Equal(t, []string{"line 1: json: must be an object"}, errs, line)
		}
	})
}

func TestParseTasksJSONL_ModeRules(t *testing.T) {
	t.Parallel()

	t.Run("duplicate id", func(t *testing.T) {
		t.Parallel()

		input := taskLine(t, listBase, nil) + "\n" +
			taskLine(t, listBase, map[string]any{"id": "t_2"}) + "\n" +
			taskLine(t, listBase, nil) + "\n"

		_, errs := parseJSONL(t, input)
		require.Equal(t, []string{"line 3: id: duplicate of line 1"}, errs)
	})

	t.Run("mixed modes", func(t *testing.T) {
		t.Parallel()

		input := taskLine(t, listBase, nil) + "\n" + taskLine(t, browserBase, map[string]any{"id": "t_2"}) + "\n"

		_, errs := parseJSONL(t, input)
		require.Equal(t, []string{"line 2: mode: all tasks must share one mode (list from line 1)"}, errs)
	})

	t.Run("wrong-mode fields are errors", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name string
			base map[string]any
			over map[string]any
			want string
		}{
			{"span_m in browser", browserBase, map[string]any{"span_m": 5000}, "line 1: span_m: only valid in list mode"},
			{"max_pages in browser", browserBase, map[string]any{"max_pages": 3}, "line 1: max_pages: only valid in list mode"},
			{"depth in list", listBase, map[string]any{"depth": 5}, "line 1: depth: only valid in browser mode"},
		}

		for _, tc := range cases {
			_, errs := parseJSONL(t, taskLine(t, tc.base, tc.over)+"\n")
			require.Equal(t, []string{tc.want}, errs, tc.name)
		}
	})

	t.Run("x_ keys and shared fields are fine in both modes", func(t *testing.T) {
		t.Parallel()

		input := taskLine(t, browserBase, map[string]any{"x_span_m": 5000, "radius_m": 100, "lang": "de"}) + "\n"

		tasks, errs := parseJSONL(t, input)
		require.Empty(t, errs)
		require.Len(t, tasks, 1)
	})

	t.Run("every error is reported in line order and blank lines count", func(t *testing.T) {
		t.Parallel()

		input := taskLine(t, listBase, map[string]any{"lat": 95.0}) + "\n" +
			"\n" +
			taskLine(t, listBase, map[string]any{"id": "t_2", "zoom": 1.0}) + "\n" +
			"{bad\n" +
			taskLine(t, listBase, map[string]any{"id": "t_5", "lang": "eng"}) + "\n"

		_, errs := parseJSONL(t, input)
		require.Len(t, errs, 4)
		require.Equal(t, "line 1: lat: must be between -90 and 90", errs[0])
		require.Equal(t, "line 3: zoom: must be between 3 and 21", errs[1])
		require.True(t, strings.HasPrefix(errs[2], "line 4: json: "))
		require.Equal(t, "line 5: lang: must be 2 letters", errs[3])
	})
}

func TestParseTasksJSONL_ZeroTasks(t *testing.T) {
	t.Parallel()

	for name, input := range map[string]string{
		"empty":      "",
		"blank only": "\n\n  \n\t\n",
		"bom only":   "\xef\xbb\xbf",
	} {
		tasks, errs := parseJSONL(t, input)
		require.Empty(t, tasks, name)
		require.Empty(t, errs, name)
	}
}
