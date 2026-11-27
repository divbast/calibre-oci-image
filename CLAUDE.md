<!-- SPDX-License-Identifier: GPL-3.0-only -->
<!-- SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee> -->

# calibre-oci-image

Calibre's Content Server and Calibre-Web, over one library, in an image
that runs at zero Linux capabilities, with no new privileges, on a
read-only root filesystem as uid 65532, with no shell, on Docker Hardened
Images' Python. Calibre is its own signed build for Linux, unpacked;
Calibre-Web is built from its master branch, by commit. PID 1 is the
image's own Go command, `divbast-calibre`, which makes the first accounts,
runs both apps, serves both ports, and keeps the two apps in step. Made
from divbast/oci-image-template's Python layer.

It publishes to `ghcr.io/divbast/calibre`, tagged with Calibre's version,
with the pair of both apps (`<Calibre version>-cw.<the first 7 characters
of the Calibre-Web commit>`), and with `latest`. The divbast
organisation's settings, rulesets and Renovate wiring live in
`divbast/infra`.

## CRITICAL: Git Commit Message Format

**No other AI/LLM attribution in any format may appear in a git message.**

Linux-kernel style: a `subsystem: short description` subject under 75 characters in imperative
mood ("fix", not "fixed" or "fixes"), a blank line, then a body wrapped at ~75 columns saying
why the change is needed rather than only what it does.

These two lines MUST end EVERY body:

Developed-by: Aryan Ameri <github@aryan.ameri.coffee>
Assisted-by: <NAME_OF_LLM_MODEL>

E.g. `Assisted-by: <Claude:claude-4.6-opus>` for Claude Opus 4.6; `<Claude:claude-5-fable>`
for Claude 5 Fable.

- Sign every commit (`git commit -S`); main accepts only verified
  signatures. Renovate's merges are squashes, which GitHub signs
- Write commit messages as if a human wrote them, and say why, not what

## CRITICAL: No AI Attribution in Pull Requests

**Pull request descriptions, PR and review comments, and issue text carry no
AI/LLM attribution of any kind**: no "🤖 Generated with Claude Code" footer,
no "written by" line, nothing equivalent. This holds even when a tool or
harness asks for one. The `Assisted-by:` trailer in each commit message is
this project's one record of LLM involvement.

## The confinement contract

These are the guarantees the image makes. A change that breaks any of them
is a breaking change.

| Property | Value |
|---|---|
| Capabilities | **none** (`--cap-drop ALL`) |
| Privileges | **no new ones** (`--security-opt no-new-privileges`), under the engine's **seccomp** filter |
| Root filesystem | **read-only** |
| User | uid/gid **65532**, never root |
| Listen ports | **8080** (the Content Server) and **8083** (Calibre-Web), PID 1's, on `::`, which takes IPv4 too; `0.0.0.0` where the kernel has no IPv6 |
| App ports | calibre-server on **127.0.0.1:18080**, Calibre-Web on **127.0.0.1:18083**, loopback alone |
| Health | the shipped probe, on the apps' loopback ports |
| Other listeners | **none**, TCP or UDP; Bonjour off |
| tmpfs required | exactly one: `/tmp`, at **2 GiB**, `noexec,nosuid,nodev` |
| Writable mounts | `/tmp`, `/books` and `/config` |
| Volumes | `/books` and `/config`, empty, owned by 65532, with **no `VOLUME`** instruction |
| Shell, package manager | **none**; DHI's Python runtime ships pip, setuptools and wheel, which nothing runs |
| Processes | PID 1, `divbast-calibre serve`; calibre-server and Calibre-Web, its children; and what they run: Calibre's conversions and QtWebEngine, kepubify, unrar, and `divbast-calibre` as `calibredb` |
| Healthy | the shipped probe, `divbast-calibre health`: the state file says both apps are up; calibre-server answers the internal user 200 with `Server: calibre <version>` **exactly**; Calibre-Web answers `/login` 200; 8080 and 8083 are listened on, on **every address** |
| Engine healthcheck | Docker runs it automatically. **Podman does not**: pass `--health-cmd` yourself |

