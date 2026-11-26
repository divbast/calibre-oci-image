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
			tb := redacting(t)
			sec := newSecrets(tb)
			env := sec.env()
			delete(env, missing+"_FILE")
			st := newStore(tb, e)
			l := &launch{env: env, mounts: append(st.mounts(), sec.mount()), passwords: sec.all()}
			refused(tb, l.refuse(tb, e, image), sec.all(), missing+" is not set")
			nothingWritten(tb, e, image, st)
		})
	}
}

// weakPasswords are passwords that each break one of the rules the image
// holds an admin password to. Each is head, then n characters drawn at
// random from set, then, when breaks is set, breaks and 10 more random
// letters. Five are Calibre-Web's default rules, and a line break, which
// no login form can send: the image holds both passwords to these. A
// control character the Content Server refuses too, so only the admin
// password of the Content Server is held to it. Calibre-Web's sixth rule,
// a letter, cannot be broken alone: a password with a lower-case and an
// upper-case letter has a letter.
var weakPasswords = []struct {
	name, rule        string
	head, set, breaks string
	n                 int
	adminOnly         bool
}{
	{"too short", "needs at least 8 characters", "Aa1!", lower, "", 3, false},
	{"no digit", "needs at least one digit", "Aa!", lower + upper, "", 20, false},
	{"no lower-case letter", "needs at least one lower-case letter", "A1!", upper + digits, "", 20, false},
	{"no upper-case letter", "needs at least one upper-case letter", "a1!", lower + digits, "", 20, false},
	{
		"no special character", "needs at least one character that is not a letter, a space or a digit 0-9",
		"Aa1", lower + upper + digits, "", 20, false,
	},
	{"a line break", "must not hold a line break", "Aa1!", lower + upper + digits, "\n", 20, false},
	{"a control character", "must not hold a control character", "Aa1!", lower + upper + digits, "\t", 20, true},
}

// TestRefusesWeakPassword is a first start with one admin password that
// breaks a rule, given as a file, for every rule that holds for that
// variable: the start exits 1 within a minute, with one line that names
// the variable and the rule, prints no part of the password, and writes
// nothing to /books or /config.
func TestRefusesWeakPassword(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	for _, variable := range []string{adminVar, webAdminVar} {
		for _, tc := range weakPasswords {
			if tc.adminOnly && variable != adminVar {
				continue
			}
			t.Run(variable+" "+tc.name, func(t *testing.T) {
				t.Parallel()
				tb := redacting(t)
				weak := tc.head + random(tb, tc.n, tc.set)
				if tc.breaks != "" {
					weak += tc.breaks + random(tb, 10, lower)
				}
				tb.hide(weak)
				adminPW, webPW := weak, tb.password()
				if variable == webAdminVar {
					adminPW, webPW = webPW, adminPW
				}
				sec := writeSecrets(tb, adminPW, webPW)
				st := newStore(tb, e)
				l := &launch{env: sec.env(), mounts: append(st.mounts(), sec.mount()), passwords: sec.all()}
				refused(tb, l.refuse(tb, e, image), sec.all(), variable+"_FILE: the password", tc.rule)
				nothingWritten(tb, e, image, st)
			})
		}
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
	tb := redacting(t)
	ctx := tb.Context()
	sec := newSecrets(tb)
	st := newStore(tb, e)
	s := (&launch{env: sec.env(), mounts: append(st.mounts(), sec.mount()), passwords: sec.all()}).serve(tb, e, image)
	checks := harness.NewChecks(tb)

	made, err := isFile(ctx, s.C, booksDir+"/metadata.db")
	checks.True("an empty /books gets a Calibre library, /books/metadata.db", made && err == nil, fmt.Sprint(err))

	resp, err := serverAdmin(ctx, s, sec.admin, "/ajax/library-info")
	harness.Answers(tb, "8080: admin logs in with CALIBRE_ADMIN_PASSWORD and reads the library", http.StatusOK, resp, err)
	resp, err = serverAdmin(ctx, s, sec.admin+"x", "/ajax/library-info")
	harness.Answers(tb, "8080: admin with another password is refused", http.StatusUnauthorized, resp, err)

	w := newWebSession(tb, s)
	resp, err = w.login(ctx, admin, sec.web, nil)
	harness.Answers(tb, "8083: admin logs in with CALIBRE_WEB_ADMIN_PASSWORD", http.StatusFound, resp, err)
	resp, err = w.get(ctx, "/me", nil)
	harness.Answers(tb, "8083: admin's own page answers once logged in", http.StatusOK, resp, err)
	resp, err = newWebSession(tb, s).login(ctx, admin, "admin123", nil)
	harness.Answers(tb, "8083: admin with Calibre-Web's default password, admin123, is refused", http.StatusUnauthorized, resp, err)

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
		tb.Fatal(err)
	}
	var argv strings.Builder
	for i := range ps {
		argv.WriteString(strings.Join(ps[i].Argv, "\x00") + "\n")
	}
	noPassword(checks, "the command line of each of the "+strconv.Itoa(len(ps))+" processes", argv.String(), sec.all())
}

