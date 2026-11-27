// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/divbast/ci/harness"
)

// samplerCode is Python that samples the container every sys.argv[2]
// seconds, until the file sys.argv[1] exists, having made the file
// sys.argv[1].ready once it has its first sample: how much of /tmp is used;
// calibre-server's PID; whether 127.0.0.1:18080 takes a connection;
// whether the shipped probe passes, when sys.argv[3] is "1"; and whether
// metadata.db, its journal or its write-ahead log changed. It prints, as
// JSON, /tmp's use at the start and at most, and each sample that differs
// from the one before, with its time in seconds since the start.
const samplerCode = `import json, os, socket, subprocess, sys, time
stop, every, health = sys.argv[1], float(sys.argv[2]), sys.argv[3] == "1"
def used():
    st = os.statvfs("/tmp")
    return (st.f_blocks - st.f_bfree) * st.f_frsize
def server():
    for d in os.listdir("/proc"):
        if d.isdigit():
            try:
                argv0 = open(f"/proc/{d}/cmdline", "rb").read().split(b"\0")[0]
            except OSError:
                continue
            if os.path.basename(argv0) == b"calibre-server":
                return int(d)
    return 0
def accepts():
    s = socket.socket()
    s.settimeout(0.5)
    try:
        s.connect(("127.0.0.1", 18080))
        return True
    except OSError:
        return False
    finally:
        s.close()
def stamp():
    out = []
    for suffix in ("", "-journal", "-wal"):
        try:
            st = os.stat("/books/metadata.db" + suffix)
            out.append((st.st_size, st.st_mtime_ns))
        except FileNotFoundError:
            out.append(None)
    return out
base = peak = used()
samples, last, db = [], None, stamp()
t0 = time.monotonic()
open(stop + ".ready", "w").close()
while not os.path.exists(stop):
    peak = max(peak, used())
    now = stamp()
    s = {"pid": server(), "accepts": accepts(), "db": now != db}
    db = now
    if health:
        s["healthy"] = subprocess.run(["` + binary + `", "health"], capture_output=True).returncode == 0
    if s != last or s["db"]:
        samples.append(dict(s, at=round(time.monotonic() - t0, 3)))
    last = s
    time.sleep(every)
os.remove(stop)
os.remove(stop + ".ready")
print(json.dumps({"tmp_base": base, "tmp_peak": max(peak, used()), "health": health, "samples": samples}))
`

// sample is one sample samplerCode took that differs from the one before.
type sample struct {
	At      float64 `json:"at"` // seconds since the sampler started
	PID     int     `json:"pid"`
	Accepts bool    `json:"accepts"`
	Healthy bool    `json:"healthy"`
	DB      bool    `json:"db"` // metadata.db changed since the sample before
}

// sampled is what samplerCode saw, and whether it ran the probe.
type sampled struct {
	TmpBase int64    `json:"tmp_base"`
	TmpPeak int64    `json:"tmp_peak"`
	Health  bool     `json:"health"`
	Samples []sample `json:"samples"`
}

// sampler is samplerCode running in a container.
type sampler struct {
	c      *harness.Container
	stopAt string
	done   chan error
	out    string
}

// samplerEvery is how often a sampler samples.
const samplerEvery = "0.1"

// startSampler starts samplerCode in c, which also runs the shipped probe
// at each sample when health is set.
func startSampler(tb testing.TB, c *harness.Container, health ...bool) *sampler {
	tb.Helper()
	probe := "0"
	if len(health) > 0 && health[0] {
		probe = "1"
	}
	s := &sampler{c: c, stopAt: "/tmp/divbast-sampler-" + random(tb, 8, lower), done: make(chan error, 1)}
	ctx := context.WithoutCancel(tb.Context())
	go func() {
		out, err := inPython(ctx, c, samplerCode, s.stopAt, samplerEvery, probe)
		s.out = out
		s.done <- err
	}()
	// What the test does next must come after the sampler's first look
	err := harness.Poll(tb.Context(), 30*time.Second, 50*time.Millisecond, func(ctx context.Context) error {
		ready, err := isFile(ctx, c, s.stopAt+".ready")
		if err == nil && !ready {
			err = errors.New("the sampler has not started")
		}
		return harness.NotYet(err)
	})
	if err != nil {
		tb.Fatal(err)
	}
	return s
}

