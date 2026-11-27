// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"context"
	"encoding/base64"
	"encoding/csv"
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/divbast/ci/harness"
)

// The bounds of the suite's checks that the two apps stay in step.
const (
	// seenWithin bounds how long a change one app makes takes to show in
	// the other: PID 1 sees it within its 1 s poll, waits 5 s of quiet,
	// and a restart of calibre-server then takes a few seconds more.
	seenWithin = 60 * time.Second
	// steadyFor is how long a writer's PID must stay the same after its
	// change shows in the other app: longer than the 5 s of quiet after
	// which PID 1 reloads, and the 10 s in which it puts a change down to
	// the request that made it.
	steadyFor = 15 * time.Second
	// pendingAfter is how long after a Calibre-Web edit a test waits
	// before it relies on PID 1 having seen it: two of its 1 s polls,
	// with room to spare.
	pendingAfter = 2500 * time.Millisecond
	// heldFor is how long a test watches a restart that a conversion must
	// hold back: well past the 5 s of quiet and the 10 s window.
	heldFor = 20 * time.Second
	// convertWithin bounds one conversion of a fixture book.
	convertWithin = 2 * time.Minute
)

// both is a container serving a fixture library, with admin's client of
// each app.
type both struct {
	s   *harness.Serving
	srv *server
	web *webSession
}

// serveBooks serves books, a new fixture library, with env added to the
// container's environment, and logs admin in to both apps.
func serveBooks(r *redactor, e harness.Engine, image string, books []shelf, env map[string]string) *both {
	r.Helper()
	sec := newSecrets(r)
	l := &launch{
		env:       sec.env(),
		mounts:    []harness.Mount{bookLibrary(r, e, image, books), volume(r, e, configDir), sec.mount()},
		passwords: sec.all(),
	}
	maps.Copy(l.env, env)
	s := l.serve(r, e, image)
	return &both{s: s, srv: &server{s: s, pw: sec.admin}, web: loginAdmin(r, s, sec.web)}
}

// within polls check every half second, for at most d, until it holds.
func within(ctx context.Context, d time.Duration, check func(context.Context) error) error {
	return harness.Poll(ctx, d, 500*time.Millisecond, func(ctx context.Context) error {
		if err := check(ctx); err != nil {
			return harness.NotYet(err)
		}
		return nil
	})
}

// steady checks that app in c keeps the PID pid for d, and returns the
// first other PID it saw, or 0.
func steady(ctx context.Context, c *harness.Container, app string, pid int, d time.Duration) (int, error) {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		got, err := appPID(ctx, c, app)
		if err == nil && got != pid {
			return got, nil
		}
		if err := harness.Pause(ctx, healthEvery); err != nil {
			return 0, err
		}
	}
	return 0, nil
}

// syncPath is one way a change goes from one app to the other: how a test
// makes it in the writer, and how it sees it in the reader.
type syncPath struct {
	change func(ctx context.Context, b *both) error
	seen   func(ctx context.Context, b *both) error
}

// The fixture library of TestSyncEachPath: book 1 is changed, and book 2
// is left as it is.
var syncBooks = titled("Sync Book", 2)

// syncedTitle is the title a test edits book 1 to.
const syncedTitle = "Edited For Sync"

// contentServerPaths are the changes the Content Server makes, through
// port 8080, each seen in Calibre-Web.
func contentServerPaths(f *fixtures, cover []byte) map[string]syncPath {
	return map[string]syncPath{
		"edit title": {
			change: func(ctx context.Context, b *both) error {
				resp, err := b.srv.setTitle(ctx, 1, syncedTitle)
				return answered("editing a title in the Content Server", resp, err)
			},
			seen: func(ctx context.Context, b *both) error { return webShows(ctx, b.web, 1, syncedTitle) },
		},
		"add book": {
			change: func(ctx context.Context, b *both) error {
				id, err := b.srv.addBook(ctx, "added.epub", f.epub)
				if err == nil && id != 3 {
					err = fmt.Errorf("the new book is book %d, not 3", id)
				}
				return err
			},
			seen: func(ctx context.Context, b *both) error { return webShows(ctx, b.web, 3, epubTitle) },
		},
		"delete book": {
			change: func(ctx context.Context, b *both) error { return b.srv.deleteBook(ctx, 1) },
			seen:   func(ctx context.Context, b *both) error { return webGone(ctx, b.web, 1) },
		},
		"convert": {
			change: func(ctx context.Context, b *both) error {
				_, err := b.srv.convert(ctx, 1, "EPUB", "mobi", convertWithin)
				return err
			},
			seen: func(ctx context.Context, b *both) error { return webHasFormat(ctx, b.web, 1, "MOBI") },
		},
		"set cover": {
			change: func(ctx context.Context, b *both) error { return b.srv.setCover(ctx, 1, cover) },
			seen: func(ctx context.Context, b *both) error {
				img, err := b.web.cover(ctx, 1)
				return isColour(img, err, coverBlue)
			},
		},
	}
}

