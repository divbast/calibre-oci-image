// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/divbast/ci/harness"
)

// droppingQtWebEngine is the one Qt WebEngine version that can end a PDF
// conversion just as Chromium starts to draw the pages: its browser thread
// reads what the GPU thread makes, before that thread has made it, and
// Calibre's PDF worker stops with a segmentation fault. Qt fixed it in Qt
// WebEngine 6.10.2 (QTBUG-142720, QTBUG-142497). On a busy or small
// machine it happens to a few conversions in 100.
const droppingQtWebEngine = "6.10.1"

// qtWebEngineCode is Python, for calibre-debug, that prints the version of
// the Qt WebEngine in Calibre's bundle.
const qtWebEngineCode = "from qt.webengine import qWebEngineVersion; print(qWebEngineVersion())"

// threePart is a version as Qt prints one: major, minor, patch.
var threePart = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// bundledQt holds the bundle's Qt WebEngine version once the first test
// has read it.
var bundledQt struct {
	once    sync.Once
	version string
	err     error
}

// qtWebEngineVersion returns the version of the Qt WebEngine in img's
// Calibre bundle, read once in a run, by the bundle's calibre-debug, in a
// one-off container of img under the contract, on no network.
func qtWebEngineVersion(tb testing.TB, e harness.Engine, img string) string {
	tb.Helper()
	bundledQt.once.Do(func() {
		res, err := e.Run(tb.Context(), tb, &harness.RunSpec{
			Image:      img,
			Network:    "none",
			Confine:    harness.Contract(tmpfsSize),
			Env:        map[string]string{"CALIBRE_CONFIG_DIRECTORY": "/tmp/calibre-config"},
			Entrypoint: []string{"/opt/calibre/calibre-debug"},
			Args:       []string{"-c", qtWebEngineCode},
		})
		if err == nil && res.Code != 0 {
			err = fmt.Errorf("exit status %d: %s", res.Code, harness.Clip(string(res.Stderr)))
		}
		v := strings.TrimSpace(string(res.Stdout))
		if err == nil && !threePart.MatchString(v) {
			err = fmt.Errorf("it printed %q, which is no version", harness.Clip(v))
		}
		if err != nil {
			bundledQt.err = fmt.Errorf("reading the version of the Qt WebEngine in Calibre's bundle: %w", err)
			return
		}
		bundledQt.version = v
	})
	if bundledQt.err != nil {
		tb.Fatal(bundledQt.err)
	}
	return bundledQt.version
}

// drawing is the last line Calibre's PDF output logs before Chromium
// draws the pages of the fixture book, which is one HTML file.
const drawing = "Rendering 1 HTML files"

// chromiumLine is a line of Chromium's own log, which it writes to the
// same log when its log is on: "[pid:tid:date/time:LEVEL:file(line)] …",
// its level in the first group.
var chromiumLine = regexp.MustCompile(`^\[\d+:\d+:\d{4}/\d{6}\.\d{6}:([A-Z0-9_]+):[^\]]*\] `)

// chromiumNotFatal reports whether line is one of Chromium's own log lines
// at any level but FATAL. A FATAL line is Chromium stopping a process on
// purpose, which may itself be why the worker ended, so it is never passed
// over.
func chromiumNotFatal(line string) bool {
	m := chromiumLine.FindStringSubmatch(line)
	return len(m) == 2 && m[1] != "FATAL"
}

// diedDrawing reports whether a failed conversion's log and traceback, as
// the Content Server answers them, show Calibre's PDF worker ending as
// Chromium started to draw the pages: the last line of the log that is
// not one of Chromium's own, FATAL lines excepted, is drawing, and the
// traceback is calibre-server's
// end of its connection to the worker, reading EOFError, because the
// worker ended without an answer.
func diedDrawing(log, traceback string) bool {
	last := ""
	for line := range strings.Lines(log) {
		line = strings.TrimSpace(line)
		if line != "" && !chromiumNotFatal(line) {
			last = line
		}
	}
	trace := strings.TrimSpace(traceback)
	return last == drawing &&
		strings.HasPrefix(trace, "Traceback (most recent call last):\n") &&
		strings.Contains(trace, `File "calibre/utils/ipc/simple_worker.py"`) &&
		strings.Contains(trace, `File "multiprocessing/connection.py"`) &&
		strings.HasSuffix(trace, "\nEOFError")
}

