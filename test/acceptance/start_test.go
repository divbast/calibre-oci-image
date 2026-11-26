// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/divbast/ci/harness"
)

// TestRefusesWithoutPasswords is a first start with one admin password
// missing, the other given as a file: the start exits 1 within a minute,
// with one line that names the variable, and writes nothing to /books or
// /config.
func TestRefusesWithoutPasswords(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	for _, missing := range []string{adminVar, webAdminVar} {
		t.Run(missing, func(t *testing.T) {
			t.Parallel()
			sec := newSecrets(t)
			env := sec.env()
			delete(env, missing+"_FILE")
			st := newStore(t, e)
			l := &launch{env: env, mounts: append(st.mounts(), sec.mount()), passwords: sec.all()}
			refused(t, l.refuse(t, e, image), sec.all(), missing+" is not set")
			nothingWritten(t, e, image, st)
		})
	}
}

// weakPasswords are passwords that each break one of the rules the image
// holds an admin password to, for the variable that is given it: the five
// of Calibre-Web's default rules that can be broken alone, split between
// the two variables, then a control character, which the Content Server
// also refuses, and a line break, which no login form can send. Each is
// head, then n characters drawn at random from set, then, when breaks is
// set, breaks and 10 more random letters.
var weakPasswords = []struct {
	name, variable, rule string
	head, set, breaks    string
	n                    int
}{
	{"too short", adminVar, "needs at least 8 characters", "Aa1!", lower, "", 3},
	{"no digit", adminVar, "needs at least one digit", "Aa!", lower + upper, "", 20},
	{"no upper-case letter", adminVar, "needs at least one upper-case letter", "a1!", lower + digits, "", 20},
	{"a control character", adminVar, "must not hold a control character", "Aa1!", lower + upper + digits, "\t", 20},
	{"no lower-case letter", webAdminVar, "needs at least one lower-case letter", "A1!", upper + digits, "", 20},
	{
		"no special character", webAdminVar, "needs at least one character that is not a letter, a space or a digit 0-9",
		"Aa1", lower + upper + digits, "", 20,
	},
	{"a line break", webAdminVar, "must not hold a line break", "Aa1!", lower + upper + digits, "\n", 20},
}

// TestRefusesWeakPassword is a first start with one admin password that
// breaks a rule, given as a file: the start exits 1 within a minute, with
// one line that names the variable and the rule, prints no part of the
// password, and writes nothing to /books or /config.
func TestRefusesWeakPassword(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	for _, tc := range weakPasswords {
		t.Run(tc.variable+" "+tc.name, func(t *testing.T) {
			t.Parallel()
			weak := tc.head + random(t, tc.n, tc.set)
			if tc.breaks != "" {
				weak += tc.breaks + random(t, 10, lower)
			}
			adminPW, webPW := weak, password(t)
			if tc.variable == webAdminVar {
				adminPW, webPW = webPW, adminPW
			}
			sec := writeSecrets(t, adminPW, webPW)
			st := newStore(t, e)
			l := &launch{env: sec.env(), mounts: append(st.mounts(), sec.mount()), passwords: sec.all()}
			refused(t, l.refuse(t, e, image), sec.all(), tc.variable+"_FILE: the password", tc.rule)
			nothingWritten(t, e, image, st)
		})
	}
}