// stop stops the sampler and returns what it saw.
func (s *sampler) stop(tb testing.TB) sampled {
	tb.Helper()
	if _, err := inPython(tb.Context(), s.c, "import sys; open(sys.argv[1], 'w').close()", s.stopAt); err != nil {
		tb.Fatalf("stopping the sampler: %v", err)
	}
	var m sampled
	if err := <-s.done; err != nil {
		tb.Fatalf("the sampler: %v", err)
	}
	if err := json.Unmarshal([]byte(s.out), &m); err != nil {
		tb.Fatalf("reading the sampler's samples: %v: %s", err, harness.Clip(s.out))
	}
	return m
}

// mib is n bytes in MiB.
func mib(n int64) string {
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}

// restart is one restart of calibre-server a sampler saw: the old PID and
// the new, and when, in seconds since the sampler started, the old process
// stopped taking connections, the new one took one, and the probe passed.
type restart struct {
	old, pid          int
	down, up, healthy float64
	sawUp, sawHealthy bool
}

// restarts returns each restart of calibre-server in m: from the first
// sample in which the old process no longer takes a connection, or is
// gone, to the first in which the new one takes one, and, when the
// sampler ran the probe, the first after it in which the probe passes.
func restarts(m sampled) []restart {
	if len(m.Samples) == 0 {
		return nil
	}
	var out []restart
	pid := m.Samples[0].PID
	open := false // a restart is under way
	for i, s := range m.Samples[1:] {
		prev := m.Samples[i]
		if !open && prev.PID == pid && prev.Accepts && (!s.Accepts || s.PID != pid) {
			out = append(out, restart{old: pid, down: s.At})
			open = true
		}
		if !open {
			continue
		}
		r := &out[len(out)-1]
		if !r.sawUp && s.PID != 0 && s.PID != pid && s.Accepts {
			r.pid, r.up, r.sawUp = s.PID, s.At, true
		}
		if r.sawUp && s.PID == r.pid && (!m.Health || s.Healthy) {
			r.healthy, r.sawHealthy = s.At, m.Health
			pid, open = r.pid, false
		}
	}
	return out
}

// measureBooks is the size of TestMeasureReload's library.
const measureBooks = 5000

