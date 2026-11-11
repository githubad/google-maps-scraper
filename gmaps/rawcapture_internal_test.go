package gmaps

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/gosom/scrapemate"
	"github.com/stretchr/testify/require"
)

func gunzipFile(t *testing.T, path string) []byte {
	t.Helper()

	f, err := os.Open(path)
	require.NoError(t, err)

	defer f.Close()

	zr, err := gzip.NewReader(f)
	require.NoError(t, err)

	out, err := io.ReadAll(zr)
	require.NoError(t, err)

	return out
}

func TestRawCapture_WritesExactBytes(t *testing.T) {
	// AS-6 (unit)
	dir := filepath.Join(t.TempDir(), "nested", "raw")
	bodies := [][]byte{
		withPrefix([]byte(`[["a"]]`)),
		withPrefix([]byte(`[["b"]]`)),
		[]byte(")]}'\n\x00\xff binary \r\n"),
	}

	for page, body := range bodies {
		require.NoError(t, writeRaw(dir, "t_x", page, body))
	}

	for page, body := range bodies {
		got := gunzipFile(t, filepath.Join(dir, "t_x_p"+string(rune('0'+page))+".json.gz"))
		require.True(t, bytes.Equal(body, got), "page %d", page)
	}
}

func TestRawCapture_AtomicNoTmpLeft(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, writeRaw(dir, "t_x", 0, []byte("body")))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "t_x_p0.json.gz", entries[0].Name())
}

func TestRawCapture_Overwrites(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, writeRaw(dir, "t_x", 0, []byte("first")))
	require.NoError(t, writeRaw(dir, "t_x", 0, []byte("second")))
	require.Equal(t, []byte("second"), gunzipFile(t, filepath.Join(dir, "t_x_p0.json.gz")))
}

func TestRawCapture_RejectsBadTaskID(t *testing.T) {
	dir := t.TempDir()

	for _, id := range []string{"", "../x", "a/b", `a\b`, "a b"} {
		require.Error(t, writeRaw(dir, id, 0, []byte("x")), id)
	}

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)

	_, statErr := os.Stat(filepath.Join(filepath.Dir(dir), "x_p0.json.gz"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestMaskSecrets(t *testing.T) {
	t.Parallel()

	cases := map[string]string{ //nolint:gosec // test data, not credentials
		"http://user:pass@host:8080/x":         "http://***@host:8080/x",
		"socks5://a:b@h":                       "socks5://***@h",
		"no credentials here":                  "no credentials here",
		"a http://u:p@h1 and https://x:y@h2/z": "a http://***@h1 and https://***@h2/z",
	}

	for in, want := range cases {
		require.Equal(t, want, maskSecrets(in))
	}
}

func TestRawCapture_UnwritableDirWarnsOnly(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))

	var logs bytes.Buffer

	log.SetOutput(&logs)

	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	job := newFixtureJob(WithSearchJobTaskID("t_x"), WithSearchJobRawDir(blocker+"/sub"))

	res, _, err := processFixture(t, job)
	require.NoError(t, err, "a capture failure never fails the task")
	require.Len(t, flattenResult(res), 20)

	require.Contains(t, logs.String(), "raw capture")
	require.NotContains(t, logs.String(), "secret")
}

func TestRawCapture_ThroughProcess(t *testing.T) {
	// AS-6 (end to end with the driver)
	full := outputPage(t)
	short := trimRows(t, full)

	srv, _ := newBodyServer(t, func(offset int) []byte {
		if offset >= 40 {
			return short
		}

		return full
	})

	dir := t.TempDir()

	runTask(t, newPagedJob(srv.URL, 6, WithSearchJobRawDir(dir)))

	require.Equal(t, full, gunzipFile(t, filepath.Join(dir, "t_x_p0.json.gz")))
	require.Equal(t, full, gunzipFile(t, filepath.Join(dir, "t_x_p1.json.gz")))
	require.Equal(t, short, gunzipFile(t, filepath.Join(dir, "t_x_p2.json.gz")))

	t.Run("an unparseable page is still captured", func(t *testing.T) {
		bad := []byte(")]}'\n{not json")
		dir := t.TempDir()

		job := newPagedJob("http://unused.invalid", 2, WithSearchJobRawDir(dir))

		_, _, err := job.Process(context.Background(), &scrapemate.Response{Body: bad})
		require.Error(t, err)
		require.Equal(t, bad, gunzipFile(t, filepath.Join(dir, "t_x_p0.json.gz")))
	})

	t.Run("an empty body writes no file and no directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "raw")

		job := newPagedJob("http://unused.invalid", 2, WithSearchJobRawDir(dir))

		_, _, err := job.Process(context.Background(), &scrapemate.Response{Body: nil})
		require.Error(t, err)

		_, statErr := os.Stat(dir)
		require.ErrorIs(t, statErr, os.ErrNotExist)
	})

	t.Run("no raw dir writes nothing", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "raw")

		_, _, err := processFixture(t, newFixtureJob(WithSearchJobTaskID("t_x")))
		require.NoError(t, err)

		_, statErr := os.Stat(dir)
		require.ErrorIs(t, statErr, os.ErrNotExist)
	})
}