- **First start makes what is missing, once, and never writes it again.**
  `serve` reads `TRUSTED_PROXIES` first (a bad entry: one line, exit 1).
  It checks that `/config`, `/books` with its `metadata.db`, and each of
  `/config/calibre` and `/config/calibre-web` that is there, with the
  files the apps write in it, are writable. It reads and checks each admin
  password while its account is still to be made: a byte-order mark
  anywhere, or a carriage return left once one `\n` or `\r\n` is taken off
  a file's end, stops the start, before Calibre-Web's rules; nothing is
  taken off quietly. Only then does it make,
  in order: `/config/calibre` and `/config/calibre-web` (0700); an empty
  library, by `calibredb --with-library /books list`, when `/books` holds
  no `metadata.db`; `users.sqlite`, with `admin` and the read-only
  `calibre-web`, whose password, 43 random characters, is in
  `internal-password` (0600), by `calibre-debug -e calibre_users.py`; and
  `app.db`, with admin's password and seven settings, by
  `calibre_web_init.py init` in Calibre-Web's venv. Each helper writes to
  a new temporary path that `serve` renames into place, so a failure
  leaves nothing half-written, and the next start tries again. Never add
  a setting it writes at every start: each app's own pages own them
- **No password on a command line, in a log, or in a child's
  environment.** The helpers get the passwords on stdin. `serve` unsets
  `CALIBRE_ADMIN_PASSWORD`, `CALIBRE_WEB_ADMIN_PASSWORD` and their
  `_FILE`s before it starts any child, and every child's environment is
  built without them. Errors name the variable or the path, never the
  value. A test that checks an environment reports names, never values
- **The children's argv is the contract's**: `/opt/calibre/calibre-server
  --listen-on 127.0.0.1 --port 18080 --enable-auth --userdb
  /config/calibre/users.sqlite --disable-use-bonjour /books`, and
  `/app/venv/bin/python -OO /app/calibre-web/cps.py -p
  /config/calibre-web/app.db -g /config/calibre-web/gdrive.db -i 127.0.0.1
  -r -o /dev/stdout`. Never `--enable-local-write` or `--trusted-ips`: the
  Content Server sees every request come from 127.0.0.1. `-r` turns on
  Calibre-Web's `/reconnect`, which PID 1 alone calls, and to which 8083
  answers 404. calibre-server alone gets `CALIBRE_TEMP_DIR=
  /config/calibre/tmp`, in place of the image's `/tmp`: an add through
  8080 leaves two or three copies there, and one more for a few seconds
  (a 300 MiB book: 1.2 GiB, then 900 MiB), and they add up, add after
  add, until calibre-server ends. The README says so, and points a large
  import to Calibre-Web, or to a container restart after it. The
  supervisor's `Before` empties that folder in place before each
  calibre-server start, first, crash and planned alike, and never while it
  runs, then sets it 0700. It never removes or replaces the folder, which
  may be a volume. An entry it cannot remove stays, with one log line that
  names it and gives `Writable`'s chown hint, and calibre-server starts.
  It refuses a symbolic link there, unfollowed, and a file, with one line
  each try: emptying where a link points could empty more than temporary
  files. Everything else keeps `/tmp`
- **The supervisor's times are part of the contract**: a child that ends
  on its own starts again after 1 s, doubling to 60 s, back to 1 s after
  10 minutes up. A stop gives each child 8 s after SIGTERM, then SIGKILL
  to its process group, and exit 1. PID 1 also reaps every orphan
- **The two apps stay in step through PID 1.** It polls `metadata.db`, its
  `-journal` and its `-wal` every 1 s, and puts each change down to the app
  with a request that can change the library open, or ended at most 10 s
  before, unless the app itself answered it 400 or more:
  `internal/reload/routes.go` lists those routes for each app, and a route
  it does not know counts as one. A request the app refused or failed
  changed nothing, so a POST with no login to a page Calibre-Web does not
  have, answered 404, never restarts calibre-server for an 8080 edit that
  follows. One the app sent no answer to, as when its client left first,
  still counts: the proxy gives `done` `proxy.NoAnswer`, not its own 502,
  which is not the app's answer. A change Calibre-Web made restarts
  calibre-server; one calibre-server made reconnects Calibre-Web; the
  writer is never reloaded. A change during an app's own start or
  reload is that app's own; one nothing explains reloads both. A reload
  waits for 5 s of quiet; a restart also waits until no 8080 request is
  open and no tracked conversion runs; neither waits more than 10 minutes.
  While a restart waits, 8080 refuses the requests that can change the
  library, but `POST /conversion/status/<id>`, with 503, `Retry-After: 10`
  and one line; while it runs, 8080 refuses every request. A planned
  restart counts as healthy for under 2 minutes of downtime. Every number
  here is part of the contract: a new one goes to the user first. In the
  second before PID 1's next look, an 8080 write after a Calibre-Web edit
  still goes through: PID 1 looks once a second, and in every test the
  library stayed whole. The probe can pass up to a second before 8080's
  gate opens after a restart: PID 1 opens it at its next 1 s look at
  calibre-server's port, and the probe asks that port directly. Neither
  gap lasts more than that second