// TestMeasureReload restarts calibre-server over a library of 5,000
// books, made by the image's own Calibre, after an edit in Calibre-Web.
// It records how long calibre-server is down, from the moment its port no
// longer takes a connection to the moment the new one does; how long the
// image takes to be healthy again; how long the first read through 8080
// then takes, which opens the library; and the most /tmp held. The
// restart must be healthy again within the two minutes a planned restart
// may take.
func TestMeasureReload(t *testing.T) { //nolint:paralleltest // measured alone, so that no other test's containers change its numbers
	e, image := underTest(t)
	tb := redacting(t)
	ctx := tb.Context()
	start := time.Now()
	books := make([]shelf, measureBooks)
	for i := range books {
		books[i] = shelf{Title: fmt.Sprintf("Measured Book %04d", i+1), Author: fmt.Sprintf("Author %03d", i%250)}
	}
	sec := newSecrets(tb)
	lib := bookLibrary(tb, e, image, books)
	tb.Logf("made a library of %d books in %s", measureBooks, time.Since(start).Round(time.Second))
	start = time.Now()
	l := &launch{env: sec.env(), mounts: []harness.Mount{lib, volume(tb, e, configDir), sec.mount()}, passwords: sec.all()}
	s := l.serve(tb, e, image)
	b := &both{s: s, srv: &server{s: s, pw: sec.admin}, web: loginAdmin(tb, s, sec.web)}
	tb.Logf("first start on it: healthy in %s", time.Since(start).Round(time.Millisecond))
	start = time.Now()
	if err := serverShows(ctx, b.srv, measureBooks, books[measureBooks-1].Title); err != nil {
		tb.Fatal(err)
	}
	tb.Logf("the first read through 8080, which opens the library: %s", time.Since(start).Round(time.Millisecond))
	pid, err := appPID(ctx, s.C, contentServer)
	if err != nil {
		tb.Fatal(err)
	}

	smp := startSampler(tb, s.C, true)
	edited := time.Now()
	editTitle(tb, b.web, 1, "Measured Edit")
	if _, err := waitNewPID(ctx, s.C, contentServer, pid); err != nil {
		tb.Fatal(err)
	}
	if err := within(ctx, plannedLimit, func(ctx context.Context) error { return serverShows(ctx, b.srv, 1, "Measured Edit") }); err != nil {
		tb.Fatal(err)
	}
	tb.Logf("from the edit to the restarted calibre-server showing it, through 8080: %s", time.Since(edited).Round(time.Millisecond))
	if err := harness.Pause(ctx, 5*time.Second); err != nil {
		tb.Fatal(err)
	}
	m := smp.stop(tb)
	rs := restarts(m)
	checks := harness.NewChecks(tb)
	checks.True("one restart of calibre-server", len(rs) == 1, fmt.Sprintf("%+v", rs))
	for _, r := range rs {
		tb.Logf("restart, PID %d to %d: down %.2f s, from its port closing to the new one's taking a connection; healthy %.2f s after it closed",
			r.old, r.pid, r.up-r.down, r.healthy-r.down)
		checks.True("healthy again within the 2 minutes a planned restart may take", r.sawHealthy && r.healthy-r.down < plannedLimit.Seconds(), fmt.Sprintf("%+v", r))
	}
	tb.Logf("/tmp held at most %s, from %s", mib(m.TmpPeak), mib(m.TmpBase))
}

// plannedLimit is how long a planned restart of calibre-server may take
// and the image still be healthy.
const plannedLimit = 2 * time.Minute

// koboBooks is the size of TestMeasureKoboSync's library.
const koboBooks = 20

// kobo turns Calibre-Web's Kobo sync on, through its basic configuration
// page, as an admin does, and waits for Calibre-Web, which restarts itself
// for it, to be healthy again.
func kobo(tb testing.TB, b *both) {
	tb.Helper()
	ctx := tb.Context()
	resp, err := b.web.get(ctx, "/admin/config", nil)
	if err != nil || resp.Status != http.StatusOK {
		tb.Fatalf("Calibre-Web's configuration page: %v, status %d", err, resp.Status)
	}
	form, err := formValues(resp.Body, "config_port")
	if err != nil {
		tb.Fatal(err)
	}
	form.Set("config_kobo_sync", "on")
	if resp, err = b.web.post(ctx, "/admin/ajaxconfig", form, nil); err != nil || resp.Status != http.StatusOK {
		tb.Fatalf("saving Calibre-Web's configuration: %v, status %d", err, resp.Status)
	}
	if err := harness.Pause(ctx, 3*time.Second); err != nil {
		tb.Fatal(err)
	}
	if err := waitHealthy(ctx, b.s.C); err != nil {
		tb.Fatal(err)
	}
}

// koboToken is a Kobo's token in Calibre-Web's Kobo set-up page: in the
// sync address, or, for a page asked for at 127.0.0.1, alone.
var koboToken = regexp.MustCompile(`(?:/kobo/|Kobo Token:\s*)([0-9a-f]{32})`)