// firstThenStop serves the image on new volumes, with new passwords, and
// returns the volumes and the passwords once it has stopped, with exit 0.
func firstThenStop(r *redactor, e harness.Engine, image string) (*store, *secrets) {
	r.Helper()
	sec := newSecrets(r)
	st := newStore(r, e)
	first := (&launch{env: sec.env(), mounts: append(st.mounts(), sec.mount()), passwords: sec.all()}).serve(r, e, image)
	if _, code := stop(r, e, first.C); code != 0 {
		r.Fatalf("the first start exits %d when stopped", code)
	}
	return st, sec
}

// TestRestartKeepsAccounts is a second start on the volumes of a first,
// with no password variable at all: it serves, both admins still log in
// with the first start's passwords, and it creates nothing again.
func TestRestartKeepsAccounts(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	tb := redacting(t)
	ctx := tb.Context()
	st, sec := firstThenStop(tb, e, image)

	s := (&launch{mounts: st.mounts(), passwords: sec.all()}).serve(tb, e, image)
	resp, err := serverAdmin(ctx, s, sec.admin, "/ajax/library-info")
	harness.Answers(tb, "8080: admin logs in with the first start's password", http.StatusOK, resp, err)
	resp, err = newWebSession(tb, s).login(ctx, admin, sec.web, nil)
	harness.Answers(tb, "8083: admin logs in with the first start's password", http.StatusFound, resp, err)
	harness.LogLacks(tb, s.C, "the second start creates nothing", "divbast-calibre: created")
	harness.LogLacks(tb, s.C, "the second start writes no first settings", "divbast-calibre: wrote")
}

// TestRefusesForeignOwnedBooks is a library made by the image's own
// calibredb, owned by uid 1000, as a desktop Calibre's is on the host: the
// first start exits 1 within a minute, with one line that names /books and
// says how to give it to uid 65532, and writes nothing to /config.
func TestRefusesForeignOwnedBooks(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	tb := redacting(t)
	books := library(tb, e, image, 1000)
	cfg := volume(tb, e, configDir)
	sec := newSecrets(tb)
	l := &launch{env: sec.env(), mounts: []harness.Mount{books, cfg, sec.mount()}, passwords: sec.all()}
	refused(tb, l.refuse(tb, e, image), sec.all(), "cannot write /books", "chown -R 65532:65532")
	out := rootPython(tb, e, image, listCode, []harness.Mount{cfg}, configDir)
	harness.Equal(harness.NewChecks(tb), "nothing is written to /config", "", strings.TrimSpace(out))
}