// calibreWebPaths are the changes Calibre-Web makes, through port 8083,
// each seen in the Content Server.
func calibreWebPaths(f *fixtures, cover []byte) map[string]syncPath {
	return map[string]syncPath{
		"edit title": {
			change: func(ctx context.Context, b *both) error { return webEditTitle(ctx, b.web, 1, syncedTitle) },
			seen:   func(ctx context.Context, b *both) error { return serverShows(ctx, b.srv, 1, syncedTitle) },
		},
		"add book": {
			change: func(ctx context.Context, b *both) error {
				id, err := b.web.upload(ctx, "added.epub", strings.NewReader(string(f.epub)), int64(len(f.epub)))
				if err == nil && id != 3 {
					err = fmt.Errorf("the new book is book %d, not 3", id)
				}
				return err
			},
			seen: func(ctx context.Context, b *both) error { return serverShows(ctx, b.srv, 3, epubTitle) },
		},
		"delete book": {
			change: func(ctx context.Context, b *both) error { return b.web.deleteBook(ctx, 1) },
			seen: func(ctx context.Context, b *both) error {
				_, status, err := b.srv.book(ctx, 1)
				if err == nil && status != http.StatusNotFound {
					err = fmt.Errorf("the Content Server answers %d for book 1", status)
				}
				return err
			},
		},
		"convert": {
			change: func(ctx context.Context, b *both) error { return b.web.convert(ctx, 1, "EPUB", "MOBI") },
			seen:   func(ctx context.Context, b *both) error { return serverHasFormat(ctx, b.srv, 1, "MOBI") },
		},
		"set cover": {
			change: func(ctx context.Context, b *both) error { return b.web.setCover(ctx, 1, cover) },
			seen: func(ctx context.Context, b *both) error {
				img, err := b.srv.cover(ctx, 1)
				return isColour(img, err, coverBlue)
			},
		},
	}
}

// webShows is an error unless Calibre-Web shows book id with title.
func webShows(ctx context.Context, w *webSession, id int, title string) error {
	ok, err := w.shows(ctx, id, title)
	if err == nil && !ok {
		err = fmt.Errorf("calibre-web does not show book %d as %q", id, title)
	}
	return err
}

// webGone is an error unless Calibre-Web no longer has book id: it sends
// a request for its page to the start page.
func webGone(ctx context.Context, w *webSession, id int) error {
	resp, err := w.bookPage(ctx, id)
	if err == nil && resp.Status != http.StatusFound {
		err = fmt.Errorf("calibre-web answers %d for book %d", resp.Status, id)
	}
	return err
}

// libraryFileCode prints, in base64, the one file of book sys.argv[1]
// with the extension sys.argv[2] in the library, as calibre and
// Calibre-Web lay it out: /books/<author>/<title> (<id>)/<file>.
const libraryFileCode = `
import base64, glob, sys
paths = glob.glob("/books/*/* (%s)/*.%s" % (sys.argv[1], sys.argv[2]))
if len(paths) != 1:
    sys.exit("%d files: %r" % (len(paths), paths))
with open(paths[0], "rb") as f:
    sys.stdout.write(base64.b64encode(f.read()).decode())
`

// libraryFile returns book id's file with the extension ext, as it is in
// the library in c.
func libraryFile(ctx context.Context, c *harness.Container, id int, ext string) ([]byte, error) {
	out, err := inPython(ctx, c, libraryFileCode, strconv.Itoa(id), ext)
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(out)
}