// poll503 sends GET /ajax/library-info to 8080 every 250 ms until ctx
// ends, and counts the answers and the 503s among them.
func poll503(ctx context.Context, s *harness.Serving) (answers, refused chan int) {
	answers, refused = make(chan int, 1), make(chan int, 1)
	go func() {
		n, r := 0, 0
		for ctx.Err() == nil {
			resp, err := s.Get(ctx, "/ajax/library-info", nil)
			if err == nil {
				n++
				if resp.Status == http.StatusServiceUnavailable {
					r++
				}
			}
			_ = harness.Pause(ctx, 250*time.Millisecond)
		}
		answers <- n
		refused <- r
	}()
	return answers, refused
}

// kepubs returns how many books the Content Server has in KEPUB, of the
// first n.
func kepubs(ctx context.Context, c *server, n int) int {
	got := 0
	for id := 1; id <= n; id++ {
		if serverHasFormat(ctx, c, id, "KEPUB") == nil {
			got++
		}
	}
	return got
}

// TestMeasureKoboSync is a Kobo's first sync with Calibre-Web, over a
// library of 20 EPUBs: Kobo sync on, the Kobo's set-up page, then the
// sync itself, both of which have Calibre-Web convert every EPUB to
// KEPUB. It records how many conversions worked, how often calibre-server
// was restarted, and for how long, how many reads through 8080 got 503
// meanwhile, and how long until the Content Server had every KEPUB.
func TestMeasureKoboSync(t *testing.T) { //nolint:paralleltest // measured alone, so that no other test's containers change its numbers
	e, image := underTest(t)
	tb := redacting(t)
	ctx := tb.Context()
	b := serveBooks(tb, e, image, titled("Kobo Book", koboBooks), nil)
	kobo(tb, b)

	smp := startSampler(tb, b.s.C)
	pollCtx, stopPoll := context.WithCancel(ctx)
	answers, refused := poll503(pollCtx, b.s)
	start := time.Now()
	setup, err := b.web.get(ctx, "/kobo_auth/generate_auth_token/1", nil)
	m := koboToken.FindSubmatch(setup.Body)
	if err != nil || setup.Status != http.StatusOK || m == nil {
		stopPoll()
		tb.Fatalf("the Kobo set-up page: %v, status %d, no sync address in it", err, setup.Status)
	}
	synced, err := b.web.get(ctx, "/kobo/"+string(m[1])+"/v1/library/sync", nil)
	tb.Logf("the Kobo's sync: %d, %v, %d bytes, %s after the set-up page", synced.Status, err, len(synced.Body), time.Since(start).Round(time.Millisecond))
	err = within(ctx, 10*time.Minute, func(ctx context.Context) error {
		if n := kepubs(ctx, b.srv, koboBooks); n < koboBooks {
			return fmt.Errorf("the Content Server has %d of %d books in KEPUB", n, koboBooks)
		}
		return nil
	})
	all := time.Since(start)
	tb.Logf("every book in KEPUB in the Content Server: %v, %s after the set-up page", err, all.Round(time.Millisecond))
	if perr := harness.Pause(ctx, 20*time.Second); perr != nil {
		tb.Fatal(perr)
	}
	stopPoll()
	n, r := <-answers, <-refused
	sm := smp.stop(tb)
	rs := restarts(sm)
	var down float64
	for _, x := range rs {
		down += x.up - x.down
	}
	changes := 0
	for _, s := range sm.Samples {
		if s.DB {
			changes++
		}
	}
	tasks, _ := b.web.get(ctx, "/ajax/emailstat", nil)
	tb.Logf("calibre-server restarted %d times, down %.2f s in all; %d samples saw metadata.db change; %d of %d reads through 8080 got 503; /tmp at most %s",
		len(rs), down, changes, r, n, mib(sm.TmpPeak))
	for _, x := range rs {
		tb.Logf("  restart at %.2f s: down %.2f s", x.down, x.up-x.down)
	}
	tb.Logf("Calibre-Web's tasks: %s", harness.Clip(string(tasks.Body)))
	harness.NewChecks(tb).True("every conversion of the first sync reaches the Content Server", err == nil, fmt.Sprint(err))
}

