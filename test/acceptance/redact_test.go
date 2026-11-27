// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode"

	"github.com/divbast/ci/harness"
)

// hidden is what a redactor writes in a password's place.
const hidden = "<password>"

// redactor is the testing.TB a test runs through, so that nothing it
// prints holds one of its passwords: each password it hides, and each of
// its pieces (see pieces), becomes <password> in every
// message, failure and skip, before the test's log has it. The harness
// prints through the same TB: its checks, and its dump of a container's log
// when the test fails, so a password the image leaked is never printed
// either.
type redactor struct {
	testing.TB

	mu        sync.Mutex
	passwords []string
}

// redacting returns a redactor for tb, hiding nothing yet.
func redacting(tb testing.TB) *redactor {
	tb.Helper()
	return &redactor{TB: tb}
}

// hide has r replace each of passwords from now on.
func (r *redactor) hide(passwords ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.passwords = append(r.passwords, passwords...)
}

// password returns a new password that follows the rules, hidden.
func (r *redactor) password() string {
	r.Helper()
	pw := password(r)
	r.hide(pw)
	return pw
}

// pieces are pw, and its parts between control and format characters (Cc
// and Cf), such as a tab or a byte-order mark: a test's output can quote a
// password, as %q does, and a program can drop such a character, and then
// only its parts stay as they were.
func pieces(pw string) []string {
	split := func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }
	return slices.DeleteFunc(slices.Concat([]string{pw}, strings.FieldsFunc(pw, split)),
		func(p string) bool { return p == "" })
}

// redact returns s with every hidden password, and each of its pieces,
// replaced by <password>, the longest first, so that no piece is left
// behind in what a longer one would have covered.
func (r *redactor) redact(s string) string {
	r.mu.Lock()
	all := make([]string, 0, 2*len(r.passwords))
	for _, pw := range r.passwords {
		all = append(all, pieces(pw)...)
	}
	r.mu.Unlock()
	slices.SortFunc(all, func(a, b string) int { return len(b) - len(a) })
	for _, p := range all {
		s = strings.ReplaceAll(s, p, hidden)
	}
	return s
}

// line is args as Log and the rest put them together.
func line(args ...any) string {
	return strings.TrimSuffix(fmt.Sprintln(args...), "\n")
}

func (r *redactor) Log(args ...any) {
	r.Helper()
	r.TB.Log(r.redact(line(args...)))
}

func (r *redactor) Logf(format string, args ...any) {
	r.Helper()
	r.TB.Log(r.redact(fmt.Sprintf(format, args...)))
}

func (r *redactor) Error(args ...any) {
	r.Helper()
	r.TB.Error(r.redact(line(args...)))
}

func (r *redactor) Errorf(format string, args ...any) {
	r.Helper()
	r.TB.Error(r.redact(fmt.Sprintf(format, args...)))
}

func (r *redactor) Fatal(args ...any) {
	r.Helper()
	r.TB.Fatal(r.redact(line(args...)))
}

func (r *redactor) Fatalf(format string, args ...any) {
	r.Helper()
	r.TB.Fatal(r.redact(fmt.Sprintf(format, args...)))
}

func (r *redactor) Skip(args ...any) {
	r.Helper()
	r.TB.Skip(r.redact(line(args...)))
}

func (r *redactor) Skipf(format string, args ...any) {
	r.Helper()
	r.TB.Skip(r.redact(fmt.Sprintf(format, args...)))
}

// recorder is a testing.TB that keeps what reaches its Log, Error and
// Fatal, and stops nothing: what a redactor passes on.
type recorder struct {
	testing.TB

	got []string
}

func (*recorder) Helper() {}

func (r *recorder) Log(args ...any) { r.got = append(r.got, fmt.Sprint(args...)) }

func (r *recorder) Error(args ...any) { r.got = append(r.got, fmt.Sprint(args...)) }

func (r *recorder) Fatal(args ...any) { r.got = append(r.got, fmt.Sprint(args...)) }

// TestRedactor: every way a test prints, its own messages, the harness's
// checks, and a quoted container log as the harness clips it, reaches the
// test's log with each hidden password, and each of its pieces, replaced:
// a part between control characters, and one without a byte-order mark.
// It needs no image.
func TestRedactor(t *testing.T) {
	t.Parallel()
	rec := &recorder{TB: t}
	r := redacting(rec)
	plain := r.password()
	tabbed := "Aa1!" + random(t, 12, lower) + "\t" + random(t, 12, upper)
	r.hide(tabbed)
	// A program that reads a password file may drop a byte-order mark
	marked := "\uFEFFAa1!" + random(t, 20, lower)
	r.hide(marked)
	unmarked := strings.TrimPrefix(marked, "\uFEFF")
	log := "divbast-calibre: started\n" + plain + "\nthe password: " + tabbed + "\n"

	r.Logf("read %s", plain)
	r.Log("read", tabbed)
	r.Logf("read %s, without its mark", unmarked)
	r.Errorf("its log: %s", harness.Clip(log))
	r.Fatalf("the log of container c:\n%s", log)
	harness.NewChecks(r).True("a check", false, "found "+plain)
	harness.Contains(r, "the log holds it", "absent", log)

	got := strings.Join(rec.got, "\n")
	for _, p := range slices.Concat(pieces(plain), pieces(tabbed), []string{unmarked}) {
		if strings.Contains(got, p) {
			t.Errorf("what reached the log holds a piece of a hidden password, %d bytes long", len(p))
		}
	}
	if n := strings.Count(got, hidden); len(rec.got) != 7 || n < 9 {
		t.Errorf("got %d messages holding %s %d times, want 7 messages and it at least 9 times", len(rec.got), hidden, n)
	}
	if !strings.Contains(got, "divbast-calibre: started") {
		t.Errorf("what is no password is gone too: %q", got)
	}
}