// webHasFormat is an error unless Calibre-Web offers book id in format.
func webHasFormat(ctx context.Context, w *webSession, id int, format string) error {
	ok, err := w.hasFormat(ctx, id, format)
	if err == nil && !ok {
		err = fmt.Errorf("calibre-web does not offer book %d as %s", id, format)
	}
	return err
}

// webEditTitle sets book id's title in Calibre-Web, as editTitle does,
// returning an error rather than failing the test.
func webEditTitle(ctx context.Context, w *webSession, id int, title string) error {
	if _, err := w.get(ctx, "/", nil); err != nil {
		return err
	}
	resp, err := w.postJSON(ctx, "/ajax/editbooks/title", map[string]any{"pk": []int{id}, "value": title})
	if err == nil && (resp.Status != http.StatusOK || !strings.Contains(string(resp.Body), `"success":true`)) {
		err = fmt.Errorf("calibre-web answered %d: %s", resp.Status, harness.Clip(string(resp.Body)))
	}
	if err != nil {
		return fmt.Errorf("editing book %d's title in Calibre-Web: %w", id, err)
	}
	return nil
}

// serverShows is an error unless the Content Server has book id with
// title.
func serverShows(ctx context.Context, c *server, id int, title string) error {
	b, status, err := c.book(ctx, id)
	if err == nil && (status != http.StatusOK || b.Title != title) {
		err = fmt.Errorf("the Content Server answers %d for book %d, titled %q", status, id, b.Title)
	}
	return err
}

// serverHasFormat is an error unless the Content Server has book id in
// format.
func serverHasFormat(ctx context.Context, c *server, id int, format string) error {
	b, status, err := c.book(ctx, id)
	if err != nil {
		return err
	}
	for _, f := range b.Formats {
		if strings.EqualFold(f, format) {
			return nil
		}
	}
	return fmt.Errorf("the Content Server answers %d for book %d, in %s alone", status, id, none(b.Formats))
}

// isColour is an error unless img, read with err, is near want.
func isColour(img []byte, err error, want rgb) error {
	if err != nil {
		return err
	}
	got, err := meanColour(img)
	if err == nil && !got.near(want) {
		err = fmt.Errorf("the cover's colour is %+v, not near %+v", got, want)
	}
	return err
}

// TestSyncEachPath makes each kind of change, edit a title, add a book,
// delete one, convert one and set a cover, in each app in turn: within a
// minute the other app shows it, and the app that made it was never
// restarted, since PID 1 reloads only the other app. Calibre-Web reads the
// library afresh at each request, and calibre-server only once PID 1 has
// restarted it.
func TestSyncEachPath(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	f := fixtureBooks(t, e, image)
	cover := solidPNG(300, 400, coverBlue)
	writers := []struct {
		port         string
		writer, read string
		paths        map[string]syncPath
	}{
		{"8080 to 8083", contentServer, calibreWeb, contentServerPaths(f, cover)},
		{"8083 to 8080", calibreWeb, contentServer, calibreWebPaths(f, cover)},
	}
	for _, w := range writers {
		for name, p := range w.paths {
			t.Run(w.port+"/"+name, func(t *testing.T) {
				t.Parallel()
				tb := redacting(t)
				ctx := tb.Context()
				b := serveBooks(tb, e, image, syncBooks, nil)
				pid, err := appPID(ctx, b.s.C, w.writer)
				if err != nil {
					tb.Fatal(err)
				}
				if p.seen(ctx, b) == nil {
					tb.Fatalf("%s already shows the change before it is made", w.read)
				}
				if err = p.change(ctx, b); err != nil {
					tb.Fatal(err)
				}
				checks := harness.NewChecks(tb)
				start := time.Now()
				err = within(ctx, seenWithin, func(ctx context.Context) error { return p.seen(ctx, b) })
				checks.True(w.read+" shows the change within "+seenWithin.String(), err == nil, fmt.Sprintf("after %s: %v", time.Since(start).Round(time.Millisecond), err))
				other, err := steady(ctx, b.s.C, w.writer, pid, steadyFor)
				checks.True(w.writer+", which made the change, keeps its PID", err == nil && other == 0, fmt.Sprintf("PID %d, then %d: %v", pid, other, err))
			})
		}
	}
}

