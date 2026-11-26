// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

// Command divbast-calibre is the image's PID 1: it prepares and starts
// Calibre's Content Server and Calibre-Web, serves them on ports 8080 and
// 8083, and keeps the two in step. It is also the image's health check,
// its version, and the calibredb that Calibre-Web runs.
//
// Usage:
//
//	divbast-calibre serve      first start, then the two apps
//	divbast-calibre health     check both apps answer, and ports 8080 and 8083 are listened on
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
// inherits them.
//
// Then serve starts calibre-server on 127.0.0.1:18080 and Calibre-Web on
// 127.0.0.1:18083, and starts again either one that ends. It serves the
// Content Server on port 8080 and Calibre-Web on port 8083, on every
// address, keeping forwarding headers only from the sources
// TRUSTED_PROXIES lists. It watches the library's metadata.db, and after
// each change one app makes, it reloads the other. It writes each app's
// state to /tmp/divbast-calibre/state.json, for health. On SIGTERM or
// SIGINT it stops both apps and exits.
//
// health reads the state file, asks each app that is up to answer on its
// loopback port, and checks that ports 8080 and 8083 are listened on,
// on every address. The Content Server must answer 200 to the internal
// user, and name the Calibre version the image is built from. Calibre-Web
// must answer 200. An app in a planned restart for under 2 minutes is
// healthy. health prints nothing when all of it holds, and one
// "<check>: <reason>" line for each part that does not.
//
// calibredb is what Calibre-Web runs, as /opt/calibre-bin/calibredb. It
// sends the three calls Calibre-Web makes, --version, export and
// show_metadata, through the Content Server as its internal user, and
// refuses every other. Calibre's own calibredb replaces it.
//
// Exit codes:
//
//	0  healthy, the version printed, or serve stopped as asked
//	1  health did not hold; serve could not start, or an app had to be
//	   killed as it stopped; or Calibre's calibredb could not be run
//	2  the command line is wrong, or calibredb refused the call
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/divbast/calibre-oci-image/internal/calibredb"
	"github.com/divbast/calibre-oci-image/internal/firststart"
	"github.com/divbast/calibre-oci-image/internal/probe"
	"github.com/divbast/calibre-oci-image/internal/proxy"
	"github.com/divbast/calibre-oci-image/internal/reload"
	"github.com/divbast/calibre-oci-image/internal/supervise"
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

// trustedVar lists the sources whose forwarding headers are kept.
const trustedVar = "TRUSTED_PROXIES"

// Where the two apps listen, on loopback, and where serve writes their
// state for health.
const (
	contentServerAddr = "127.0.0.1:18080"
	calibreWebAddr    = "127.0.0.1:18083"
	statePath         = "/tmp/divbast-calibre/state.json"
)

// The image's times.
var (
	superviseOptions = supervise.Options{Min: time.Second, Max: time.Minute, ResetAfter: 10 * time.Minute, Grace: 8 * time.Second}
	reloadOptions    = reload.Options{Window: 10 * time.Second, Quiet: 5 * time.Second, Cap: 10 * time.Minute, RetryAfter: 10 * time.Second}
)

const (
	// watchEvery is how often serve looks at metadata.db, and at an app's
	// port after it starts.
	watchEvery = time.Second
	// loginTimeout is how long a read of Calibre-Web's login header
	// setting may take.
	loginTimeout = 10 * time.Second
	// readHeaderTimeout is how long a client has to send a request's
	// headers, as on every divbast server.
	readHeaderTimeout = 5 * time.Second
)

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
		os.Exit(runCalibredb(os.Args[1:]))
	}
	if len(os.Args) >= 2 && os.Args[1] == "calibredb" {
		os.Exit(runCalibredb(os.Args[2:]))
	}
	if len(os.Args) != 2 {
		usage()
	}
	switch os.Args[1] {
	case "serve":
		os.Exit(serve())
	case "health":
		os.Exit(health())
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

// serve runs the image, until SIGTERM or SIGINT.
func serve() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return runServe(ctx, os.Environ(), firststart.Exec, image, os.Stdout, os.Stderr, startApps)
}

// settings is what serve starts the apps with.
type settings struct {
	environ []string // PID 1's environment, as serve began
	trusted []netip.Prefix
	l       layout
	stderr  io.Writer
}