- **Calibre-Web's own calibredb exports are held, not refused.** On
  8080, a request from a loopback peer, logged in by Digest as
  `calibre-web` (both signs, never one), is the wrapper's. When it is an
  export, `/cdb/cmd/export/{version}` by calibre's own parts (downloads
  and sends, and a KEPUB conversion's read, which PID 1 cannot tell from
  a download's), PID 1 looks at `metadata.db` at once, then holds it
  while a restart is to come or runs, and passes it on once the new
  calibre-server takes connections. A restart to come then skips its 5 s
  of quiet, but no sooner than `Options.Quiet` (5 s) after
  calibre-server's latest start, so bulk exports restart it at most once
  each 5 s; and it still waits until no other 8080 request is open.
  While a tracked conversion runs, the export is not held, and one held
  is let go when a conversion starts, so a held export never waits for a
  conversion. Without this a download right after a Calibre-Web edit
  answered 404 for 5.5 s, then came without its metadata, and a KEPUB
  conversion failed. In return for the wait, every download, send and
  KEPUB carries the book's current metadata. `show_metadata`, which
  Calibre's own conversions (not kepubify) read first, is not held while
  a restart is only to come, so such a conversion within about 5 s of an
  edit may use the old metadata
- **A wrapper request only breaks a tie with calibre-server's own
  start.** A change seen while one is open, or within 10 s after it,
  that no request of either app and no conversion can be the cause of,
  and that falls in calibre-server's own start, is Calibre-Web's, not
  the start's: Calibre-Web converts in the background, with no request
  of its own open, reading the book through `calibredb` first, and its
  write would otherwise fall in calibre-server's own start after the
  restart the read brought on, and be lost (a Kobo's first sync of 20
  EPUBs left the Content Server without the last KEPUBs). It counts for
  nothing else: a change during a mutating 8080 request, or within 10 s
  after one, is never Calibre-Web's for a wrapper request alone, so an
  8080 edit after a Calibre-Web download never restarts calibre-server.
  A first sync of 20 EPUBs takes 20 restarts, one each 5.1 s, 99 s in
  all, 0.21-0.31 s down each, and 86 of 471 reads through 8080 answered
  503 meanwhile
- **During a restart, any 8080 request from a loopback peer waits** for
  the new calibre-server, bounded by its own context, rather than getting
  503; `show_metadata` among them. `calibredb`'s first request of each
  call carries no login yet. A loopback peer is not only the image's own
  tools: in a Kubernetes pod, the pod's other containers and `kubectl
  port-forward` reach 127.0.0.1, and with host networking the host's
  programs do. Such a client waits out restarts too, and, sending Digest
  as `calibre-web` on an export, can bring forward a restart that is
  already due; it cannot call for one
- **8080 answers a body past calibre-server's own limit itself**: a
  `Content-Length` over 524,288,000 bytes, calibre-server's 500 MB
  default `--max-request-body-size`, which the image keeps, gets 413 with
  calibre-server's own words. calibre-server answers before it reads the
  body and closes, so through the proxy the client saw an empty 502 about
  one time in four. A chunked body goes on, for calibre-server to count
- **Forwarded headers come only from `TRUSTED_PROXIES`.** From a listed
  source PID 1 keeps `Forwarded`, `X-Forwarded-*`, `X-Real-IP`,
  `X-Scheme`, `X-Script-Name` and Calibre-Web's login header, in any
  spelling of the dashes, and appends the peer to `X-Forwarded-For`; from
  any other source it removes them and sets `X-Forwarded-For` to the peer.
  8080 removes the login header from everyone. Calibre-Web trusts
  127.0.0.1 for its header login by default, so the header's name is read
  again, by `calibre_web_init.py get-login-header`, after every request
  but GET and HEAD under `/admin/` on 8083, which is how Calibre-Web saves
  its settings (`POST /admin/ajaxconfig`). Unlisted sources wait
  meanwhile; a failed read gives them 503 until a read works, with at most
  one read a second, each capped at 10 s
- **The `calibredb` Calibre-Web runs is `divbast-calibre`**, through the
  `/opt/calibre-bin/calibredb` link. It takes Calibre-Web's three calls
  alone, in the exact form its code writes them (`--version`; `export …
  --with-library /books …`; `show_metadata --as-opf <id> --with-library
  /books`). It replaces the library with `http://127.0.0.1:8080/#books`,
  as user `calibre-web` with
  `--password '<f:/config/calibre/internal-password>'`, and execs
  Calibre's own `calibredb`. Any other call is refused, exit 2, in one
  line. Calibre lets one program open a library at a time, and
  calibre-server holds it