// refusedAsPending checks that resp is the 503 port 8080 gives a change
// while a restart of calibre-server is to come: Retry-After 10, and one
// line that says why.
func refusedAsPending(tb testing.TB, what string, resp harness.Response, err error) {
	tb.Helper()
	harness.Answers(tb, what+": 503", http.StatusServiceUnavailable, resp, err)
	checks := harness.NewChecks(tb)
	harness.Equal(checks, what+": Retry-After", "10", resp.Header.Get("Retry-After"))
	checks.True(what+": one line says why", len(lines(string(resp.Body))) == 1 && strings.Contains(string(resp.Body), "Calibre-Web"), harness.Clip(string(resp.Body)))
}

// TestWritesRefusedWhilePending holds a restart of calibre-server back,
// with a conversion in the Content Server that no page polls, and then
// edits a title in Calibre-Web: while the restart is to come, a change
// through port 8080 gets 503 with Retry-After 10, and a read answers 200.
func TestWritesRefusedWhilePending(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	tb := redacting(t)
	ctx := tb.Context()
	b := serveBooks(tb, e, image, syncBooks, nil)
	pid, err := appPID(ctx, b.s.C, contentServer)
	if err != nil {
		tb.Fatal(err)
	}
	if _, err = b.srv.startConversion(ctx, 1, "EPUB", "mobi"); err != nil {
		tb.Fatal(err)
	}
	editTitle(tb, b.web, 2, syncedTitle)
	if err = harness.Pause(ctx, pendingAfter); err != nil {
		tb.Fatal(err)
	}
	resp, err := b.srv.setTitle(ctx, 1, "Refused While Pending")
	refusedAsPending(tb, "a title edit through 8080 while a restart is to come", resp, err)
	read, status, err := b.srv.book(ctx, 1)
	harness.Answers(tb, "a read through 8080 while a restart is to come", http.StatusOK, harness.Response{Status: status}, err)
	harness.Equal(harness.NewChecks(tb), "the refused edit changed nothing", syncBooks[0].Title, read.Title)
	now, err := appPID(ctx, b.s.C, contentServer)
	harness.NewChecks(tb).True("calibre-server is not restarted yet: the conversion holds it", err == nil && now == pid, fmt.Sprintf("PID %d, then %d: %v", pid, now, err))
}

// TestConversionDelaysRestart starts a conversion in the Content Server,
// which no page polls, then edits a title in Calibre-Web: calibre-server
// is not restarted while the conversion runs. The conversion's status
// polls pass, and once one says it is done, PID 1 restarts calibre-server,
// which then shows the edit.
func TestConversionDelaysRestart(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	tb := redacting(t)
	ctx := tb.Context()
	b := serveBooks(tb, e, image, syncBooks, nil)
	pid, err := appPID(ctx, b.s.C, contentServer)
	if err != nil {
		tb.Fatal(err)
	}
	job, err := b.srv.startConversion(ctx, 1, "EPUB", "mobi")
	if err != nil {
		tb.Fatal(err)
	}
	editTitle(tb, b.web, 2, syncedTitle)
	checks := harness.NewChecks(tb)
	other, err := steady(ctx, b.s.C, contentServer, pid, heldFor)
	checks.True("calibre-server is not restarted for "+heldFor.String()+" while the conversion runs", err == nil && other == 0, fmt.Sprintf("PID %d, then %d: %v", pid, other, err))
	st, err := b.srv.finish(ctx, job, convertWithin)
	checks.True("the conversion's status polls pass, and it ends", err == nil && st.OK, fmt.Sprintf("%+v: %v", st.OK, err))
	restarted, err := waitNewPID(ctx, b.s.C, contentServer, pid)
	checks.True("once the conversion is done, PID 1 restarts calibre-server", err == nil, fmt.Sprintf("PID %d, then %d: %v", pid, restarted, err))
	err = within(ctx, seenWithin, func(ctx context.Context) error { return serverShows(ctx, b.srv, 2, syncedTitle) })
	checks.True("the restarted calibre-server shows Calibre-Web's edit", err == nil, fmt.Sprint(err))
	err = within(ctx, seenWithin, func(ctx context.Context) error { return serverHasFormat(ctx, b.srv, 1, "MOBI") })
	checks.True("and the conversion's new format", err == nil, fmt.Sprint(err))
}

// edit is one of the two edits of TestSimultaneousEdits: the title it
// sets, how its app answered, and when it was sent and answered.
type edit struct {
	title      string
	status     int
	err        error
	sent, done time.Time
}