// runServe reads TRUSTED_PROXIES and runs first start in l, with environ
// and run, then removes the password variables from this process's
// environment, and runs apps. A failure before apps is one line on stderr.
func runServe(ctx context.Context, environ []string, run firststart.Runner, l layout, stdout, stderr io.Writer,
	apps func(context.Context, *settings) int,
) int {
	say(stdout, "Calibre %s, Calibre-Web %s", version, cwCommit)
	trusted, err := proxy.ParseTrusted(lookup(environ)(trustedVar))
	if err != nil {
		say(stderr, "%v", err)
		return 1
	}
	if err := firstStart(ctx, environ, run, l, stdout); err != nil {
		say(stderr, "%v", err)
		return 1
	}
	if err := unsetPasswords(); err != nil {
		say(stderr, "%v", err)
		return 1
	}
	return apps(ctx, &settings{environ: environ, trusted: trusted, l: l, stderr: stderr})
}

// startApps runs the two apps and their proxies, until ctx ends, in order:
// the supervisor, Calibre-Web's login header setting, the state file, the
// apps, the proxies, and the watch on the library. Its exit code is the
// supervisor's, or 1 when it could not start or a proxy stopped serving.
func startApps(ctx context.Context, s *settings) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	env := withoutPasswords(s.environ)
	sup := supervise.New(children(s.l, env), superviseOptions, nil)

	// From here on, every program PID 1 runs goes through the supervisor's
	// Exec, whose reaper collects them all
	read := &loggedRead{read: loginReader(sup.Exec, env, s.l), logw: s.stderr}
	login := proxy.NewLoginHeader(read.Read, loginTimeout, time.Now)
	if err := login.Load(ctx); err != nil {
		say(s.stderr, "%v", err)
		return 1
	}
	read.on.Store(true)

	apps := reload.New(reload.SystemClock(), &reloader{sup: sup, client: &http.Client{}, reconnect: "http://" + calibreWebAddr + "/reconnect"}, reloadOptions)
	defer apps.Stop()
	t := newTracker(ctx, statePath, apps, s.stderr)
	if err := t.write(); err != nil {
		say(s.stderr, "writing %s failed: %v", statePath, err)
		return 1
	}
	sup.OnChange(t.change)
	ran := make(chan int, 1)
	go func() { ran <- sup.Run(ctx) }()

	failed := make(chan struct{}, 2)
	if err := serveProxies(ctx, s, apps, login, failed); err != nil {
		say(s.stderr, "%v", err)
		cancel()
		<-ran
		return 1
	}
	go reload.Watch(ctx, libraryFiles(s.l), watchEvery, apps.DBChanged)

	code := 0
	select {
	case <-ctx.Done():
	case <-failed:
		code = 1
		cancel()
	}
	return max(code, <-ran)
}

// children are the two apps, each with env as its whole environment.
func children(l layout, env []string) []supervise.Child {
	return []supervise.Child{
		{
			Name: reload.ContentServer.String(),
			Argv: []string{
				"/opt/calibre/calibre-server", "--listen-on", "127.0.0.1", "--port", "18080", "--enable-auth",
				"--userdb", filepath.Join(l.calibre(), firststart.UsersFile), "--disable-use-bonjour", l.books,
			},
			Env: env,
		},
		{
			Name: reload.CalibreWeb.String(),
			Argv: []string{
				firststart.Python, "-OO", "/app/calibre-web/cps.py",
				"-p", filepath.Join(l.calibreWeb(), firststart.AppDB), "-g", filepath.Join(l.calibreWeb(), "gdrive.db"),
				"-i", "127.0.0.1", "-r", "-o", "/dev/stdout",
			},
			Env: env,
		},
	}
}

// libraryFiles are the files the library changes: metadata.db, and its
// journal and write-ahead log while SQLite writes.
func libraryFiles(l layout) []string {
	db := filepath.Join(l.books, firststart.MetadataDB)
	return []string{db, db + "-journal", db + "-wal"}
}