- **Health is each app's own answer, and where PID 1 listens.**
  calibre-server's login is Digest alone, and its version is in the
  `Server` header of an answer to a logged-in user, compared exactly with
  the three-part `CALIBRE_VERSION`. Calibre-Web names its version only
  behind a login, so only its status is read: the root is read-only, so
  the Calibre-Web that answers is the image's. Each app's check has 4 s,
  one after the other, inside the `HEALTHCHECK`'s 10 s timeout. Then 8080
  and 8083 must be in `/proc/net/tcp6` or `tcp` on every address. Each
  failure is one `state:`, `calibre-server:`, `calibre-web:` or `listen:`
  line: with no shell, the engine's health log is the only record of why
- **Podman never sees this image's `HEALTHCHECK`**, as for every divbast
  image published as an OCI index: users pass
  `--health-cmd '["/usr/local/bin/divbast-calibre","health"]'`, and the
  suite does the same under Podman
- **Nothing may add a shell or a package manager to the final image.**
  libc-bin, which ships `C.utf8`, adds `getconf`, `getent`, `iconv`,
  `ldd`, `locale`, `localedef`, `pldd`, `tzselect`, `zdump`, `iconvconfig`,
  `ldconfig` and `zic`: none is a shell, and `ldd` and `tzselect` are
  shell scripts that nothing can run
- **Zero egress the image adds.** Calibre-Web's updater is off at build,
  the Content Server's Bonjour is off, and Google Drive is not installed.
  What the apps reach beyond that is what a user sets up in them

### Measured facts

Each was measured on the built image, under the contract's confinement,
or read in upstream's source where the line says so:

- First start on empty volumes to healthy: 2.4 to 2.8 s on arm64, about
  5 s on amd64 under emulation; a later start 1.3 s on arm64. The
  `HEALTHCHECK`'s start period is 60 s. `docker stop` of an idle
  container: 0.4 to 1.4 s, exit 0. `divbast-calibre health` takes about
  8 ms
- The image: 2.34 GB on disk and 585 MB compressed on arm64, 2.37 GB and
  596 MB on amd64. Calibre's bundle is about 870 MiB of it
