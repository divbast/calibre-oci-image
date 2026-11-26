// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

// Command divbast-calibre is the image's PID 1: it prepares and starts
// Calibre's Content Server and Calibre-Web, and is the image's health
// check, its version, and the calibredb that Calibre-Web runs.
//
// Usage:
//
//	divbast-calibre serve      first start, then the two apps
//	divbast-calibre health     check the page and /healthz, both on :8080
//	divbast-calibre version    print the version
//	divbast-calibre calibredb  run calibredb for Calibre-Web; a link named calibredb does the same
//
// serve's first start makes what is not there yet, and changes nothing
// that is: an empty Calibre library in /books; the Content Server's users
// in /config/calibre; Calibre-Web's settings and admin password in
// /config/calibre-web. The two admin passwords come from
// CALIBRE_ADMIN_PASSWORD and CALIBRE_WEB_ADMIN_PASSWORD, or from the files
// CALIBRE_ADMIN_PASSWORD_FILE and CALIBRE_WEB_ADMIN_PASSWORD_FILE name,
// and are read only while an account is still to be made. Then serve
// removes the four variables from its environment, so no app it starts
// inherits them. Starting the two apps is not written yet: serve stops
// there, with exit code 1.
//
// health prints nothing when both halves hold, and one "<check>: <reason>"
// line for each that does not.
//
// Exit codes:
//
//	0  healthy, or the version printed
//	1  a half of health did not hold, first start failed, or serve or
//	   calibredb is not written yet
//	2  the command line is wrong
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/divbast/calibre-oci-image/internal/firststart"
	"github.com/divbast/calibre-oci-image/internal/probe"
)

// version is set at build time, with -ldflags "-X main.version=…": the
// Calibre version the image is built from.
var version = "dev"

// cwCommit is set at build time, with -ldflags "-X main.cwCommit=…": the
// Calibre-Web commit the image is built from.
var cwCommit = "dev"

// The variables that hold the two admin passwords; each may instead be
// given as a file, named by the same variable with _FILE after it.
const (
	adminVar    = "CALIBRE_ADMIN_PASSWORD"
	webAdminVar = "CALIBRE_WEB_ADMIN_PASSWORD"
)

// passwordVariables is all four password variables.
var passwordVariables = []string{adminVar, adminVar + "_FILE", webAdminVar, webAdminVar + "_FILE"}

// layout is where first start finds the config and the library.
type layout struct {
	config, books string
}

// image is the image's layout.
var image = layout{config: "/config", books: "/books"}

// calibre is the Content Server's config directory.
func (l layout) calibre() string { return filepath.Join(l.config, "calibre") }

// calibreWeb is Calibre-Web's config directory.
func (l layout) calibreWeb() string { return filepath.Join(l.config, "calibre-web") }

// calibreWebSettings are the settings the image gives Calibre-Web on first
// start, for the library in books: Calibre's tools in /opt/calibre-bin,
// kepubify and unrar, its port on loopback, its log on stdout, and uploads
// allowed.
func calibreWebSettings(books string) *firststart.CalibreWebSettings {
	return &firststart.CalibreWebSettings{
		CalibreDir:      books,
		BinariesDir:     "/opt/calibre-bin",
		KepubifyPath:    "/usr/local/bin/kepubify",
		RarfileLocation: "/usr/local/bin/unrar",
		Port:            18083,
		Logfile:         "/dev/stdout",
		Uploading:       true,
	}
}

func main() {
	if filepath.Base(os.Args[0]) == "calibredb" {
		os.Exit(calibredb())
	}
	if len(os.Args) >= 2 && os.Args[1] == "calibredb" {
		os.Exit(calibredb())
	}
	if len(os.Args) != 2 {
		usage()
	}
	switch os.Args[1] {
	case "serve":
		os.Exit(serve())
	case "health":
		os.Exit(probe.Probe(context.Background(), "http://127.0.0.1:8080/", "http://127.0.0.1:8080/healthz", os.Stderr))
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: divbast-calibre serve|health|version|calibredb")
	os.Exit(2)
}

// serve runs first start in the image, until SIGTERM or SIGINT.
func serve() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return runServe(ctx, os.Environ(), firststart.Exec, image, os.Stdout, os.Stderr)
}

// runServe runs first start in l, with environ and run, then removes the
// password variables from this process's environment. Any failure is one
// line on stderr. Starting the two apps is not written yet.
func runServe(ctx context.Context, environ []string, run firststart.Runner, l layout, stdout, stderr io.Writer) int {
	say(stdout, "Calibre %s, Calibre-Web %s", version, cwCommit)
	if err := firstStart(ctx, environ, run, l, stdout); err != nil {
		say(stderr, "%v", err)
		return 1
	}
	if err := unsetPasswords(); err != nil {
		say(stderr, "%v", err)
		return 1
	}
	say(stderr, "first start is done, but starting Calibre and Calibre-Web is not written yet")
	return 1
}

// calibredb is Calibre-Web's calibredb, which is not written yet.
func calibredb() int {
	say(os.Stderr, "calibredb is not written yet")
	return 1
}