// TestFirstStart is a first start on empty volumes, with both passwords
// as files: /books gets a library, both admins log in with their
// passwords, and Calibre-Web's own default password does not work. No
// app's environment holds a password variable, and no process's command
// line holds a password.
func TestFirstStart(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	ctx := t.Context()
	sec := newSecrets(t)
	st := newStore(t, e)
	s := (&launch{env: sec.env(), mounts: append(st.mounts(), sec.mount()), passwords: sec.all()}).serve(t, e, image)
	checks := harness.NewChecks(t)

	made, err := isFile(ctx, s.C, booksDir+"/metadata.db")
	checks.True("an empty /books gets a Calibre library, /books/metadata.db", made && err == nil, fmt.Sprint(err))

	resp, err := serverAdmin(ctx, s, sec.admin, "/ajax/library-info")
	harness.Answers(t, "8080: admin logs in with CALIBRE_ADMIN_PASSWORD and reads the library", http.StatusOK, resp, err)
	resp, err = serverAdmin(ctx, s, sec.admin+"x", "/ajax/library-info")
	harness.Answers(t, "8080: admin with another password is refused", http.StatusUnauthorized, resp, err)

	w := newWebSession(t, s)
	resp, err = w.login(ctx, admin, sec.web, nil)
	harness.Answers(t, "8083: admin logs in with CALIBRE_WEB_ADMIN_PASSWORD", http.StatusFound, resp, err)
	resp, err = w.get(ctx, "/me", nil)
	harness.Answers(t, "8083: admin's own page answers once logged in", http.StatusOK, resp, err)
	resp, err = newWebSession(t, s).login(ctx, admin, "admin123", nil)
	harness.Answers(t, "8083: admin with Calibre-Web's default password, admin123, is refused", http.StatusUnauthorized, resp, err)

	for _, app := range apps {
		pid, perr := appPID(ctx, s.C, app)
		if perr != nil {
			checks.True(app+" runs", false, perr.Error())
			continue
		}
		names, nerr := environNames(ctx, s.C, pid)
		if nerr != nil {
			checks.True(app+"'s environment can be read", false, nerr.Error())
			continue
		}
		held := slices.DeleteFunc(names, func(n string) bool { return !slices.Contains(passwordVars, n) })
		harness.Equal(checks, app+"'s environment holds none of the password variables", "none", none(held))
	}

	ps, err := processes(ctx, s.C)
	if err != nil {
		t.Fatal(err)
	}
	var argv strings.Builder
	for i := range ps {
		argv.WriteString(strings.Join(ps[i].Argv, "\x00") + "\n")
	}
	noPassword(checks, "the command line of each of the "+strconv.Itoa(len(ps))+" processes", argv.String(), sec.all())
}

// TestRestartKeepsAccounts is a second start on the volumes of a first,
// with no password variable at all: it serves, both admins still log in
// with the first start's passwords, and it creates nothing again.
func TestRestartKeepsAccounts(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	ctx := t.Context()
	sec := newSecrets(t)
	st := newStore(t, e)
	first := (&launch{env: sec.env(), mounts: append(st.mounts(), sec.mount()), passwords: sec.all()}).serve(t, e, image)
	if _, code := stop(t, e, first.C); code != 0 {
		t.Fatalf("the first start exits %d when stopped", code)
	}

	s := (&launch{mounts: st.mounts(), passwords: sec.all()}).serve(t, e, image)
	resp, err := serverAdmin(ctx, s, sec.admin, "/ajax/library-info")
	harness.Answers(t, "8080: admin logs in with the first start's password", http.StatusOK, resp, err)
	resp, err = newWebSession(t, s).login(ctx, admin, sec.web, nil)
	harness.Answers(t, "8083: admin logs in with the first start's password", http.StatusFound, resp, err)
	harness.LogLacks(t, s.C, "the second start creates nothing", "divbast-calibre: created")
	harness.LogLacks(t, s.C, "the second start writes no first settings", "divbast-calibre: wrote")
}

// TestRefusesForeignOwnedBooks is a library made by the image's own
// calibredb, owned by uid 1000, as a desktop Calibre's is on the host: the
// first start exits 1 within a minute, with one line that names /books and
// says how to give it to uid 65532, and writes nothing to /config.
func TestRefusesForeignOwnedBooks(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	books := library(t, e, image, 1000)
	cfg := volume(t, e, configDir)
	sec := newSecrets(t)
	l := &launch{env: sec.env(), mounts: []harness.Mount{books, cfg, sec.mount()}, passwords: sec.all()}
	refused(t, l.refuse(t, e, image), sec.all(), "cannot write /books", "chown -R 65532:65532")
	out := rootPython(t, e, image, listCode, []harness.Mount{cfg}, configDir)
	harness.Equal(harness.NewChecks(t), "nothing is written to /config", "", strings.TrimSpace(out))
}