// TestExistingLibrary is the same library once given to uid 65532, as the
// refusal says: the first start keeps it, and both apps serve its book.
func TestExistingLibrary(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	tb := redacting(t)
	ctx := tb.Context()
	books := library(tb, e, image, 1000)
	own(tb, e, image, books, 65532)
	sec := newSecrets(tb)
	s := (&launch{
		env:       sec.env(),
		mounts:    []harness.Mount{books, volume(tb, e, configDir), sec.mount()},
		passwords: sec.all(),
	}).serve(tb, e, image)
	checks := harness.NewChecks(tb)
	harness.LogLacks(tb, s.C, "the first start makes no new library", "created an empty Calibre library")

	resp, err := serverAdmin(ctx, s, sec.admin, "/ajax/books/books")
	harness.Answers(tb, "8080: the library's books answer", http.StatusOK, resp, err)
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

	w := loginAdmin(tb, s, sec.web)
	resp, err = w.get(ctx, "/book/1", nil)
	harness.Answers(tb, "8083: the library's book has a page", http.StatusOK, resp, err)
	if err == nil {
		harness.Contains(tb, "8083: Calibre-Web serves the book by its title", fixtureTitle, string(resp.Body))
		harness.Contains(tb, "8083: Calibre-Web serves the book by its author", fixtureAuthor, string(resp.Body))
	}
}

// breakAppDB is Python that writes, over Calibre-Web's settings database,
// a file that is no SQLite database, keeping its owner and mode.
const breakAppDB = `with open("/config/calibre-web/app.db", "w") as f:
    f.write("This is not a database.\n")
`

// treeCode is Python that prints every path under each directory sys.argv
// names, one to a line, in order: each directory, each symbolic link with
// its target, and each file with its size and its content's SHA-256.
const treeCode = `import hashlib, os, sys
for top in sys.argv[1:]:
    for root, dirs, files in os.walk(top):
        dirs.sort()
        for name in dirs:
            print("d", os.path.join(root, name))
        for name in sorted(files):
            p = os.path.join(root, name)
            if os.path.islink(p):
                print("l", p, os.readlink(p))
                continue
            with open(p, "rb") as f:
                print("f", p, os.path.getsize(p), hashlib.sha256(f.read()).hexdigest())
`

// tree lists st's volumes, each file by its size and content.
func tree(tb testing.TB, e harness.Engine, image string, st *store) string {
	tb.Helper()
	return rootPython(tb, e, image, treeCode, st.mounts(), booksDir, configDir)
}

// TestServeFailsCleanly is a start that cannot go on, and one that can.
// Calibre-Web's settings broken, as a file that is no SQLite database
// where a library and both apps' users are there already, stop the start
// within a minute, exit 1, with one line that names the file and says why,
// and nothing in /books or /config changed or left behind. A start that
// goes on writes the state file the health check reads.
func TestServeFailsCleanly(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)

	t.Run("a broken app.db", func(t *testing.T) {
		t.Parallel()
		tb := redacting(t)
		st, sec := firstThenStop(tb, e, image)
		rootPython(tb, e, image, breakAppDB, []harness.Mount{st.config})
		before := tree(tb, e, image, st)
		r := (&launch{mounts: st.mounts(), passwords: sec.all()}).refuse(tb, e, image)
		refused(tb, r, sec.all(), "/config/calibre-web/app.db", "file is not a database")
		harness.Equal(harness.NewChecks(tb), "nothing in /books or /config changes, and nothing is left behind", before, tree(tb, e, image, st))
	})

	t.Run("the state file", func(t *testing.T) {
		t.Parallel()
		tb := redacting(t)
		sec := newSecrets(tb)
		s := (&launch{env: sec.env(), mounts: append(newStore(tb, e).mounts(), sec.mount()), passwords: sec.all()}).serve(tb, e, image)
		states, err := appStates(tb.Context(), s.C)
		harness.NewChecks(tb).True(statePath+" says both apps are up",
			err == nil && len(states) == 2 && states[contentServer].Up && states[calibreWeb].Up, fmt.Sprint(states, err))
	})
}