// checkLibraryCode is Python that runs the bundle's own calibredb, the one
// the image's calibredb runs, as check_library over /books, in CSV, and
// prints its report.
const checkLibraryCode = `import os, subprocess, sys
env = dict(os.environ, CALIBRE_CONFIG_DIRECTORY="/tmp/calibre-config")
r = subprocess.run(["` + calibredb + `", "--with-library", "/books", "check_library", "--csv"], env=env, capture_output=True, text=True)
sys.stdout.write(r.stdout)
sys.stderr.write(r.stderr)
sys.exit(r.returncode)
`

// checkLibrary returns calibredb check_library's report on books, each
// problem as its check's name and the path, run as root in a one-off
// container while nothing else has the library open.
func checkLibrary(tb testing.TB, e harness.Engine, image string, books harness.Mount) []string {
	tb.Helper()
	out := rootPython(tb, e, image, checkLibraryCode, []harness.Mount{books})
	// check_library says first that it vacuums the database, which is no
	// problem found
	out = strings.TrimPrefix(out, "Vacuuming database...\n")
	csvr := csv.NewReader(strings.NewReader(out))
	csvr.FieldsPerRecord = -1
	rows, err := csvr.ReadAll()
	if err != nil {
		tb.Fatalf("reading check_library's report: %v: %s", err, harness.Clip(out))
	}
	found := make([]string, 0, len(rows))
	for _, r := range rows {
		found = append(found, strings.Join(r, ": "))
	}
	return found
}