- `/tmp`, at 2 GiB: a 300 MiB add through 8080 grows
  it by under 16 MiB, its copies going to `/config/calibre/tmp`; a 300 MiB
  upload through 8083 holds 600 MiB of it, two copies, while Calibre-Web
  takes it, so 8083 takes about 1 GiB (1,100 MiB failed: "could not saved
  to temp dir"), and 8080 its own 500 MiB. A PDF cover takes about 25
  bytes of it a pixel, at 150 dpi: 1.2 GiB for a 3,500-point page; a
  3,900-point one fails to the generic cover. ImageMagick's own disk
  limit, from upstream's secure policy, is 2 GiB, as large as `/tmp`
- No `/config` mount: exit 1 at once, `Calibre and Calibre-Web cannot
  write /config: it is on a read-only file system: mount a writable volume
  or folder there, not a read-only one: open /config/.writable.…:
  read-only file system`; the same for `/books`, and for a `/books`
  mounted `:ro`. A `/books/metadata.db` owned by uid 1000: `… cannot write
  /books/metadata.db: give /books, and everything in it, to uid 65532 …`
- Calibre makes `CALIBRE_CONFIG_DIRECTORY` with mode 0755 when it is
  missing, so first start makes the two 0700 folders before any helper
  runs. `calibre-debug` writes `global.py.json` and `caches/` there; after
  a run it also holds `gui.json` and `plugins/`
- calibre-server stores its users' passwords in plain text, for Digest
  auth: `users.sqlite` and `internal-password` are 0600. `admin` may
  change its own password over HTTP; `calibre-web` may not (`403 The user
  calibre-web is not allowed to change passwords`), and a write as it
  gets `403 … does not have permission to make changes`
- `calibre-server --manage-users` without `--userdb` makes and changes
  `/config/calibre/server-users.sqlite`, which the server never reads. A
  user added with `--userdb /config/calibre/users.sqlite` logs in at once,
  with no restart. `add` reads the whole of stdin as the password, and
  Calibre refuses one that ends in a newline
- calibre-server's login is Digest only: Basic gets 400, and an anonymous
  request 401 with no `Server` header. The logged-in 200 says
  `Server: calibre 9.19.0`, while `calibredb --version` says `9.19`.
  Calibre-Web: `/login` 200, `/` 302, `/opds` 401, and no page without a
  login names a version
- Calibre-Web runs `calibredb` for three things alone: `export` (12
  arguments, for a download with metadata written in), `show_metadata`
  (5, before a conversion) and `--version`. It fails the export if
  `calibredb` writes anything to stderr; through the wrapper it writes
  nothing
- From upstream's source: calibre-server's routes that write the library
  are the 20 that Calibre flags `needs_db_write`.
  `/conversion/status/<id>` writes: the poll that first answers
  `running: false` adds the format. `/metadata-download/status` writes
  nothing. Calibre's page treats any status poll but a 200 as a failed
  conversion and stops polling, which is why the gate lets those polls
  through. calibre-server forgets a job 360 s after its last poll, opens
  the library at its first request, and has no background writer
- From upstream's source: on 8083, three GET routes write the library:
  Kobo sync and `/kobo_auth/generate_auth_token/<id>`, which convert
  EPUBs to KEPUB, and `/gdrive/watch/callback`. Calibre-Web's own Restart
  keeps its PID (`os.execv`); its Shutdown ends it, and the supervisor
  starts it again
- EPUB to PDF in the Content Server: 2.1 s on arm64 and 3.1 s on amd64,
  with no Mesa. Leaving Mesa, LLVM and z3 out saved 254 MB on disk and
  changed no result
- Chromium needs the image's `QTWEBENGINE_CHROMIUM_FLAGS="--disable-gpu
  --disable-namespace-sandbox --disable-dev-shm-usage"`. Under rootless
  Podman, without the second, its namespace sandbox starts and then
  aborts on `sys_chroot("/proc/self/fdinfo/")`; without the third, it
  aborts on the read-only `/dev/shm` that `--read-only-tmpfs=false`
  leaves. Any value without the first aborts PDF output everywhere
  ("GLOzone not found for unknown"): Qt turns the GPU off itself only
  while the variable is unset. With all three, each page-drawing process
  logs "Activated seccomp-bpf sandbox for process type: renderer" on
  both engines. Never `QTWEBENGINE_DISABLE_SANDBOX`: it turns Chromium's
  whole sandbox off, the seccomp-bpf filter with it
- Calibre 9.19.0's bundle carries Qt WebEngine 6.10.1, Chromium 134. Its
  browser thread can read the GPU thread's channel manager before that
  thread has made it (QTBUG-142720, QTBUG-142497; fixed in 6.10.2). When
  it does, the PDF worker ends with a segmentation fault just as Chromium
  starts to draw the pages. In the Content Server, the job's log ends at
  `Rendering 1 HTML files`, and calibre-server's traceback is `EOFError`
  in `simple_worker.py`. For Calibre-Web, only `ebook-convert`, which it
  runs, was measured: it ends with exit status 139 (SIGSEGV). Converting
  again works. Measured, the same runs as the README gives:
  - one CPU: 2 of 60 through 8080, 4 of 60 through the worker alone, and
    1 of 60 in `ebook-convert`;
  - four CPUs, the worker alone, one conversion at a time: 2 of 80;
  - 17 CPUs, eight containers at once: 2 of 160 through 8080, and 4, 6
    and 10 of 160 through the worker alone;
  - 17 CPUs, the worker alone, one conversion at a time: 0 of 80.

  It failed just as often in each of these cases, each tested on its own:
  `--disable-dev-shm-usage` left out, `--disable-namespace-sandbox` left
  out, the whole variable unset, `/tmp` without `noexec`, no contract
  confinement and no seccomp, and either engine
- `TestPDFOutputSandboxed` reads the bundle's Qt WebEngine version once
  a run. It converts again, once, only when that version is exactly
  6.10.1 and the conversion failed in exactly that way, and it says so in
  its log. A FATAL line of Chromium's after `Rendering 1 HTML files` is
  not that way. On any other version, older or newer, it never converts
  again. The seccomp-bpf check reads only the job log of the attempt that
  worked. The retry and the README's note go once the bundle's Qt
  WebEngine is 6.10.2 or newer. With them come a test that the version is
  6.10.2 or newer, and a test that PDF output works while the CPUs are
  busy
- Without libc-bin's `C.utf8`, Qt warned of a `C` locale at every
  calibre-server start; with it, never. Calibre names its folders and
  files in ASCII, and Calibre-Web in Unicode, whatever the locale; each
  app reads the other's
- Wand needs `MAGICK_HOME`; no library path is needed. An ImageMagick
  built to run `gs` as a program runs it through `popen`, that is
  `/bin/sh`, which the image does not have: hence Ghostscript linked in,
  `--with-gslib`. The policy matches a format's name, not its coder: a
  thumbnail written as `cover.jpg` needs `JPG` beside `JPEG`, and covers
  need `BMP`; PNM, TIF, XC, MIFF and INFO are not needed
- Calibre-Web without PyDrive2 starts and serves; its database settings
  page shows no Google Drive part, and nothing logs an error. Gmail's
  library still imports: googleapiclient copes with oauth2client being
  absent
- fontconfig logs `No writable cache directories` three times at each
  start. Calibre-Web warns that it finds no time zone, and uses UTC (the
  image has the zone data, and `TZ` works), and that flask-limiter keeps
  its counts in memory
- 485 ELF files on arm64, and 493 on amd64, find every library they need;
  the 19 named in `libcheck` are left out. 72 DHI packages are added, the
  same on both arches; `status.d` holds 113 entries, 41 of them the
  base's, and grype lists them as `pkg:deb` components

### The rules the build keeps

- **Every stage is a Docker Hardened Image, by digest**, verified by
  `divbast-verify-images` before anything builds, and every apt package a
  stage installs is DHI's: the toolchain stage deletes Debian's sources
  first, so apt fails on anything DHI does not build. Each package is
  pinned ` name=version \`, one a line, which Renovate's deb manager
  reads; its regex excludes `;`, so ` key=…; \` lines are not packages
- **Calibre's bundle must be signed by `keys/calibre-release.asc`**, the
  release key, fingerprint `3CE1780F78DD88DF45194FD706BC317B515ACE7C`,
  which must be the one primary key in the file. The bundle is held to
  `CALIBRE_SHA256_X86_64` or `CALIBRE_SHA256_ARM64` by curl and
  `sha256sum`: an `ADD` cannot choose its checksum by `TARGETARCH`, and
  `divbast-verify-images` refuses `FROM calibre-${TARGETARCH}`. Then
  `gpgv` must report `VALIDSIG` for that fingerprint. The tag
  `v<version>` must be signed by the same key (`git verify-tag`), and
  `bypy/sources.json` and `pyproject.toml` come from it, for the SBOM.
  **When Calibre changes its release key**, the build fails, naming it:
  check the new key from more than one source
  (`https://github.com/kovidgoyal.gpg`, calibre-ebook.com's signatures),
  export it into `keys/calibre-release.asc`, and change the fingerprint in
  the Containerfile, in one commit a person makes and signs
- **Calibre-Web is changed in two lines.** The `sed` of
  `UPDATER_AVAILABLE = True` must change exactly one line of
  `cps/constants.py`, and `NIGHTLY_VERSION[0]` must name
  `CALIBRE_WEB_COMMIT`. The `sed` of `cps/file_helper.py`'s
  `allowed_mimetypes.append(…)` must change exactly one line, so that a
  `.cbr` may also be `application/vnd.rar`: the type the image's libmagic
  gives a RAR archive, where Calibre-Web asks for `application/x-rar`
  alone and refused every genuine CBR. Its `test/` and `.github/` do not
  ship
- **The Python packages come from the locks alone**, as SABnzbd's: every
  file by sha256 with curl, then `--network=none`. Source releases build
  with `--no-build-isolation --check-build-dependencies`, from a build
  venv that never ships, which also takes setuptools, packaging,
  pycparser, and cffi and greenlet built first, since other builds import
  them; cryptography builds against DHI's OpenSSL (`OPENSSL_NO_VENDOR=1`).
  `pip check` must pass, the venv holds neither pip nor wheel, and
  `compileall` writes `-OO` bytecode with unchecked hashes
- **The runtime packages are unpacked, not installed.** Each `.deb` is
  `dpkg-deb -x`'d into the image, with its control file, from
  `dpkg-deb -f`, as `/var/lib/dpkg/status.d/<name>`, byte for byte DHI's
  own format. A package the runtime already ships fails the build, and so
  does any unmet dependency that `apt-get check` finds, but
  `libegl1 → libegl-mesa0` and `libglx0 → libglx-mesa0`. No maintainer
  script runs
- **`libcheck` proves the tree before it ships.** In a chroot of the
  final tree, the runtime's loader lists each ELF file's libraries, with
  `LD_TRACE_LOADED_OBJECTS` (and `LD_LIBRARY_PATH=/opt/calibre/lib` for
  the bundle's, as Calibre's launchers set), and any `not found` fails,
  but for the 19 named. Each of the 19 must be in the bundle, so a stale
  name fails too. No package file may replace a runtime file. Ghostscript
  must run, `magic` and `wand` must load, and `check_policy.py` proves the
  ImageMagick policy: a PDF's first page reads to a JPEG, and SVG, MSL,
  PS, EPS and writing a PDF raise `PolicyError`. Calibre itself cannot run
  there, since its launchers need `/proc`: the acceptance suite runs it
  first. The final stage takes the packages from this stage
- **Ghostscript, ImageMagick and unrar link to DHI's libraries**, and
  each must pass `check-elf`: PIE, `BIND_NOW`, `GNU_RELRO`, and no library
  that the runtime and the added packages do not ship. Ghostscript is
  built as a library (`make so`), with its own copies of DHI's libraries
  deleted, keeping its jbig2dec. ImageMagick has no modules, no programs
  and no OpenMP, and the build asserts `MAGICKCORE_GS_DELEGATE` and
  `MAGICKCORE_XML_DELEGATE`. kepubify is static Go, built offline, and
  must print its version
- **Each tool's source is checked**: ImageMagick and kepubify by commit,
  after which the tag must still name it; Ghostscript and unrar by the
  sha256 that their `ADD` holds
- **The SBOMs are written from the same ARGs and the signed tag's
  lists**, by `sbom/sboms.py`. A library or Python package its tables do
  not name fails the build, saying what to add. Both SBOMs are in
  `io.github.divbast.scan.sboms`; the bundle's alone is in
  `io.github.divbast.scan.sboms.advisory`. grype matches a Go component
  only when it is named by its module path, so kepubify's is
- **Every ARG group stays adjacent and in order**: `CALIBRE_VERSION`,
  `CALIBRE_SHA256_X86_64`, `CALIBRE_SHA256_ARM64`; `CALIBRE_WEB_COMMIT`,
  `CALIBRE_WEB_SHA256`; `IMAGEMAGICK_VERSION`, `IMAGEMAGICK_COMMIT`;
  `GHOSTSCRIPT_VERSION`, `GHOSTSCRIPT_TAG`, `GHOSTSCRIPT_SHA256`;
  `KEPUBIFY_VERSION`, `KEPUBIFY_COMMIT`; `UNRAR_VERSION`, `UNRAR_SHA256`.
  Renovate's managers read them that way. `IMAGE_PAIR` is
  `divbast-pins`'s to write, and `-check` holds it to the two apps'
  versions
- **hadolint forgets a stage's `SHELL` when an `ARG` or `COPY` comes
  between it and the `RUN`**: each stage puts `SHELL` right before its
  first `RUN`
- **The licences label names everything the image ships beyond the
  base**: this repository's, Calibre's and its bundle's, Calibre-Web's and
  each Python package's, the four tools', and every licence in the
  copyright file of each DHI package added. The bundle's and the Python
  packages' licences are tables in `sbom/sboms.py`, written from
  upstream, since `sources.json` carries none

## Python dependencies

- **`go run ./cmd/divbast-pins`** writes `python/requirements.lock`,
  `python/build.lock` and `python/files.txt` from what the repository
  pins: Calibre-Web's `requirements.txt` and `optional-requirements.txt`
  at `CALIBRE_WEB_COMMIT`, through `python/requirements.in`'s two `-r`
  lines, read for Linux and the Containerfile's DHI Python; and
  `python/pins.txt`. It fills in each checksum ARG that is empty
  (Calibre's two, Calibre-Web's, Ghostscript's and unrar's), and writes
  `IMAGE_PAIR`. `just lint` runs it with `-check`, which fails with a diff
  when a file is stale. It needs the network
- **A set checksum is never rewritten.** Renovate's update empties it,
  and the tool fills it from what the source serves then; a set one that
  the source no longer matches fails every run, naming both checksums
- **`python/pins.txt`** holds one `package==version` a line, each under a
  comment saying which kind it is: `# upstream leaves it unpinned: <its
  requirement>`, `# needed by <package> …`, or `# builds <packages> …`,
  for the build venv alone. `# leaves out: <why>` stands above a bare
  name, PyDrive2 and oauth2client, which neither lock nor `files.txt`
  then names; a `leaves out` for a package upstream's lists do not name
  fails, so a stale one is caught. A pin outside upstream's range fails,
  as does a pin for a package upstream pins with `==`. With PyDrive2 left
  out, pyOpenSSL and six were needed by nothing, and went: 144 packages
  are installed, 29 of them built from source
- **A yanked release is refused, unless upstream pins it with `==`**, as
  in SABnzbd's tool. A pure-Python package installs from its pure wheel,
  any other from its source release
- **A new minor of DHI's Python moves the Python the locks target**, and
  no post-upgrade task follows it: `-check` fails that PR, and a person
  runs the tool

## Dependency updates

Renovate, through divbast/ci's `renovate.yml`, opens a PR for each new
input and merges it once every check passes, while `CALIBRE_AUTOMERGE` is
`enabled`:

- Calibre's GitHub releases, `v<version>`; the update empties both
  checksums;
- Calibre-Web's master branch, through the git-refs datasource, each new
  commit as a new digest; the update empties `CALIBRE_WEB_SHA256`. Nothing
  signs the commits and no release age applies, so only the acceptance
  suite holds a broken commit back;
- ImageMagick's tags (`7.1.2-36`, read by a versioning of its own) and
  kepubify's (`v5.0.0`), each with its commit; kepubify through
  github-tags, since its releases give Renovate no commit;
- Ghostscript's releases, by their tag number, `gs10080`, from which the
  update writes `GHOSTSCRIPT_VERSION`, emptying the checksum;
- unrar's tarball, from RARLAB's `rar_add.htm`, betas included, with
  `UNRAR_SHA256` emptied: nothing shows that the first download is
  RARLAB's own;
- each line of `python/pins.txt`, from PyPI;
- the DHI images, the Python runtime and `-dev` grouped by the app
  preset, and the packages of the toolchain and runtimelibs stages,
  grouped here as `DHI build packages`.

A PR that moves Calibre, Calibre-Web, Ghostscript, unrar or a pin runs
`go run ./cmd/divbast-pins` as a post-upgrade task, which ci's
`RENOVATE_ALLOWED_COMMANDS` allows, so the locks, the checksums and
`IMAGE_PAIR` move with it. A new Calibre-Web whose requirements need a pin
that `pins.txt` does not have fails that task, naming the line to add.

## Where the shared parts live

- **divbast/ci**, checked out by `just setup` into `.ci/` at the one commit
  every workflow pins: the reusable workflows, the recipes the justfile
  imports, the Go tools in `.ci/bin/` (`divbast-verify-images`,
  `divbast-scan`, `divbast-testcount` and the rest), the acceptance
  `harness` and the `contract` suite, and `go/golangci.yml`. Its scan
  reads both SBOM labels, and lists the advisory SBOM's findings without
  blocking on them
- **divbast/renovate-config**, the presets `renovate.json` extends, beside
  this repository's own managers
- **divbast/infra**, the repository's settings, rulesets, `renovate`
  environment, `CALIBRE_AUTOMERGE`, and the DHI login

## Key commands

- `just setup`: divbast/ci into `.ci/`, then the toolchain from `mise.toml`
- `just build`: the image, `localhost/calibre:dev`. It does not verify the
  images it builds from; `just ci-tools && .ci/bin/divbast-verify-images`
  does, with `DHI_USERNAME` and `DHI_TOKEN` set
- `just acceptance`, `just test`: the suite, under `ENGINE`, through
  `divbast-testcount`
- `just lint`: every lint gate, with `python-lint`: ruff and ty over the
  Python in `firstrun/`, `sbom/` and `imagemagick/`, then
  `divbast-pins -check`. Needs Docker, logged in to dhi.io, for
  `docker build --check`, and the network
- `just archive`, `just scan`: the image as an OCI archive, then
  `divbast-scan` over it and the published `:latest`
- `go run ./cmd/divbast-pins`: the locks from the pins, the empty
  checksums, and `IMAGE_PAIR`
- `just ci`: every gate, locally. Run this before committing

## Code quality

- All linter gates are enforced as errors. Any suppression needs a
  justification beside it
- **The acceptance suite is Go, on divbast/ci's harness**: every
  container runs under the contract's confinement, with `/tmp` at 2 GiB,
  and with both passwords given as `_FILE`s, as test passwords that meet
  Calibre-Web's rules
- **Go is modern and idiomatic, on the standard library alone**, under
  ci's `go/golangci.yml`. The two first-start helpers are Python, run by
  Calibre's and Calibre-Web's own interpreters, under ruff and ty
- A test that has never failed proves nothing: when adding one, break what
  it guards and watch it fail
- REUSE-compliant SPDX headers on every file. This repository is
  **GPL-3.0-only**; the image's licences label lists what it ships