// TestExistingLibrary is the same library once given to uid 65532, as the
// refusal says: the first start keeps it, and both apps serve its book.
func TestExistingLibrary(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	ctx := t.Context()
	books := library(t, e, image, 1000)
	own(t, e, image, books, 65532)
	sec := newSecrets(t)
	s := (&launch{
		env:       sec.env(),
		mounts:    []harness.Mount{books, volume(t, e, configDir), sec.mount()},
		passwords: sec.all(),
	}).serve(t, e, image)
	checks := harness.NewChecks(t)
	harness.LogLacks(t, s.C, "the first start makes no new library", "created an empty Calibre library")

	resp, err := serverAdmin(ctx, s, sec.admin, "/ajax/books/books")
	harness.Answers(t, "8080: the library's books answer", http.StatusOK, resp, err)
	if err == nil {
		var got map[string]struct {
			Title   string   `json:"title"`
			Authors []string `json:"authors"`
		}
		uerr := json.Unmarshal(resp.Body, &got)
		checks.True("8080: the Content Server serves the library's one book, by its title and author",
			uerr == nil && len(got) == 1 && got["1"].Title == fixtureTitle && slices.Equal(got["1"].Authors, []string{fixtureAuthor}),
			harness.Clip(string(resp.Body)))
	}

	w := loginAdmin(t, s, sec.web)
	resp, err = w.get(ctx, "/book/1", nil)
	harness.Answers(t, "8083: the library's book has a page", http.StatusOK, resp, err)
	if err == nil {
		harness.Contains(t, "8083: Calibre-Web serves the book by its title", fixtureTitle, string(resp.Body))
		harness.Contains(t, "8083: Calibre-Web serves the book by its author", fixtureAuthor, string(resp.Body))
	}
}

// brokenAppDB is Python that puts a file that is no SQLite database where
// Calibre-Web keeps its settings, owned by uid 65532.
const brokenAppDB = ownDef + `
os.mkdir("/config/calibre-web", 0o700)
with open("/config/calibre-web/app.db", "w") as f:
    f.write("This is not a database.\n")
own("/config", 65532)
`

// TestServeFailsCleanly is a start that cannot go on, and one that can:
// Calibre-Web's settings broken, as a file that is no SQLite database,
// make the start exit 1 within a minute, with the helper's one line that
// names the problem and PID 1's one line, and nothing else; a start that
// goes on writes the state file the health check reads.
func TestServeFailsCleanly(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)

	t.Run("a broken app.db", func(t *testing.T) {
		t.Parallel()
		cfg := volume(t, e, configDir)
		rootPython(t, e, image, brokenAppDB, []harness.Mount{cfg})
		sec := newSecrets(t)
		l := &launch{env: sec.env(), mounts: []harness.Mount{volume(t, e, booksDir), cfg, sec.mount()}, passwords: sec.all()}
		r := l.refuse(t, e, image)
		checks := harness.NewChecks(t)
		harness.Equal(checks, "the start exits 1", 1, r.code)
		checks.True("the start ends within "+refuseWithin.String(), r.took < refuseWithin, r.took.String())
		got := lines(r.stderr)
		naming := slices.DeleteFunc(slices.Clone(got), func(l string) bool { return !strings.Contains(l, "file is not a database") })
		checks.True("one line names the problem", len(naming) == 1, harness.Clip(r.stderr))
		harness.Contains(t, "PID 1 says what failed", "divbast-calibre: reading Calibre-Web's login header setting failed", r.stderr)
		foreign := slices.DeleteFunc(got, func(l string) bool {
			return strings.HasPrefix(l, "calibre_web_init: ") || strings.HasPrefix(l, "divbast-calibre: ")
		})
		checks.True("stderr holds the helper's line and PID 1's, and nothing else, such as a traceback", len(got) == 2 && len(foreign) == 0, harness.Clip(r.stderr))
		noPassword(checks, "what the start printed", r.stdout+r.stderr, sec.all())
	})

	t.Run("the state file", func(t *testing.T) {
		t.Parallel()
		sec := newSecrets(t)
		s := (&launch{env: sec.env(), mounts: append(newStore(t, e).mounts(), sec.mount()), passwords: sec.all()}).serve(t, e, image)
		out, err := inPython(t.Context(), s.C, "import sys; sys.stdout.write(open(sys.argv[1]).read())", statePath)
		if err != nil {
			t.Fatalf("reading %s: %v", statePath, err)
		}
		var states map[string]struct {
			Up bool `json:"up"`
		}
		uerr := json.Unmarshal([]byte(out), &states)
		harness.NewChecks(t).True(statePath+" says both apps are up",
			uerr == nil && len(states) == 2 && states[contentServer].Up && states[calibreWeb].Up, harness.Clip(out))
	})
}