// TestSimultaneousEdits edits one book's title in both apps within a
// second, in four orders, and then with the Content Server's edit just
// after PID 1 must have seen Calibre-Web's, which must then be refused.
// Any edit refused got 503; the edit that was accepted is in the library,
// the later one when both were; both apps show the same title; and
// calibredb's check_library finds no folder that the library does not
// name, nor any other problem. Before PID 1's next look at metadata.db,
// within a second, a Content Server that has not yet reloaded takes an
// edit, and the library stays whole.
func TestSimultaneousEdits(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	orders := []struct {
		name        string
		server, web time.Duration // when each edit is sent
		// seen has the Content Server's edit wait until PID 1 must have
		// seen Calibre-Web's: one of its 1 s looks after Calibre-Web
		// answered, and so after it wrote. The edit must then be refused,
		// and comes a little more than a second after Calibre-Web's
		seen bool
	}{
		{"Calibre-Web first, the Content Server 0.3 s later", 300 * time.Millisecond, 0, false},
		{"Calibre-Web first, the Content Server 0.9 s later", 900 * time.Millisecond, 0, false},
		{"the Content Server first, Calibre-Web 0.5 s later", 0, 500 * time.Millisecond, false},
		{"both at once", 0, 0, false},
		{"Calibre-Web first, the Content Server once PID 1 has seen it", 0, 0, true},
	}
	for _, o := range orders {
		t.Run(o.name, func(t *testing.T) {
			t.Parallel()
			tb := redacting(t)
			ctx := tb.Context()
			sec := newSecrets(tb)
			books := bookLibrary(tb, e, image, syncBooks)
			l := &launch{env: sec.env(), mounts: []harness.Mount{books, volume(tb, e, configDir), sec.mount()}, passwords: sec.all()}
			s := l.serve(tb, e, image)
			b := &both{s: s, srv: &server{s: s, pw: sec.admin}, web: loginAdmin(tb, s, sec.web)}
			if _, err := b.web.get(ctx, "/", nil); err != nil {
				tb.Fatal(err)
			}
			srvEdit := edit{title: "Edited In The Content Server"}
			webEdit := edit{title: "Edited In Calibre-Web"}
			var refusal harness.Response
			start := time.Now()
			webDone := make(chan struct{})
			var wg sync.WaitGroup
			wg.Go(func() {
				_ = harness.Pause(ctx, time.Until(start.Add(o.server)))
				if o.seen {
					<-webDone
					_ = harness.Pause(ctx, time.Second+100*time.Millisecond)
				}
				srvEdit.sent = time.Now()
				resp, err := b.srv.setTitle(ctx, 1, srvEdit.title)
				srvEdit.status, srvEdit.err, srvEdit.done, refusal = resp.Status, err, time.Now(), resp
			})
			wg.Go(func() {
				_ = harness.Pause(ctx, time.Until(start.Add(o.web)))
				webEdit.sent = time.Now()
				webEdit.err = webEditTitle(ctx, b.web, 1, webEdit.title)
				webEdit.done = time.Now()
				close(webDone)
				webEdit.status = http.StatusOK
				if webEdit.err != nil {
					webEdit.status = 0
				}
			})
			wg.Wait()
			if srvEdit.status == http.StatusServiceUnavailable {
				refusedAsPending(tb, "the Content Server's edit", refusal, srvEdit.err)
			}
			checks := harness.NewChecks(tb)
			gap := srvEdit.sent.Sub(webEdit.sent).Abs()
			if o.seen {
				harness.Equal(checks, "the Content Server refuses its edit, once PID 1 has seen Calibre-Web's", http.StatusServiceUnavailable, srvEdit.status)
				checks.True("the two edits are sent within two seconds", gap < 2*time.Second, gap.String())
			} else {
				checks.True("the two edits are sent within a second", gap < time.Second, gap.String())
			}
			checks.True("Calibre-Web accepts its edit", webEdit.err == nil, fmt.Sprint(webEdit.err))
			checks.True("the Content Server accepts its edit, or refuses it with 503",
				srvEdit.err == nil && (srvEdit.status == http.StatusOK || srvEdit.status == http.StatusServiceUnavailable), fmt.Sprintf("%d: %v", srvEdit.status, srvEdit.err))
			tb.Logf("the Content Server answered %d, %s after it was sent; Calibre-Web answered, %s after", srvEdit.status, srvEdit.done.Sub(srvEdit.sent).Round(time.Millisecond), webEdit.done.Sub(webEdit.sent).Round(time.Millisecond))

			// The title that must win: the only accepted edit's, or, when
			// both were accepted one after the other, the later's
			want := []string{webEdit.title}
			if srvEdit.status == http.StatusOK {
				switch {
				case srvEdit.sent.After(webEdit.done):
					want = []string{srvEdit.title}
				case !webEdit.sent.After(srvEdit.done):
					want = []string{srvEdit.title, webEdit.title}
				}
			}
			var shown string
			err := within(ctx, seenWithin, func(ctx context.Context) error {
				got, status, err := b.srv.book(ctx, 1)
				if err != nil || status != http.StatusOK {
					return fmt.Errorf("the Content Server answers %d: %w", status, err)
				}
				shown = got.Title
				for _, title := range want {
					if got.Title == title {
						return webShows(ctx, b.web, 1, title)
					}
				}
				return fmt.Errorf("the Content Server shows book 1 as %q, not as %s", got.Title, strings.Join(want, " or "))
			})
			checks.True("both apps show the edit that must win, "+strings.Join(want, " or "), err == nil, fmt.Sprintf("%q: %v", shown, err))
			if _, code := stop(tb, e, s.C); code != 0 {
				tb.Errorf("the container exits %d, not 0", code)
			}
			harness.Equal(checks, "check_library finds no problem in the library", "none", none(checkLibrary(tb, e, image, books)))
		})
	}
}

// TestIdleReloadsNothing makes no change: PID 1 then reloads nothing, and
// metadata.db stays as it was, through Calibre-Web's own reconnect, which
// PID 1 calls after each change the Content Server makes, and through
// calibre-server's first read of the library, more than 10 s after its
// start, when PID 1 no longer puts a change down to that start. It does so
// after the first start, and again after a planned restart.
func TestIdleReloadsNothing(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	tb := redacting(t)
	ctx := tb.Context()
	b := serveBooks(tb, e, image, syncBooks, nil)
	pid, err := appPID(ctx, b.s.C, contentServer)
	if err != nil {
		tb.Fatal(err)
	}
	checks := harness.NewChecks(tb)
	for _, after := range []string{"after the first start", "after a planned restart"} {
		smp := startSampler(tb, b.s.C)
		if err := harness.Pause(ctx, steadyFor-3*time.Second); err != nil {
			tb.Fatal(err)
		}
		out, err := inPython(ctx, b.s.C, reconnectCode)
		checks.True(after+": Calibre-Web reconnects", err == nil && strings.TrimSpace(out) == "200", fmt.Sprintf("%q: %v", out, err))
		err = serverShows(ctx, b.srv, 1, syncBooks[0].Title)
		checks.True(after+": the first read through 8080 answers", err == nil, fmt.Sprint(err))
		other, err := steady(ctx, b.s.C, contentServer, pid, steadyFor)
		checks.True(after+": calibre-server is not restarted", err == nil && other == 0, fmt.Sprintf("PID %d, then %d: %v", pid, other, err))
		m := smp.stop(tb)
		var changed []string
		for _, s := range m.Samples {
			if s.DB {
				changed = append(changed, fmt.Sprintf("%.1f s", s.At))
			}
		}
		harness.Equal(checks, after+": metadata.db changes", "none", none(changed))
		if after == "after the first start" {
			editTitle(tb, b.web, 2, syncedTitle)
			pid = reloaded(tb, b.s, pid)
		}
	}
}