// tmpWhile returns the most /tmp held in c while do ran, and from how
// much.
func tmpWhile(tb testing.TB, c *harness.Container, do func() error) (sampled, error) {
	tb.Helper()
	smp := startSampler(tb, c)
	err := do()
	return smp.stop(tb), err
}

// TestMeasureLargeBook records the most /tmp holds while each app adds
// and converts a large book: the 300 MiB PDF of TestLargeUpload, and a
// 100 MiB comic of 20 pages, each added through 8080, then converted to
// EPUB in the Content Server and to MOBI in Calibre-Web.
func TestMeasureLargeBook(t *testing.T) { //nolint:paralleltest // measured alone, so that no other test's containers change its numbers
	e, image := underTest(t)
	tb := redacting(t)
	ctx := tb.Context()
	b := serveBooks(tb, e, image, titled("Large Book", 1), nil)
	pid, err := appPID(ctx, b.s.C, contentServer)
	if err != nil {
		tb.Fatal(err)
	}

	pages := make([]archived, 20)
	for i := range pages {
		pages[i] = archived{name: fmt.Sprintf("page%02d.png", i+1), data: noisePNG(1600, 1100, uint64(i+1))}
	}
	comic := cbz(pages)
	tb.Logf("the comic: %s", mib(int64(len(comic))))
	pdf, pdfSize := pdfPage(612, 792, pageRed, largeSize)
	var pdfID, comicID int
	m, err := tmpWhile(tb, b.s.C, func() error {
		var aerr error
		pdfID, aerr = b.srv.addBookFrom(ctx, "large.pdf", pdf, pdfSize)
		return aerr
	})
	tb.Logf("8080: add the %s PDF: /tmp at most %s, from %s: %v", mib(pdfSize), mib(m.TmpPeak), mib(m.TmpBase), err)
	m, err = tmpWhile(tb, b.s.C, func() error {
		var aerr error
		comicID, aerr = b.srv.addBookFrom(ctx, "comic.cbz", bytes.NewReader(comic), int64(len(comic)))
		return aerr
	})
	tb.Logf("8080: add the comic: /tmp at most %s: %v", mib(m.TmpPeak), err)
	for _, c := range []struct {
		name string
		id   int
		from string
	}{{"the PDF", pdfID, "PDF"}, {"the comic", comicID, "CBZ"}} {
		start := time.Now()
		m, err = tmpWhile(tb, b.s.C, func() error {
			_, cerr := b.srv.convert(ctx, c.id, c.from, "epub", 15*time.Minute)
			return cerr
		})
		tb.Logf("8080: convert %s to EPUB in %s: /tmp at most %s, from %s: %v", c.name, time.Since(start).Round(time.Second), mib(m.TmpPeak), mib(m.TmpBase), err)
	}
	pid = waitAfterServerChange(tb, b, pid)
	for _, c := range []struct {
		name string
		id   int
		from string
	}{{"the PDF", pdfID, "PDF"}, {"the comic", comicID, "CBZ"}} {
		start := time.Now()
		m, err = tmpWhile(tb, b.s.C, func() error {
			if cerr := b.web.convert(ctx, c.id, c.from, "MOBI"); cerr != nil {
				return cerr
			}
			return within(ctx, 15*time.Minute, func(ctx context.Context) error { return webHasFormat(ctx, b.web, c.id, "MOBI") })
		})
		tb.Logf("8083: convert %s to MOBI in %s: /tmp at most %s, from %s: %v", c.name, time.Since(start).Round(time.Second), mib(m.TmpPeak), mib(m.TmpBase), err)
		pid = reloaded(tb, b.s, pid)
	}
}

