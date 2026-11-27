// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/divbast/ci/harness"
)

// chromiumLog has Chromium, inside Calibre's QtWebEngine, log what it does
// to stderr, which QtWebEngine always logs to. Qt turns the GPU off itself
// only while QTWEBENGINE_CHROMIUM_FLAGS is unset: with the variable set,
// and no --disable-gpu, Chromium aborts, finding no GL display.
const chromiumLog = "--disable-gpu --enable-logging=stderr --v=1"

// sandboxed is what Chromium logs when a page-drawing process starts under
// its seccomp-bpf filter.
const sandboxed = "Activated seccomp-bpf sandbox for process type: renderer"

// jobLogCode is Python that reads, every 50 ms until the file sys.argv[1]
// exists, each log file under /tmp, where calibre-server's worker writes
// the stdout and stderr of a conversion while it runs, and prints the name
// of the first that holds sys.argv[2]. calibre-server deletes a job's log
// when the job works, and logs it only when it fails.
const jobLogCode = `import os, sys, time
stop, needle = sys.argv[1], sys.argv[2].encode()
while not os.path.exists(stop):
    for root, dirs, files in os.walk("/tmp"):
        for f in files:
            if f.endswith(".log"):
                try:
                    with open(os.path.join(root, f), "rb") as fh:
                        if needle in fh.read(16 << 20):
                            print(f)
                            sys.exit(0)
                except OSError:
                    pass
    time.sleep(0.05)
`

// jobLogHolds runs do, and reports whether a log calibre-server's worker
// wrote meanwhile held needle.
func jobLogHolds(tb testing.TB, c *harness.Container, needle string, do func()) bool {
	tb.Helper()
	stopAt := "/tmp/divbast-joblog-" + random(tb, 8, lower)
	found := make(chan string, 1)
	go func() {
		out, err := inPython(context.WithoutCancel(tb.Context()), c, jobLogCode, stopAt, needle)
		if err != nil {
			tb.Errorf("reading calibre-server's job logs: %v", err)
		}
		found <- strings.TrimSpace(out)
	}()
	do()
	if _, err := inPython(tb.Context(), c, "import sys; open(sys.argv[1], 'w').close()", stopAt); err != nil {
		tb.Fatal(err)
	}
	name := <-found
	tb.Logf("the job log that holds it: %s", cmp.Or(name, "none"))
	return name != ""
}

// TestPDFOutputSandboxed converts an EPUB to a PDF in the Content Server,
// which draws the pages in QtWebEngine: the conversion works, and, with
// Chromium's own log on, the conversion's log shows each page-drawing
// process under Chromium's seccomp-bpf filter.
func TestPDFOutputSandboxed(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	runs := []struct {
		name string
		env  map[string]string
	}{
		{"as the image runs it", nil},
		{"with Chromium's log on", map[string]string{"QTWEBENGINE_CHROMIUM_FLAGS": chromiumLog}},
	}
	for _, r := range runs {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			tb := redacting(t)
			ctx := tb.Context()
			b := serveBooks(tb, e, image, titled("PDF Book", 1), r.env)
			var st conversion
			var err error
			logged := jobLogHolds(tb, b.s.C, sandboxed, func() { st, err = b.srv.convert(ctx, 1, "EPUB", "pdf", convertWithin) })
			checks := harness.NewChecks(tb)
			checks.True("the Content Server converts the EPUB to a PDF", err == nil && st.Fmt == "pdf", fmt.Sprintf("%q: %v", st.Fmt, err))
			checks.True("the book has the PDF", serverHasFormat(ctx, b.srv, 1, "PDF") == nil, "")
			if r.env != nil {
				checks.True("the conversion's log shows a page-drawing process under the seccomp-bpf filter", logged, sandboxed)
			}
		})
	}
}

// epubOPFTitle returns the title in the package document of epub, an EPUB
// or a KEPUB: what a reader shows, as written in the file.
func epubOPFTitle(epub []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(epub), int64(len(epub)))
	if err != nil {
		return "", fmt.Errorf("the download, %d bytes, is no EPUB: %w", len(epub), err)
	}
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".opf") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		b, err := io.ReadAll(rc)
		if cerr := rc.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return "", err
		}
		if m := opfTitle.FindSubmatch(b); m != nil {
			return string(m[1]), nil
		}
	}
	return "", errors.New("the EPUB has no title in a package document")
}