// convertsAgain reports whether the PDF test converts again after st: only
// when the bundle's Qt WebEngine, qtWebEngine, is exactly the one that can
// end a conversion as the pages start to be drawn, and st failed in just
// that way. Any other version, older or newer, gets no second try.
func convertsAgain(qtWebEngine string, st conversion) bool {
	return qtWebEngine == droppingQtWebEngine && !st.OK && diedDrawing(st.Log, st.Traceback)
}

// convertPDF runs attempt, one conversion to PDF. When it fails as
// convertsAgain describes, convertPDF says so in the test's log and runs
// attempt again, once. It returns the last attempt's answer, so that what
// a caller checks of an attempt is of the one that counts.
func convertPDF(tb testing.TB, qtWebEngine string, attempt func() (conversion, error)) (conversion, error) {
	tb.Helper()
	st, err := attempt()
	if err != nil && convertsAgain(qtWebEngine, st) {
		tb.Logf("converting again, once: Calibre's PDF worker ended just as Chromium started to draw the pages "+
			"(its log ends at %q, and calibre-server read EOFError from it). Qt WebEngine %s, which this Calibre "+
			"carries, does that now and then on a busy machine; 6.10.2 fixed it (QTBUG-142720)", drawing, qtWebEngine)
		st, err = attempt()
	}
	return st, err
}

// fixture returns the file name in testdata.
func fixture(tb testing.TB, name string) string {
	tb.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // G304: a fixture this test names, in its own testdata
	if err != nil {
		tb.Fatal(err)
	}
	return string(b)
}