// TestMeasurePDFCover uploads to Calibre-Web PDFs whose first page is
// square, and larger each time, and records the most /tmp held while
// Calibre-Web made each cover, and whether the cover is the page, red, or
// its generic one. Calibre-Web reads the page at 150 dots an inch, so a
// page of 2,700 points is 5,625 pixels square, 31.6 megapixels, under
// ImageMagick's 32 in memory, and one of 3,000 points is 39 megapixels,
// past it, where ImageMagick keeps the pixels in its disk cache, in /tmp.
func TestMeasurePDFCover(t *testing.T) { //nolint:paralleltest // measured alone, so that no other test's containers change its numbers
	e, image := underTest(t)
	tb := redacting(t)
	ctx := tb.Context()
	b := serveBooks(tb, e, image, titled("Cover Book", 1), nil)
	pid, err := appPID(ctx, b.s.C, contentServer)
	if err != nil {
		tb.Fatal(err)
	}
	for _, side := range []int{612, 2000, 2700, 3000, 3500, 3900} {
		big, size := pdfPage(side, side, pageRed, 0)
		var id int
		m, err := tmpWhile(tb, b.s.C, func() error {
			var uerr error
			id, uerr = b.web.upload(ctx, fmt.Sprintf("square%d.pdf", side), big, size)
			return uerr
		})
		cover, cerr := b.web.cover(ctx, id)
		colour, _ := meanColour(cover)
		tb.Logf("8083: a PDF whose first page is %d points square, %d pixels at 150 dots an inch: /tmp at most %s, from %s; its cover: %d bytes, %+v, the page's: %t: %v, %v",
			side, side*150/72, mib(m.TmpPeak), mib(m.TmpBase), len(cover), colour, colour.near(pageRed), err, cerr)
		pid = reloaded(tb, b.s, pid)
	}
	logs, _ := b.s.C.Logs(ctx)
	for l := range strings.SplitSeq(logs, "\n") {
		if strings.Contains(l, "Cannot extract") || strings.Contains(l, "Imagemagick") {
			tb.Logf("log: %s", l)
		}
	}
}

// waitAfterServerChange gives PID 1 the time to reconnect Calibre-Web
// after the Content Server changed the library, so that a test's next
// change in Calibre-Web is not put down to the Content Server too, and
// returns calibre-server's PID, which must not have changed.
func waitAfterServerChange(tb testing.TB, b *both, pid int) int {
	tb.Helper()
	if other, err := steady(tb.Context(), b.s.C, contentServer, pid, steadyFor); err != nil || other != 0 {
		tb.Fatalf("calibre-server is restarted after its own change: PID %d, then %d: %v", pid, other, err)
	}
	return pid
}

// TestMeasureDownloadAfterEdit has calibre-server open the library, with
// a read through 8080, as any use of the Content Server or a download
// from Calibre-Web does; then edits a title in Calibre-Web, and asks
// Calibre-Web for the book every half second. It records what each answer
// was, until the download has the new title written in. Calibre-Web reads
// the file, with its metadata written in, through calibre-server, which
// knows of the edit only once PID 1 has restarted it.
func TestMeasureDownloadAfterEdit(t *testing.T) { //nolint:paralleltest // measured alone, so that no other test's containers change its numbers
	e, image := underTest(t)
	tb := redacting(t)
	ctx := tb.Context()
	b := serveBooks(tb, e, image, titled("Download Book", 1), nil)
	pid, err := appPID(ctx, b.s.C, contentServer)
	if err != nil {
		tb.Fatal(err)
	}
	if err = serverShows(ctx, b.srv, 1, "Download Book 1"); err != nil {
		tb.Fatal(err)
	}
	const title = "Edited Before A Download"
	editTitle(tb, b.web, 1, title)
	edited := time.Now()
	var seen []string
	last := ""
	err = within(ctx, seenWithin, func(ctx context.Context) error {
		resp, gerr := b.web.get(ctx, "/download/1/epub/book", nil)
		got := fmt.Sprint(gerr)
		if gerr == nil {
			got = strconv.Itoa(resp.Status)
			if resp.Status == http.StatusOK {
				t, terr := epubOPFTitle(resp.Body)
				got = fmt.Sprintf("200 %q %v", t, terr)
				if t == title {
					seen = append(seen, fmt.Sprintf("%.2f s: %s", time.Since(edited).Seconds(), got))
					return nil
				}
			}
		}
		if got != last {
			seen = append(seen, fmt.Sprintf("%.2f s: %s", time.Since(edited).Seconds(), got))
			last = got
		}
		return fmt.Errorf("the download is %s", got)
	})
	now, _ := appPID(ctx, b.s.C, contentServer)
	tb.Logf("after an edit in Calibre-Web, its downloads were: %s (calibre-server PID %d, then %d): %v", strings.Join(seen, "; "), pid, now, err)
	harness.NewChecks(tb).True("a download has the new title within a minute", err == nil, fmt.Sprint(err))
}