// firstStart makes what Calibre and Calibre-Web need before they first
// start, in l, and what is there already it leaves as it is. In order:
//
//   - /config and /books must be writable, and so must /config/calibre
//     and /config/calibre-web where they are there already, with the
//     files the apps write in each;
//   - each admin password is read and checked, from environ, while its
//     account is still to be made;
//   - /config/calibre and /config/calibre-web are made, mode 0700;
//   - an empty library, when /books holds none;
//   - the Content Server's users;
//   - Calibre-Web's settings and admin password.
//
// Each command first start runs gets environ without the password
// variables; the passwords go on stdin. It writes one line to stdout for
// each thing it makes, and stops at the first error, which it returns.
func firstStart(ctx context.Context, environ []string, run firststart.Runner, l layout, stdout io.Writer) error {
	env := lookup(environ)
	if err := writable(l); err != nil {
		return err
	}
	adminPW, err := password(env, adminVar, filepath.Join(l.calibre(), firststart.UsersFile))
	if err == nil && adminPW != "" {
		err = firststart.CheckCalibrePassword(source(env, adminVar), adminPW)
	}
	if err != nil {
		return err
	}
	webPW, err := password(env, webAdminVar, filepath.Join(l.calibreWeb(), firststart.AppDB))
	if err != nil {
		return err
	}
	for _, dir := range []string{l.calibre(), l.calibreWeb()} {
		if derr := firststart.EnsureDir(dir); derr != nil {
			return derr
		}
	}
	run = withEnv(run, withoutPasswords(environ))
	created, err := firststart.EnsureLibrary(ctx, run, l.books)
	if err != nil {
		return err
	}
	if created {
		say(stdout, "created an empty Calibre library in %s", l.books)
	}
	created, err = firststart.EnsureCalibreUsers(ctx, run, l.calibre(), adminPW, rand.Reader)
	if err != nil {
		return err
	}
	if created {
		say(stdout, "created the Content Server's users %s and %s in %s", firststart.AdminUser, firststart.InternalUser, filepath.Join(l.calibre(), firststart.UsersFile))
	}
	created, err = firststart.EnsureCalibreWeb(ctx, run, l.calibreWeb(), webPW, calibreWebSettings(l.books))
	if err != nil {
		return err
	}
	if created {
		say(stdout, "wrote Calibre-Web's first settings, and its %s's password, to %s", firststart.AdminUser, filepath.Join(l.calibreWeb(), firststart.AppDB))
	}
	return nil
}

// writable checks, before first start looks inside them, that the image's
// user can write /config, /books with its metadata.db, and each of
// /config/calibre and /config/calibre-web that is there, with the files
// the apps write in it. Either may be a symbolic link to a directory; a
// link to nothing, or to anything else, is refused.
func writable(l layout) error {
	if err := firststart.Writable(l.config); err != nil {
		return err
	}
	if err := firststart.Writable(l.books, firststart.MetadataDB); err != nil {
		return err
	}
	for _, d := range []struct {
		dir   string
		files []string
	}{
		{l.calibre(), []string{firststart.UsersFile, firststart.InternalPasswordFile}},
		{l.calibreWeb(), []string{firststart.AppDB}},
	} {
		// Stat follows a symbolic link, as Calibre and Calibre-Web will
		fi, err := os.Stat(d.dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if target, lerr := os.Readlink(d.dir); lerr == nil {
				return fmt.Errorf("%s is a symbolic link to %s, which is not there", d.dir, target)
			}
			continue
		case err != nil:
			return err
		case !fi.IsDir():
			return fmt.Errorf("%s is not a directory, or a symbolic link to one", d.dir)
		}
		if werr := firststart.Writable(d.dir, d.files...); werr != nil {
			return werr
		}
	}
	return nil
}

// password reads an admin password, but only when its account is still to
// be made: name is the variable that holds the password, or names, with
// _FILE after it, the file that does, and accounts is the file that holds
// the account once it is made. The password must follow Calibre-Web's
// default password rules. When accounts is there already, password reads
// nothing and returns "".
func password(env func(string) string, name, accounts string) (string, error) {
	_, err := os.Lstat(accounts)
	switch {
	case err == nil:
		return "", nil
	case !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("cannot tell whether %s exists: %w", accounts, err)
	}
	pw, err := firststart.ReadSecret(env, name)
	if err != nil {
		return "", err
	}
	if err := firststart.CheckPassword(source(env, name), pw); err != nil {
		return "", err
	}
	return pw, nil
}

// source is the variable a password came from: name, or name_FILE when
// that is set.
func source(env func(string) string, name string) string {
	if env(name+"_FILE") != "" {
		return name + "_FILE"
	}
	return name
}

// lookup returns a function that looks a variable up in environ. A
// variable set twice gives its first value, as Go's os.Getenv and C's
// getenv give it.
func lookup(environ []string) func(string) string {
	return func(key string) string {
		for _, e := range environ {
			if k, v, ok := strings.Cut(e, "="); ok && k == key {
				return v
			}
		}
		return ""
	}
}

// withoutPasswords returns environ without the password variables.
func withoutPasswords(environ []string) []string {
	return slices.DeleteFunc(slices.Clone(environ), func(e string) bool {
		k, _, _ := strings.Cut(e, "=")
		return slices.Contains(passwordVariables, k)
	})
}

// withEnv returns run, with base before the environment each command is
// given.
func withEnv(run firststart.Runner, base []string) firststart.Runner {
	return func(ctx context.Context, argv, env []string, stdin []byte) ([]byte, error) {
		return run(ctx, argv, append(slices.Clone(base), env...), stdin)
	}
}

// unsetPasswords removes the password variables from this process's
// environment, which every app it starts inherits.
func unsetPasswords() error {
	var err error
	for _, name := range passwordVariables {
		err = errors.Join(err, os.Unsetenv(name))
	}
	return err
}

// say writes one line to w, after the command's name.
func say(w io.Writer, format string, args ...any) {
	// A failed write to stdout or stderr leaves nowhere better to report it.
	_, _ = fmt.Fprintln(w, "divbast-calibre:", fmt.Sprintf(format, args...))
}
