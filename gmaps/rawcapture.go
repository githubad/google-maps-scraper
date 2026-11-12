package gmaps

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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

// noteMessageMax is the longest message a raw note carries.
const noteMessageMax = 200

// rawNote is the content of the raw file of a page whose handshake failed
// certificate verification (docs/contracts.md §5): there is no body, so a small
// JSON note stands in for it. The key order is part of the format. It never holds
// certificate or response bytes.
type rawNote struct {
	Note    string `json:"note"`
	Host    string `json:"host"`
	Class   string `json:"class"`
	Message string `json:"message"`
	Page    int    `json:"page"`
}

// noteMessage makes an error text fit a note: credentials masked, one line, at
// most noteMessageMax characters.
func noteMessage(s string) string {
	s = maskSecrets(s)
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)

	if r := []rune(s); len(r) > noteMessageMax {
		s = string(r[:noteMessageMax])
	}

	return s
}

// writeRawNote saves the note of a certificate verification failure under the
// file name a page body would have had: <dir>/<taskID>_p<page>.json.gz. A raw
// file whose content starts with {"note": is a note, not a page.
func writeRawNote(dir, taskID string, page int, host, class, message string) error {
	b, err := json.Marshal(rawNote{
		Note:    string(ReasonTLSCert),
		Host:    host,
		Class:   class,
		Message: noteMessage(message),
		Page:    page,
	})
	if err != nil {
		return err
	}

	return writeRaw(dir, taskID, page, append(b, '\n'))
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