// opfTitle is the title in an EPUB's package document.
var opfTitle = regexp.MustCompile(`<dc:title[^>]*>([^<]*)<`)

// fromZip returns the files in the ZIP archive data whose names end with
// suffix, one after another.
func fromZip(data []byte, suffix string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	var all []byte
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, suffix) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(rc)
		if cerr := rc.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
		all = append(all, b...)
	}
	return all, nil
}

// download returns book id in format, as Calibre-Web gives it to a reader.
func (w *webSession) download(ctx context.Context, id int, format string) ([]byte, error) {
	resp, err := w.get(ctx, fmt.Sprintf("/download/%d/%s/book", id, strings.ToLower(format)), nil)
	err = answered("downloading from Calibre-Web", resp, err)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// reloaded waits until PID 1 has restarted calibre-server, from old, to
// read what Calibre-Web just changed, and the image is healthy again:
// Calibre-Web reads a book's files through the Content Server, and one
// that has not reloaded does not know of the change yet.
func reloaded(tb testing.TB, s *harness.Serving, old int) int {
	tb.Helper()
	ctx := tb.Context()
	pid, err := waitNewPID(ctx, s.C, contentServer, old)
	if err == nil {
		err = waitHealthy(ctx, s.C)
	}
	if err == nil {
		err = gateOpen(ctx, s)
	}
	if err != nil {
		tb.Fatalf("calibre-server is not restarted, and healthy, after a change in Calibre-Web: %v", err)
	}
	return pid
}

// gateOpen waits until port 8080 no longer refuses every request, as it
// does while calibre-server restarts: PID 1 learns that the new process
// takes connections at its next look at the port, each second, which can
// be after the probe passes.
func gateOpen(ctx context.Context, s *harness.Serving) error {
	return within(ctx, startWithin, func(ctx context.Context) error {
		resp, err := s.Get(ctx, "/ajax/library-info", nil)
		if err == nil && resp.Status == http.StatusServiceUnavailable {
			err = fmt.Errorf("port 8080 still refuses every request: %s", harness.Clip(string(resp.Body)))
		}
		return err
	})
}

// convertIn has Calibre-Web convert book 1 from EPUB to format, and
// checks that it offers the new format within convertWithin. It returns
// calibre-server's PID once PID 1 has restarted it for the new format.
func convertIn(tb testing.TB, b *both, format string, pid int) int {
	tb.Helper()
	ctx := tb.Context()
	if err := b.web.convert(ctx, 1, "EPUB", format); err != nil {
		tb.Fatal(err)
	}
	err := within(ctx, convertWithin, func(ctx context.Context) error { return webHasFormat(ctx, b.web, 1, format) })
	harness.NewChecks(tb).True("Calibre-Web converts the EPUB to "+format, err == nil, fmt.Sprint(err))
	return reloaded(tb, b.s, pid)
}

// bookShows checks that Calibre-Web's page of book id holds each of
// needles.
func bookShows(tb testing.TB, w *webSession, what string, id int, needles ...string) {
	tb.Helper()
	page, err := w.bookPage(tb.Context(), id)
	harness.Answers(tb, what+": its page", http.StatusOK, page, err)
	for _, n := range needles {
		harness.Contains(tb, what+": its page shows "+n, n, string(page.Body))
	}
}

// errorLine is a line Calibre-Web logs at its level ERROR, or a Python
// traceback.
var errorLine = regexp.MustCompile(`(?m)^.*(\] ERROR \{|Traceback \(most recent call last\)).*$`)

// TestCalibreWebFeatures uses what Calibre-Web does with Calibre's tools
// in the image:
//   - a download has its metadata written in: the title in the file is
//     the one edited a second earlier, in the Content Server;
//   - an EPUB converts to MOBI, with Calibre's ebook-convert, and to
//     KEPUB, with kepubify;
//   - an uploaded PDF's cover is its first page, through ImageMagick and
//     Ghostscript, and not Calibre-Web's generic cover;
//   - a comic's metadata is read from its ComicInfo.xml: a CBZ named .cbr,
//     since Calibre-Web refuses the upload of a RAR archive, which the
//     image's libmagic names application/vnd.rar;
//   - the admin page has no update controls, and the database settings
//     page no Google Drive, which the image does not install, with no
//     error logged for either.
func TestCalibreWebFeatures(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	tb := redacting(t)
	ctx := tb.Context()
	f := fixtureBooks(tb, e, image)
	b := serveBooks(tb, e, image, titled("Feature Book", 2), nil)
	checks := harness.NewChecks(tb)
	pid, err := appPID(ctx, b.s.C, contentServer)
	if err != nil {
		tb.Fatal(err)
	}

	title := "Embedded Title " + random(tb, 8, lower)
	resp, err := b.srv.setTitle(ctx, 2, title)
	if err = answered("editing a title in the Content Server", resp, err); err != nil {
		tb.Fatal(err)
	}
	if err = harness.Pause(ctx, time.Second); err != nil {
		tb.Fatal(err)
	}
	epub, err := b.web.download(ctx, 2, "EPUB")
	got, terr := epubOPFTitle(epub)
	checks.True("a download from Calibre-Web has the title edited a second earlier written in", err == nil && terr == nil && got == title,
		fmt.Sprintf("%q, want %q: %v, %v", got, title, err, terr))

	pid = convertIn(tb, b, "MOBI", pid)
	mobi, err := b.web.download(ctx, 1, "MOBI")
	checks.True("the MOBI is a MOBI", err == nil && len(mobi) > 68 && string(mobi[60:68]) == "BOOKMOBI", fmt.Sprintf("%d bytes: %v", len(mobi), err))
	convertIn(tb, b, "KEPUB", pid)
	kepub, err := b.web.download(ctx, 1, "KEPUB")
	spans, zerr := fromZip(kepub, "html")
	checks.True("the KEPUB has kepubify's spans", err == nil && zerr == nil && bytes.Contains(spans, []byte("koboSpan")), fmt.Sprintf("%d bytes: %v, %v", len(kepub), err, zerr))

	id, err := b.web.upload(ctx, "page.pdf", bytes.NewReader(f.pdf), int64(len(f.pdf)))
	if err != nil {
		tb.Fatal(err)
	}
	cover, err := b.web.cover(ctx, id)
	checks.True("an uploaded PDF's cover is its red first page", isColour(cover, err, pageRed) == nil, fmt.Sprint(isColour(cover, err, pageRed)))
	generic, err := b.web.get(ctx, "/static/generic_cover.jpg", nil)
	checks.True("and not the generic cover", err == nil && generic.Status == http.StatusOK && !bytes.Equal(cover, generic.Body), fmt.Sprint(err))

	comic := cbz(comicFiles())
	id, err = b.web.upload(ctx, "comic.cbr", bytes.NewReader(comic), int64(len(comic)))
	if err != nil {
		tb.Fatal(err)
	}
	bookShows(tb, b.web, "a comic book, .cbr", id, comicTitle, comicSeries, comicWriter)

	before, err := b.s.C.Logs(ctx)
	if err != nil {
		tb.Fatal(err)
	}
	view, err := b.web.get(ctx, "/admin/view", nil)
	harness.Answers(tb, "the admin page", http.StatusOK, view, err)
	harness.Contains(tb, "the admin page has its own Restart", `id="admin_restart"`, string(view.Body))
	for _, c := range []string{`id="check_for_update"`, `id="perform_update"`, "Check for Update"} {
		harness.Lacks(tb, "the admin page has no update control: "+c, c, string(view.Body))
	}
	dbconfig, err := b.web.get(ctx, "/admin/dbconfig", nil)
	harness.Answers(tb, "the database settings page", http.StatusOK, dbconfig, err)
	harness.Contains(tb, "the database settings page names the library", `id="config_calibre_dir"`, string(dbconfig.Body))
	for _, c := range []string{"config_use_google_drive", "Google Drive"} {
		harness.Lacks(tb, "the database settings page offers no Google Drive: "+c, c, string(dbconfig.Body))
	}
	after, err := b.s.C.Logs(ctx)
	if err != nil {
		tb.Fatal(err)
	}
	harness.Equal(checks, "the two pages log no error", "none", none(errorLine.FindAllString(strings.TrimPrefix(after, before), -1)))
	var drive []string
	for _, l := range errorLine.FindAllString(after, -1) {
		if strings.Contains(strings.ToLower(l), "drive") {
			drive = append(drive, l)
		}
	}
	harness.Equal(checks, "Calibre-Web logs no error about Google Drive", "none", none(drive))
}

// largeSize is the size of the large upload, 300 MiB: a large book, as
// an upload to either port must work for one.
const largeSize = 300 << 20

// pid1PeakCode is Python that prints the most memory PID 1 has held, in
// KiB: VmHWM, its peak resident set.
const pid1PeakCode = `import re
print(re.search(r"^VmHWM:\s+(\d+) kB$", open("/proc/1/status").read(), re.M).group(1))
`

// pid1Peak returns the most memory PID 1 has held in c, in KiB.
func pid1Peak(ctx context.Context, c *harness.Container) (int, error) {
	out, err := inPython(ctx, c, pid1PeakCode)
	if err != nil {
		return 0, err
	}
	var kib int
	_, err = fmt.Sscan(out, &kib)
	return kib, err
}

// bookSizesCode is Python that prints, as JSON, the size of each file in
// /books whose name ends with sys.argv[1].
const bookSizesCode = `import json, os, sys
out = []
for root, dirs, files in os.walk("/books"):
    out += [os.path.getsize(os.path.join(root, f)) for f in files if f.endswith(sys.argv[1])]
print(json.dumps(out))
`

// bookSizes returns the sizes of the files in c's /books whose name ends
// with suffix.
func bookSizes(ctx context.Context, c *harness.Container, suffix string) ([]int64, error) {
	out, err := inPython(ctx, c, bookSizesCode, suffix)
	if err != nil {
		return nil, err
	}
	var sizes []int64
	err = json.Unmarshal([]byte(out), &sizes)
	return sizes, err
}

// pid1Bound is the most memory, in KiB, PID 1 may hold through a large
// upload: a third of the upload, so a proxy that held the body would
// fail it.
const pid1Bound = largeSize / 3 / 1024

// addBookFrom adds a book file called name, of size bytes read from r,
// through the Content Server, streamed, and returns the new book's id.
func (c *server) addBookFrom(ctx context.Context, name string, r io.Reader, size int64) (int, error) {
	path := "/cdb/add-book/1/y/" + name + "/" + libraryID
	h, refused, err := c.authorize(ctx, http.MethodPost, path, nil)
	if err != nil {
		return 0, fmt.Errorf("%w: %d", err, refused.Status)
	}
	return addedID(send(ctx, bigClient(), c.s.Base, http.MethodPost, path, h, r, size))
}

// TestLargeUpload uploads a 300 MiB PDF to each port: each app takes it,
// whole, into the library, and PID 1 streams it, never holding a third of
// it in its memory. It logs how long the upload took, and the most /tmp
// held meanwhile, as the image sizes /tmp for it.
func TestLargeUpload(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	ports := []struct {
		name   string
		upload func(ctx context.Context, b *both, r io.Reader, size int64) (int, error)
	}{
		{"8080, the Content Server", func(ctx context.Context, b *both, r io.Reader, size int64) (int, error) {
			return b.srv.addBookFrom(ctx, "large.pdf", r, size)
		}},
		{"8083, Calibre-Web", func(ctx context.Context, b *both, r io.Reader, size int64) (int, error) {
			return b.web.upload(ctx, "large.pdf", r, size)
		}},
	}
	for _, p := range ports {
		t.Run(p.name, func(t *testing.T) {
			t.Parallel()
			tb := redacting(t)
			ctx := tb.Context()
			b := serveBooks(tb, e, image, titled("Large Book", 1), nil)
			body, size := pdfPage(612, 792, pageRed, largeSize)
			smp := startSampler(tb, b.s.C)
			start := time.Now()
			id, err := p.upload(ctx, b, body, size)
			took := time.Since(start)
			m := smp.stop(tb)
			checks := harness.NewChecks(tb)
			checks.True("the app takes the upload", err == nil && id > 0, fmt.Sprintf("book %d: %v", id, err))
			sizes, err := bookSizes(ctx, b.s.C, ".pdf")
			checks.True("the library holds the whole file", err == nil && len(sizes) == 1 && sizes[0] == size, fmt.Sprintf("%v, want [%d]: %v", sizes, size, err))
			peak, err := pid1Peak(ctx, b.s.C)
			checks.True(fmt.Sprintf("PID 1 never holds a third of the upload: under %d KiB", pid1Bound), err == nil && peak < pid1Bound, fmt.Sprintf("%d KiB: %v", peak, err))
			tb.Logf("%d bytes uploaded in %s; /tmp held at most %s, from %s; PID 1 held at most %d KiB",
				size, took.Round(time.Millisecond), mib(m.TmpPeak), mib(m.TmpBase), peak)
		})
	}
}
