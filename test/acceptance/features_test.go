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
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/divbast/ci/harness"
)

// imageChromium are the Chromium flags the image's ENV sets: no GPU, no
// namespace sandbox, and shared memory in /tmp, so that Calibre's PDF
// output runs under rootless Podman as under Docker.
const imageChromium = "--disable-gpu --disable-namespace-sandbox --disable-dev-shm-usage"

// chromiumLog is the image's Chromium flags, and Chromium's own log, to
// stderr, which QtWebEngine always logs to: a user's own value replaces
// the image's, so it keeps the image's flags.
const chromiumLog = imageChromium + " --enable-logging=stderr --v=1"

// sandboxed is what Chromium logs when a page-drawing process starts under
// its seccomp-bpf filter.
const sandboxed = "Activated seccomp-bpf sandbox for process type: renderer"

// jobLogCode is Python that reads, every 50 ms until the file sys.argv[1]
// exists, each log file under /tmp and calibre-server's own temporary
// folder, where its worker writes the stdout and stderr of a conversion
// while it runs, and prints the name of the first that holds sys.argv[2].
// calibre-server deletes a job's log when the job works, and logs it only
// when it fails.
const jobLogCode = `import os, sys, time
stop, needle = sys.argv[1], sys.argv[2].encode()
while not os.path.exists(stop):
    for top in sys.argv[3:]:
        for root, dirs, files in os.walk(top):
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
		out, err := inPython(context.WithoutCancel(tb.Context()), c, jobLogCode, stopAt, needle, "/tmp", serverTmp)
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

// envIsCode is Python that prints whether the environment of the process
// sys.argv[1] sets the variable sys.argv[2] to sys.argv[3], and nothing
// of the value.
const envIsCode = `import sys
data = open(f"/proc/{sys.argv[1]}/environ", "rb").read().split(b"\0")
print((sys.argv[2] + "=" + sys.argv[3]).encode() in data)
`

// TestPDFOutputSandboxed converts an EPUB to a PDF in the Content Server,
// which draws the pages in QtWebEngine: as the image runs it, calibre-server
// gets the image's Chromium flags and the conversion works; and with
// Chromium's own log on as well, the conversion's log shows each
// page-drawing process under Chromium's seccomp-bpf filter. On Qt
// WebEngine 6.10.1 alone, a conversion whose worker ended as Chromium
// started to draw the pages is made again, once (see convertsAgain).
func TestPDFOutputSandboxed(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	qt := qtWebEngineVersion(t, e, image)
	t.Logf("Calibre's bundle carries Qt WebEngine %s", qt)
	runs := []struct {
		name  string
		env   map[string]string
		flags string // what calibre-server's QTWEBENGINE_CHROMIUM_FLAGS must be
	}{
		{"as the image runs it", nil, imageChromium},
		{"with Chromium's log on", map[string]string{"QTWEBENGINE_CHROMIUM_FLAGS": chromiumLog}, chromiumLog},
	}
	for _, r := range runs {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			tb := redacting(t)
			ctx := tb.Context()
			b := serveBooks(tb, e, image, titled("PDF Book", 1), r.env)
			checks := harness.NewChecks(tb)
			pid, err := appPID(ctx, b.s.C, contentServer)
			if err != nil {
				tb.Fatal(err)
			}
			out, err := inPython(ctx, b.s.C, envIsCode, strconv.Itoa(pid), "QTWEBENGINE_CHROMIUM_FLAGS", r.flags)
			checks.True("calibre-server gets the Chromium flags "+r.flags, err == nil && strings.TrimSpace(out) == "True", fmt.Sprint(err))
			var st conversion
			logged := jobLogHolds(tb, b.s.C, sandboxed, func() { st, err = convertPDF(ctx, tb, b.srv, qt) })
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
//   - a comic's metadata is read from the ComicInfo.xml of a CBR, a RAR
//     archive, which the image's libmagic names application/vnd.rar, and
//     the image's Calibre-Web takes;
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

	comic := cbr(comicFiles())
	id, err = b.web.upload(ctx, "comic.cbr", bytes.NewReader(comic), int64(len(comic)))
	if err != nil {
		tb.Fatal(err)
	}
	bookShows(tb, b.web, "a comic book, a RAR archive", id, comicTitle, comicSeries, comicWriter)

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

// serverTmp is calibre-server's own folder of temporary files.
const serverTmp = "/config/calibre/tmp"

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

// sizesCode is Python that prints, as JSON, the size of each file under
// the folder sys.argv[1] whose name ends with sys.argv[2].
const sizesCode = `import json, os, sys
out = []
for root, dirs, files in os.walk(sys.argv[1]):
    out += [os.path.getsize(os.path.join(root, f)) for f in files if f.endswith(sys.argv[2])]