// downloadsFor is how long TestReadsAfterEdit keeps downloading after an
// edit: past PID 1's 1 s look, its restart of calibre-server, and the
// next second, when 8080 opens again.
const downloadsFor = 12 * time.Second

// TestReadsAfterEdit reads a book through Calibre-Web right after a title
// edit there, with the library open in calibre-server, as any use of the
// Content Server leaves it. Calibre-Web reads a book's file through
// calibre-server, by calibredb's export, which PID 1 holds for a restart
// that it brings forward, so that calibre-server reads the library as it
// is: every download, from the first, has the new title written in, and
// none is refused or missing. A conversion to KEPUB right after an edit
// reads the book the same way, so the KEPUB it writes to the library has
// the new title in it too.
func TestReadsAfterEdit(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	t.Run("a download", func(t *testing.T) {
		t.Parallel()
		tb := redacting(t)
		ctx := tb.Context()
		b := serveBooks(tb, e, image, syncBooks, nil)
		if err := serverShows(ctx, b.srv, 1, syncBooks[0].Title); err != nil {
			tb.Fatal(err)
		}
		const title = "Edited Before A Download"
		editTitle(tb, b.web, 1, title)
		edited := time.Now()
		var seen, bad []string
		for time.Since(edited) < downloadsFor {
			epub, err := b.web.download(ctx, 1, "EPUB")
			got, terr := epubOPFTitle(epub)
			at := fmt.Sprintf("%.2f s", time.Since(edited).Seconds())
			if err != nil || terr != nil || got != title {
				bad = append(bad, fmt.Sprintf("%s: %q, %v, %v", at, got, err, terr))
			}
			seen = append(seen, at)
			if err := harness.Pause(ctx, 500*time.Millisecond); err != nil {
				tb.Fatal(err)
			}
		}
		tb.Logf("downloads at %s", strings.Join(seen, ", "))
		harness.Equal(harness.NewChecks(tb), fmt.Sprintf("the downloads in the %s after the edit that lack the new title", downloadsFor), "none", none(bad))
	})
	t.Run("a KEPUB conversion", func(t *testing.T) {
		t.Parallel()
		tb := redacting(t)
		ctx := tb.Context()
		b := serveBooks(tb, e, image, syncBooks, nil)
		if err := serverShows(ctx, b.srv, 2, syncBooks[1].Title); err != nil {
			tb.Fatal(err)
		}
		const title = "Edited Before A Conversion"
		editTitle(tb, b.web, 2, title)
		// Calibre-Web reads the book for kepubify through calibredb's
		// export alone, which PID 1 holds as a download's. The file is read
		// from the library as Calibre-Web wrote it: a download would write
		// the metadata in again
		if err := b.web.convert(ctx, 2, "EPUB", "KEPUB"); err != nil {
			tb.Fatal(err)
		}
		checks := harness.NewChecks(tb)
		err := within(ctx, convertWithin, func(ctx context.Context) error { return webHasFormat(ctx, b.web, 2, "KEPUB") })
		checks.True("Calibre-Web converts the book to KEPUB right after its edit", err == nil, fmt.Sprint(err))
		kepub, err := libraryFile(ctx, b.s.C, 2, "kepub")
		got, terr := epubOPFTitle(kepub)
		checks.True("the KEPUB is in the library, with a title", err == nil && terr == nil, fmt.Sprintf("%v, %v", err, terr))
		harness.Equal(checks, "the KEPUB's title, as in the library", title, got)
	})
}