// TestMeasureComicUpload uploads the fixture comic to Calibre-Web twice
// as a .cbr: as a RAR archive, its files stored as they are, and as a ZIP
// archive, a CBZ renamed. It records what Calibre-Web made of each: the
// book's id, or why it refused it, and whether the book's page shows the
// comic's metadata.
func TestMeasureComicUpload(t *testing.T) { //nolint:paralleltest // measured alone, so that no other test's containers change its numbers
	e, image := underTest(t)
	tb := redacting(t)
	ctx := tb.Context()
	b := serveBooks(tb, e, image, titled("Comic Book", 1), nil)
	for _, c := range []struct {
		name string
		data []byte
	}{{"a RAR archive", cbr(comicFiles())}, {"a CBZ renamed", cbz(comicFiles())}} {
		before, err := b.s.C.Logs(ctx)
		if err != nil {
			tb.Fatal(err)
		}
		id, err := b.web.upload(ctx, "comic.cbr", bytes.NewReader(c.data), int64(len(c.data)))
		after, lerr := b.s.C.Logs(ctx)
		if lerr != nil {
			tb.Fatal(lerr)
		}
		shown := false
		if err == nil {
			page, perr := b.web.bookPage(ctx, id)
			shown = perr == nil && bytes.Contains(page.Body, []byte(comicTitle)) && bytes.Contains(page.Body, []byte(comicSeries))
		}
		tb.Logf("%s as comic.cbr: book %d, %v; its page shows the comic's title and series: %t; Calibre-Web logged: %s",
			c.name, id, err, shown, none(errorLine.FindAllString(strings.TrimPrefix(after, before), -1)))
	}
}

// serverBodyLimit is calibre-server's own limit on a request's body: its
// --max-request-body-size, 500 MB by default, which it counts in MiB
// (src/calibre/srv/opts.py, src/calibre/srv/http_request.py).
const serverBodyLimit = 500 << 20