print(json.dumps(out))
`

// bookSizes returns the sizes of the files in c's /books whose name ends
// with suffix.
func bookSizes(ctx context.Context, c *harness.Container, suffix string) ([]int64, error) {
	return fileSizes(ctx, c, booksDir, suffix)
}

// fileSizes returns the sizes of the files under top in c whose name ends
// with suffix.
func fileSizes(ctx context.Context, c *harness.Container, top, suffix string) ([]int64, error) {
	out, err := inPython(ctx, c, sizesCode, top, suffix)
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

// tmpBound is the most /tmp may grow by while calibre-server takes an
// upload: it keeps the copies of the file in its own folder, in /config.
const tmpBound = 16 << 20

// TestLargeUpload uploads a 300 MiB PDF to each port: each app takes it,
// whole, into the library, and PID 1 streams it, never holding a third of
// it in its memory. On 8080, /tmp grows by under 16 MiB, calibre-server
// keeps its copies of the file in its own folder in /config, a second
// upload right after works, and once calibre-server has crashed and
// started again the folder holds no file. Past calibre-server's limit, 8080 answers 413 with
// calibre-server's own words, every time. It logs how long each upload
// took, and the most /tmp held meanwhile.
func TestLargeUpload(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	t.Run("8080, the Content Server", func(t *testing.T) {
		t.Parallel()
		tb := redacting(t)
		ctx := tb.Context()
		b := serveBooks(tb, e, image, titled("Large Book", 1), nil)
		pid, err := appPID(ctx, b.s.C, contentServer)
		if err != nil {
			tb.Fatal(err)
		}
		checks := harness.NewChecks(tb)
		var size int64
		for _, n := range []string{"first", "second"} {
			var body io.Reader
			body, size = pdfPage(612, 792, pageRed, largeSize)
			smp := startSampler(tb, b.s.C)
			start := time.Now()
			id, aerr := b.srv.addBookFrom(ctx, n+".pdf", body, size)
			took := time.Since(start)
			m := smp.stop(tb)
			checks.True("8080 takes the "+n+" upload", aerr == nil && id > 0, fmt.Sprintf("book %d: %v", id, aerr))
			checks.True(fmt.Sprintf("the %s upload grows /tmp by under %s", n, mib(tmpBound)), m.TmpPeak-m.TmpBase < tmpBound, mib(m.TmpPeak-m.TmpBase))
			copies, cerr := fileSizes(ctx, b.s.C, serverTmp, ".pdf")
			checks.True("calibre-server keeps its copies of the "+n+" upload in "+serverTmp, cerr == nil && slices.Contains(copies, size), fmt.Sprintf("%v: %v", copies, cerr))
			tb.Logf("the %s upload, %d bytes, in %s; /tmp held at most %s, from %s; %s holds %d copies",
				n, size, took.Round(time.Millisecond), mib(m.TmpPeak), mib(m.TmpBase), serverTmp, len(copies))
		}
		sizes, err := bookSizes(ctx, b.s.C, ".pdf")
		checks.True("the library holds both files whole", err == nil && slices.Equal(sizes, []int64{size, size}), fmt.Sprintf("%v, want two of %d: %v", sizes, size, err))
		peak, err := pid1Peak(ctx, b.s.C)
		checks.True(fmt.Sprintf("PID 1 never holds a third of an upload: under %d KiB", pid1Bound), err == nil && peak < pid1Bound, fmt.Sprintf("%d KiB: %v", peak, err))
		// A crash, after which calibre-server cannot delete its own files,
		// as it does when it stops: PID 1 empties the folder before the
		// next start. The new calibre-server makes folders of its own there
		kill(tb, b.s.C, pid)
		if _, err = waitNewPID(ctx, b.s.C, contentServer, pid); err != nil {
			tb.Fatal(err)
		}
		if err = waitHealthy(ctx, b.s.C); err != nil {
			tb.Fatal(err)
		}
		left, err := fileSizes(ctx, b.s.C, serverTmp, "")
		checks.True("after calibre-server crashed and started again, "+serverTmp+" holds no file", err == nil && len(left) == 0, fmt.Sprintf("%d files: %v", len(left), err))
	})
	t.Run("8083, Calibre-Web", func(t *testing.T) {
		t.Parallel()
		tb := redacting(t)
		ctx := tb.Context()
		b := serveBooks(tb, e, image, titled("Large Book", 1), nil)
		body, size := pdfPage(612, 792, pageRed, largeSize)
		smp := startSampler(tb, b.s.C)
		start := time.Now()
		id, err := b.web.upload(ctx, "large.pdf", body, size)
		took := time.Since(start)
		m := smp.stop(tb)
		checks := harness.NewChecks(tb)
		checks.True("8083 takes the upload", err == nil && id > 0, fmt.Sprintf("book %d: %v", id, err))
		sizes, err := bookSizes(ctx, b.s.C, ".pdf")
		checks.True("the library holds the whole file", err == nil && len(sizes) == 1 && sizes[0] == size, fmt.Sprintf("%v, want [%d]: %v", sizes, size, err))
		peak, err := pid1Peak(ctx, b.s.C)
		checks.True(fmt.Sprintf("PID 1 never holds a third of the upload: under %d KiB", pid1Bound), err == nil && peak < pid1Bound, fmt.Sprintf("%d KiB: %v", peak, err))
		tb.Logf("%d bytes uploaded in %s; /tmp held at most %s, from %s; PID 1 held at most %d KiB",
			size, took.Round(time.Millisecond), mib(m.TmpPeak), mib(m.TmpBase), peak)
	})
	t.Run("8080, past calibre-server's limit", func(t *testing.T) {
		t.Parallel()
		tb := redacting(t)
		ctx := tb.Context()
		b := serveBooks(tb, e, image, titled("Limit Book", 1), nil)
		const tries = 10
		got := make([]string, 0, tries)
		for range tries {
			_, err := b.srv.addBookFrom(ctx, "limit.pdf", io.LimitReader(zeros{}, serverBodyLimit+1), serverBodyLimit+1)
			got = append(got, fmt.Sprint(err))
		}
		want := fmt.Sprintf("the Content Server answered the add 413: %q", "The entity sent with the request exceeds the maximum allowed bytes (524288000).")
		bad := slices.DeleteFunc(slices.Clone(got), func(s string) bool { return s == want })
		harness.Equal(harness.NewChecks(tb), fmt.Sprintf("each of %d uploads of %d bytes gets 413 and calibre-server's words", tries, serverBodyLimit+1), "none", none(bad))
	})
}
