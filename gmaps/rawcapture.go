package gmaps

import (
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

var (
	rawTaskIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	secretInURL      = regexp.MustCompile(`://[^@/\s]+@`)
)

// maskSecrets renders "://user:pass@" as "://***@" so credentials never reach logs.
func maskSecrets(s string) string {
	return secretInURL.ReplaceAllString(s, "://***@")
}

// writeRaw saves the exact response body of one page as
// <dir>/<taskID>_p<page>.json.gz. The file is written under a temporary name
// and renamed, so a reader never sees a partial file. dir is created if missing.
func writeRaw(dir, taskID string, page int, body []byte) error {
	if !rawTaskIDPattern.MatchString(taskID) {
		return fmt.Errorf("invalid task id for raw capture")
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	final := filepath.Join(dir, taskID+"_p"+strconv.Itoa(page)+".json.gz")
	tmp := final + ".tmp"

	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	if err := writeGzip(f, body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)

		return err
	}

	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)

		return err
	}

	return nil
}

// writeGzip writes body gzipped to f, flushes it to disk and closes f.
func writeGzip(f *os.File, body []byte) error {
	zw := gzip.NewWriter(f)

	if _, err := zw.Write(body); err != nil {
		return err
	}

	if err := zw.Close(); err != nil {
		return err
	}

	if err := f.Sync(); err != nil {
		return err
	}

	return f.Close()
}