// loginReader reads Calibre-Web's login header setting with run, which
// runs Calibre-Web's helper with env. Its error names app.db, and ends
// with why the helper failed.
func loginReader(run firststart.Runner, env []string, l layout) func(context.Context) (string, error) {
	db := filepath.Join(l.calibreWeb(), firststart.AppDB)
	argv := []string{firststart.Python, firststart.WebHelper, "get-login-header", db}
	return func(ctx context.Context) (string, error) {
		out, err := run(ctx, argv, env, nil)
		if err != nil {
			return "", fmt.Errorf("cannot read Calibre-Web's login header setting from %s: %w", db, err)
		}
		return strings.TrimSpace(string(out)), nil
	}
}

// loggedRead is the read of Calibre-Web's login header setting that
// LoginHeader makes, which logs each failed read in one line once on is
// set: the reads after the first, at the start, whose failure the start
// reports itself. Without the line, an unlisted client would get 503s on
// 8083 with nothing in the log to say why. LoginHeader reads again after
// each admin change, and at most once a second while the name is unknown,
// so the lines cannot flood the log.
type loggedRead struct {
	read func(context.Context) (string, error)
	logw io.Writer
	on   atomic.Bool
}

// Read reads the setting once.
func (r *loggedRead) Read(ctx context.Context) (string, error) {
	name, err := r.read(ctx)
	if err != nil && r.on.Load() {
		say(r.logw, "%v", err)
	}
	return name, err
}

// serveProxies serves port 8080 for the Content Server and 8083 for
// Calibre-Web, until ctx ends. A proxy that stops serving before then
// sends to failed.
func serveProxies(ctx context.Context, s *settings, apps events, login *proxy.LoginHeader, failed chan<- struct{}) error {
	ports := []struct {
		port int
		app  reload.App
		cfg  proxy.Config
	}{
		{8080, reload.ContentServer, proxy.Config{Login: login, StripLoginAlways: true}},
		{8083, reload.CalibreWeb, proxy.Config{Login: login, BlockReconnect: true}},
	}
	servers := make([]*http.Server, 0, len(ports))
	for _, p := range ports {
		ln, err := proxy.Listen(ctx, p.port)
		if err != nil {
			for _, srv := range servers {
				_ = srv.Close()
			}
			return fmt.Errorf("listening on port %d failed: %w", p.port, err)
		}
		addr := contentServerAddr
		if p.app == reload.CalibreWeb {
			addr = calibreWebAddr
		}
		p.cfg.Target = &url.URL{Scheme: "http", Host: addr}
		p.cfg.Trusted = s.trusted
		p.cfg.Hooks = hooks(apps, p.app, s.stderr)
		srv := &http.Server{Handler: proxy.New(&p.cfg), ReadHeaderTimeout: readHeaderTimeout}
		servers = append(servers, srv)
		go func() {
			if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				say(s.stderr, "serving port %d failed: %v", p.port, err)
				failed <- struct{}{}
			}
		}()
	}
	go func() {
		<-ctx.Done()
		for _, srv := range servers {
			_ = srv.Close()
		}
	}()
	return nil
}

// events is what the proxies tell the Coordinator, and ask it.
type events interface {
	RequestStarted(app reload.App, method, path string) (done func(status int))
	Gate(app reload.App, method, path string) (deny bool, retryAfter time.Duration, reason string)
	Answered(method, path string, status int, header http.Header, body []byte, whole bool)
}

// hooks joins app's proxy to the Coordinator: each request is counted, and
// each failure the app gives is logged. On the Content Server's port,
// requests are also gated, and the answers about conversions read.
func hooks(apps events, app reload.App, logw io.Writer) proxy.Hooks {
	h := proxy.Hooks{
		Started: func(r *http.Request) func(int) { return apps.RequestStarted(app, r.Method, r.URL.Path) },
		Failed: func(r *http.Request, err error) {
			say(logw, "%s could not answer %s %s: %v", app, r.Method, logPath(r.URL.Path), err)
		},
	}
	if app != reload.ContentServer {
		return h
	}
	h.Gate = func(r *http.Request) (bool, time.Duration, string) { return apps.Gate(app, r.Method, r.URL.Path) }
	h.Observe = func(r *http.Request) func(int, http.Header, []byte, bool) {
		if !reload.Watched(r.Method, r.URL.Path) {
			return nil
		}
		method, path := r.Method, r.URL.Path
		return func(status int, header http.Header, body []byte, whole bool) {
			apps.Answered(method, path, status, header, body, whole)
		}
	}
	return h
}

