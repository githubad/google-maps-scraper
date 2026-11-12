package gmaps

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func readGzipFile(t *testing.T, path string) []byte {
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

func TestWriteRawNote_ShapeAndKeyOrder(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "raw")

	require.NoError(t, writeRawNote(dir, "t_1", 2, "maps.google.com", "unknown_authority",
		"x509: certificate signed by unknown authority"))

	got := readGzipFile(t, filepath.Join(dir, "t_1_p2.json.gz"))

	require.Equal(t,
		`{"note":"tls_cert","host":"maps.google.com","class":"unknown_authority","message":"x509: certificate signed by unknown authority","page":2}`,
		strings.TrimSuffix(string(got), "\n"), "one JSON line, keys in this order")
	require.False(t, bytes.HasPrefix(got, []byte(")]}'")), "a note is not a page")
	require.True(t, bytes.HasPrefix(got, []byte(`{"note":`)), "readers tell a note by this prefix")

	// no temporary file is left behind
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestWriteRawNote_MessageIsShortSingleLineAndMasked(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	long := "proxy http://user:secret@gw.example:8080 said:\nline two\r\n" + strings.Repeat("x", 500)

	require.NoError(t, writeRawNote(dir, "t_2", 0, "maps.google.com", "other", long))

	var note struct {
		Note    string `json:"note"`
		Message string `json:"message"`
	}

	require.NoError(t, json.Unmarshal(readGzipFile(t, filepath.Join(dir, "t_2_p0.json.gz")), &note))

	require.Equal(t, "tls_cert", note.Note)
	require.LessOrEqual(t, len([]rune(note.Message)), 200)
	require.NotContains(t, note.Message, "\n")
	require.NotContains(t, note.Message, "\r")
	require.NotContains(t, note.Message, "secret")
	require.Contains(t, note.Message, "://***@")
}

func TestWriteRawNote_InvalidTaskIDIsAnError(t *testing.T) {
	t.Parallel()

	err := writeRawNote(t.TempDir(), "../evil", 0, "h", "other", "m")

	require.Error(t, err)
}

func TestWriteRawNote_OverwritesTheSameFileName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	require.NoError(t, writeRawNote(dir, "t_3", 1, "a.test", "expired", "first"))
	require.NoError(t, writeRawNote(dir, "t_3", 1, "b.test", "other", "second"))

	got := string(readGzipFile(t, filepath.Join(dir, "t_3_p1.json.gz")))
	require.Contains(t, got, "b.test")
	require.NotContains(t, got, "a.test")
}