// TestMeasureUploadLimits uploads PDFs of growing size to each port, each
// to a container of its own, and records what each app answered, and the
// most /tmp held: to 8080, calibre-server's own limit and a byte more; to
// 8083, where Calibre-Web sets no limit of its own, sizes around what a
// 2 GiB /tmp holds.
func TestMeasureUploadLimits(t *testing.T) { //nolint:paralleltest // measured alone, so that no other test's containers change its numbers
	e, image := underTest(t)
	uploads := []struct {
		port string
		pad  int64 // the PDF's padding: it is about 1 KiB longer
		past bool  // send calibre-server's limit and one byte more, cut from the PDF
	}{
		{"8080", 400 << 20, false},
		{"8080", serverBodyLimit - 1<<20, false},
		{"8080", serverBodyLimit, true},
		{"8083", 900 << 20, false},
		{"8083", 1100 << 20, false},
	}
	for _, u := range uploads {
		tb := redacting(t)
		ctx := tb.Context()
		b := serveBooks(tb, e, image, titled("Limit Book", 1), nil)
		body, size := pdfPage(612, 792, pageRed, u.pad)
		if u.past {
			body, size = io.LimitReader(body, serverBodyLimit+1), serverBodyLimit+1
		}
		var id int
		start := time.Now()
		m, err := tmpWhile(tb, b.s.C, func() error {
			var uerr error
			if u.port == "8080" {
				id, uerr = b.srv.addBookFrom(ctx, "limit.pdf", body, size)
			} else {
				id, uerr = b.web.upload(ctx, "limit.pdf", body, size)
			}
			return uerr
		})
		peak, _ := pid1Peak(ctx, b.s.C)
		logs, _ := b.s.C.Logs(ctx)
		var said []string
		for l := range strings.SplitSeq(logs, "\n") {
			if errorLine.MatchString(l) || strings.Contains(l, "could not answer") || strings.Contains(l, "No space") {
				said = append(said, l)
			}
		}
		tb.Logf("%s: %d bytes (%s): book %d, %v, in %s; /tmp at most %s; PID 1 at most %d KiB; the log: %s",
			u.port, size, mib(size), id, err, time.Since(start).Round(time.Millisecond), mib(m.TmpPeak), peak, none(said))
	}
}

// tmpHoldersCode is Python that prints, as JSON, how much of /tmp is used,
// each file under /tmp of a MiB or more, by size, and each file of a MiB
// or more that a process holds open after it was deleted.
const tmpHoldersCode = `import json, os
st = os.statvfs("/tmp")
out = {"used": (st.f_blocks - st.f_bfree) * st.f_frsize, "files": {}, "deleted": {}}
for root, dirs, files in os.walk("/tmp"):
    for f in files:
        p = os.path.join(root, f)
        try:
            size = os.path.getsize(p)
        except OSError:
            continue
        if size >= 1 << 20:
            out["files"][p] = size
for d in os.listdir("/proc"):
    if not d.isdigit():
        continue
    try:
        fds = os.listdir(f"/proc/{d}/fd")
    except OSError:
        continue
    for fd in fds:
        try:
            target = os.readlink(f"/proc/{d}/fd/{fd}")
            if target.startswith("/tmp/") and target.endswith(" (deleted)"):
                size = os.stat(f"/proc/{d}/fd/{fd}").st_size
                if size >= 1 << 20:
                    out["deleted"][f"{d}:{target}"] = size
        except OSError:
            pass
print(json.dumps(out))
`

// TestMeasureUploadTmp uploads the 300 MiB PDF to 8080, then records what
// holds /tmp every 20 s for two minutes, and once more after an edit in
// Calibre-Web has PID 1 restart calibre-server.
func TestMeasureUploadTmp(t *testing.T) { //nolint:paralleltest // measured alone, so that no other test's containers change its numbers
	e, image := underTest(t)
	tb := redacting(t)
	ctx := tb.Context()
	b := serveBooks(tb, e, image, titled("Tmp Book", 1), nil)
	pid, err := appPID(ctx, b.s.C, contentServer)
	if err != nil {
		tb.Fatal(err)
	}
	body, size := pdfPage(612, 792, pageRed, largeSize)
	if _, err = b.srv.addBookFrom(ctx, "large.pdf", body, size); err != nil {
		tb.Fatal(err)
	}
	report := func(when string) {
		out, err := inPython(ctx, b.s.C, tmpHoldersCode)
		tb.Logf("%s: %s: %v", when, strings.TrimSpace(out), err)
	}
	for i := range 7 {
		report(fmt.Sprintf("%d s after the upload", 20*i))
		if i < 6 {
			if err := harness.Pause(ctx, 20*time.Second); err != nil {
				tb.Fatal(err)
			}
		}
	}
	editTitle(tb, b.web, 1, "Edited To Restart")
	reloaded(tb, b.s, pid)
	report("after calibre-server's restart")
}