// logPath is path as the log shows it: without the token a Kobo's
// requests, or a remote login, carry as their second part.
func logPath(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) > 2 && (parts[1] == "kobo" || parts[1] == "verify") && parts[2] != "" {
		parts[2] = "<token>"
	}
	return strings.Join(parts, "/")
}

// reloader reloads the two apps: Calibre-Web through its /reconnect, and
// calibre-server through the supervisor.
type reloader struct {
	sup       *supervise.Supervisor
	client    *http.Client
	reconnect string // Calibre-Web's /reconnect, on loopback
}

func (r *reloader) ReconnectCalibreWeb(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.reconnect, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close() // only the status matters
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", r.reconnect, resp.Status)
	}
	return nil
}

func (r *reloader) RestartContentServer(context.Context) error {
	return r.sup.Restart(reload.ContentServer.String())
}

// starts is what the tracker tells the Coordinator.
type starts interface {
	Started(app reload.App) (start int)
	Accepting(app reload.App, start int)
}

// tracker keeps the state file in step with the supervisor, and tells the
// Coordinator of each app's start, and of when its port then first
// accepts a connection.
type tracker struct {
	ctx    context.Context
	path   string
	apps   starts
	logw   io.Writer
	wait   func(ctx context.Context, addr string, every time.Duration) error
	states map[string]supervise.State
	polls  map[string]context.CancelFunc
}

func newTracker(ctx context.Context, path string, apps starts, logw io.Writer) *tracker {
	return &tracker{
		ctx: ctx, path: path, apps: apps, logw: logw, wait: reload.WaitAccepting,
		states: map[string]supervise.State{reload.ContentServer.String(): {}, reload.CalibreWeb.String(): {}},
		polls:  make(map[string]context.CancelFunc),
	}
}

// write writes the state file.
func (t *tracker) write() error {
	return supervise.WriteState(t.path, t.states)
}

// change is the supervisor's OnChange function.
func (t *tracker) change(name string, st supervise.State) {
	prev := t.states[name]
	t.states[name] = st
	if err := t.write(); err != nil {
		say(t.logw, "writing %s failed: %v", t.path, err)
	}
	if !st.Up || prev.Up {
		return
	}
	app, addr := reload.ContentServer, contentServerAddr
	if name == reload.CalibreWeb.String() {
		app, addr = reload.CalibreWeb, calibreWebAddr
	}
	start := t.apps.Started(app)
	if stop := t.polls[name]; stop != nil {
		stop()
	}
	ctx, stop := context.WithCancel(t.ctx)
	t.polls[name] = stop
	go func() {
		if t.wait(ctx, addr, watchEvery) == nil {
			t.apps.Accepting(app, start)
		}
	}()
}

// within bounds each app's check in health, inside the HEALTHCHECK's 10 s
// timeout, which holds two of them one after the other.
const within = 4 * time.Second

// plannedLimit is how long an app may be down in a planned restart and
// health still pass.
const plannedLimit = 2 * time.Minute

// health checks the image is healthy, and returns its exit code.
func health() int {
	return probe.Probe(context.Background(), &probe.Options{
		State:        statePath,
		ServerBase:   "http://" + contentServerAddr,
		WebBase:      "http://" + calibreWebAddr,
		PasswordFile: filepath.Join(image.calibre(), firststart.InternalPasswordFile),
		Version:      version,
		Within:       within,
		PlannedLimit: plannedLimit,
		Ports:        []int{8080, 8083},
		Proc:         "/proc",
	}, os.Stderr)
}

// runCalibredb runs Calibre's calibredb for the arguments Calibre-Web gave
// calibredb, replacing this process with it, with the environment
// unchanged.
func runCalibredb(args []string) int {
	return calibredb.Run(args, os.Environ(), syscall.Exec, os.Stderr)
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
		return slices.Contains(supervise.PasswordVariables[:], k)
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
	for _, name := range supervise.PasswordVariables {
		err = errors.Join(err, os.Unsetenv(name))
	}
	return err
}

// say writes one line to w, after the command's name.
func say(w io.Writer, format string, args ...any) {
	// A failed write to stdout or stderr leaves nowhere better to report it.
	_, _ = fmt.Fprintln(w, "divbast-calibre:", fmt.Sprintf(format, args...))
}