// TestConvertsAgainOnlyOnTheKnownDrop: the PDF test converts again only on
// Qt WebEngine 6.10.1, and only when the conversion failed as that version
// makes it fail. The fixtures are real: two answers of the Content Server
// for a conversion whose worker ended as Chromium started to draw the
// pages, one with Chromium's log on, and the log of a conversion that
// worked. Each other case changes one thing in them. It needs no image.
func TestConvertsAgainOnlyOnTheKnownDrop(t *testing.T) {
	t.Parallel()
	died := conversion{Log: fixture(t, "died-drawing.log"), Traceback: fixture(t, "died-drawing.traceback")}
	diedLogged := conversion{Log: fixture(t, "died-drawing-chromium-log.log"), Traceback: fixture(t, "died-drawing-chromium-log.traceback")}
	converted := fixture(t, "converted.log")
	// Where the worker's log stood just before it logged drawing
	before, _, found := strings.Cut(died.Log, drawing)
	if !found {
		t.Fatalf("the fixture log holds no %q", drawing)
	}
	// A conversion that failed in Calibre's own code, which the worker
	// reports: calibre-server's traceback is the worker's
	failedInCalibre := "Traceback (most recent call last):\n" +
		"  File \"calibre/utils/ipc/simple_worker.py\", line 372, in main\n" +
		"  File \"calibre/srv/convert.py\", line 154, in convert_book\n" +
		"  File \"calibre/ebooks/conversion/plumber.py\", line 1229, in run\n" +
		"FileNotFoundError: [Errno 2] No such file or directory: '/titlepage.xhtml'\n"
	// The same, had the worker's own code read EOFError: not calibre-server
	// reading from the worker
	eofInCalibre := strings.Replace(failedInCalibre, "FileNotFoundError: [Errno 2] No such file or directory: '/titlepage.xhtml'", "EOFError", 1)
	// calibre-server's end of the connection, broken another way
	reset := strings.Replace(died.Traceback, "\nEOFError", "\nConnectionResetError: [Errno 104] Connection reset by peer", 1)
	// A FATAL line of Chromium's, as this image logged one under rootless
	// Podman with Chromium's namespace sandbox on, and its log on
	fatal := diedLogged.Log + "[84:84:1127/013116.526831:FATAL:zygote_host_impl_linux.cc(224)] Check failed: . : No such file or directory (2)\n"
	if reset == died.Traceback || eofInCalibre == failedInCalibre {
		t.Fatal("a fixture traceback is not as this test expects")
	}
	cases := []struct {
		name string
		qt   string
		st   conversion
		want bool
	}{
		{"6.10.1, the worker ended as the pages started to be drawn", "6.10.1", died, true},
		{"6.10.1, the same, with Chromium's log on", "6.10.1", diedLogged, true},
		{"6.10.1, the same, with a FATAL line of Chromium's after it", "6.10.1", conversion{Log: fatal, Traceback: diedLogged.Traceback}, false},
		{"6.10.2, which fixed it", "6.10.2", died, false},
		{"6.11.0", "6.11.0", died, false},
		{"6.10.0, an older one", "6.10.0", died, false},
		{"no version", "", died, false},
		{"6.10.1, a conversion that worked", "6.10.1", conversion{OK: true, Log: died.Log, Traceback: died.Traceback}, false},
		{"6.10.1, the worker ended after the pages were drawn", "6.10.1", conversion{Log: converted, Traceback: died.Traceback}, false},
		{"6.10.1, the worker ended before the pages started to be drawn", "6.10.1", conversion{Log: before, Traceback: died.Traceback}, false},
		{"6.10.1, a failure in Calibre's own code", "6.10.1", conversion{Log: died.Log, Traceback: failedInCalibre}, false},
		{"6.10.1, EOFError in Calibre's own code", "6.10.1", conversion{Log: died.Log, Traceback: eofInCalibre}, false},
		{"6.10.1, the connection to the worker reset", "6.10.1", conversion{Log: died.Log, Traceback: reset}, false},
		{"6.10.1, no traceback", "6.10.1", conversion{Log: died.Log}, false},
	}
	for _, c := range cases {
		if got := convertsAgain(c.qt, c.st); got != c.want {
			t.Errorf("%s: converts again: %v, want %v", c.name, got, c.want)
		}
	}
}

// TestConvertPDFOnceMore: convertPDF runs its attempt again only when the
// first failed as convertsAgain describes, never a third time, and returns
// the last attempt's answer. It needs no image.
func TestConvertPDFOnceMore(t *testing.T) {
	t.Parallel()
	died := conversion{Log: fixture(t, "died-drawing.log"), Traceback: fixture(t, "died-drawing.traceback")}
	worked := conversion{OK: true, Fmt: "pdf"}
	cases := []struct {
		name    string
		qt      string
		answers []conversion // each attempt's answer, in turn
		runs    int
		want    conversion
	}{
		{"6.10.1, the worker ended, then it worked", "6.10.1", []conversion{died, worked}, 2, worked},
		{"6.10.1, the worker ended twice", "6.10.1", []conversion{died, died, worked}, 2, died},
		{"6.10.1, it worked at once", "6.10.1", []conversion{worked, died}, 1, worked},
		{"6.10.2, the worker ended", "6.10.2", []conversion{died, worked}, 1, died},
	}
	for _, c := range cases {
		runs := 0
		got, err := convertPDF(t, c.qt, func() (conversion, error) {
			st := c.answers[runs]
			runs++
			if !st.OK {
				return st, errors.New("the conversion failed")
			}
			return st, nil
		})
		if runs != c.runs || got != c.want || (err == nil) != c.want.OK {
			t.Errorf("%s: %d attempts, the last answer OK %v (err %v), want %d attempts and OK %v", c.name, runs, got.OK, err, c.runs, c.want.OK)
		}
	}
}
