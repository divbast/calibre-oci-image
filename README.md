<!-- SPDX-License-Identifier: GPL-3.0-only -->
<!-- SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee> -->

# calibre-oci-image

[Calibre](https://calibre-ebook.com)'s Content Server and
[Calibre-Web](https://github.com/janeczku/calibre-web), two web interfaces
to one Calibre e-book library, in one image that runs at zero Linux
capabilities, with no new privileges, on a read-only root filesystem as a
non-root user, with no shell in it. Calibre comes as its own signed build
for Linux, and Calibre-Web is built from its source. The image goes
through divbast/ci's shared pipeline: lint, a build for amd64 and arm64,
the confinement contract and its own acceptance suite under Docker and
rootless Podman, an image scan, and a daily publish to
`ghcr.io/divbast/calibre`. [Publishing](#publishing) says when it
publishes and under which tags.

The image follows every Calibre release, and each new commit on
Calibre-Web's master branch.

## Base

The image runs on Docker Hardened Images'
[`python`](https://hub.docker.com/hardened-images/catalog) for Debian 13,
with no shell and no package manager. That image ships pip, setuptools and
wheel beside Python, and this one keeps them: nothing here runs them, and
Calibre-Web's packages are in a venv of their own, with no pip in it.
Every stage of the build is a Docker Hardened Image too, and CI refuses to
build until Docker's signature on each is verified. Every package the
build stages install comes from Docker Hardened Images' own Debian
repository alone.

- `calibre` downloads Calibre's own build for Linux, the one for the
  image's architecture, from download.calibre-ebook.com. It checks the
  file against the sha256 the Containerfile names, then checks that it is
  signed by Calibre's release key, the one in `keys/`, fingerprint
  `3CE1780F78DD88DF45194FD706BC317B515ACE7C`, and no other. It also
  checks the signature on Calibre's tag for that version, with the
  same key, and takes from the tag the lists of the libraries and Python
  packages Calibre's build holds, for the SBOM.
- `calibreweb` takes Calibre-Web at the full commit the Containerfile's
  `CALIBRE_WEB_COMMIT` names, from the archive GitHub makes of it, held to
  its sha256: Calibre-Web signs neither its tags nor its commits. It
  changes two lines of Calibre-Web, and the build fails unless each
  change is exactly its one line: the line that sets Calibre-Web's own
  updater, which it turns off, and the line that lists the types an
  upload may have, which then lets a `.cbr` be `application/vnd.rar`, the
  type the image's libmagic gives a RAR archive, so that a CBR comic can
  be uploaded.
- `deps` builds Calibre-Web's Python packages: every one that its
  `requirements.txt` and `optional-requirements.txt` name, but Google
  Drive's two, as [What the image turns off](#what-the-image-turns-off)
  says. Each file is fetched first, by the sha256 that `python/files.txt`
  names, and checked; then, with no network, the pure-Python packages are
  installed from their wheels, and the ones with C or Rust code are built
  from source, against the image's own OpenSSL and libraries. `pip check`
  then holds every package to what it requires. Calibre-Web serves its
  pages through gevent, one of those packages.
- `runtimelibs` adds the system libraries that Calibre's build,
  Calibre-Web and the tools below need beyond the base's own: 72 packages
  from Docker Hardened Images' repository, each pinned. The runtime has no
  dpkg, so each package's entry goes to `/var/lib/dpkg/status.d`, where
  the base lists its own packages and the scan reads them.
- `ghostscript` and `imagemagick` build Ghostscript and ImageMagick, with
  which Calibre-Web makes covers and thumbnails, a PDF's first page among
  them. `unrar` builds RARLAB's unrar, with which Calibre-Web reads CBR
  comics. All three link to the base's libraries and the packages above,
  so Docker's updates of those reach them, and each is hardened and
  checked: position-independent, bound at load, with a read-only
  relocation table. `kepubify` builds kepubify, with which Calibre-Web
  makes books for Kobo e-readers, as a static Go program. ImageMagick and
  kepubify are fetched by commit, and the build checks that their release
  tag still names it; Ghostscript and unrar come as tarballs, held to the
  sha256 the Containerfile names.
- `sbom` writes two SBOMs, which the scan reads: see
  [The scan](#the-scan).
- `probe` builds `divbast-calibre`, the image's own command and PID 1, a
  static Go binary on the standard library alone.
- `libcheck` puts the image's files together and checks that each program
  and library in them finds every library it needs. It leaves out, by
  name, 19 files of Calibre's build that need a library neither that
  build nor Docker Hardened Images ships. Each of the 19 is for a screen,
  a device or speech, which a server does not have. It then runs
  Ghostscript, loads ImageMagick, and proves ImageMagick's policy, as
  [ImageMagick](#imagemagick) says.

Calibre is never patched. Calibre-Web is changed in those two lines
alone.

## Container Features

| Property | Value |
|---|---|
| Capabilities | none (`--cap-drop ALL`) |
| Privileges | no new ones (`--security-opt no-new-privileges`), under the engine's seccomp filter |
| Root filesystem | read-only |
| User | uid/gid 65532 (`nonroot`) |
| Ports | 8080, Calibre's Content Server, and 8083, Calibre-Web, on IPv4 and IPv6. The two apps themselves listen on 127.0.0.1 alone, on 18080 and 18083, and nothing else listens |
| tmpfs required | `/tmp`, which nothing can run from (`--tmpfs /tmp:rw,noexec,nosuid,nodev,size=2g`); see [`/tmp`](#tmp) |
| Volumes | `/books`, the library, and `/config`, both apps' settings, which you mount, writable by 65532 |
| Processes | PID 1, `divbast-calibre`, which runs the Content Server (`calibre-server`) and Calibre-Web, and the programs they run |
| Shell in image | none |
| Package manager | none |
| Outbound network | what you set up in either app, such as e-mail or a metadata search; Calibre-Web's updater is off |
| Healthy | the shipped probe: each app answers on its loopback port, the Content Server with this image's Calibre version, and 8080 and 8083 are listened on, on every address |

## Running

Pull `ghcr.io/divbast/calibre:latest`, which follows Calibre's newest
release and Calibre-Web's newest commit. `:9.19.0` is the newest image
with Calibre 9.19.0, and `:9.19.0-cw.79d3571` the newest with that Calibre
and that Calibre-Web commit, named by its first seven characters. The
package page on ghcr.io lists the tags. To build it yourself:
`docker login dhi.io`, since every image the build starts from is Docker
Hardened, then `just setup`, then `ENGINE=podman just build`, which tags
it `localhost/calibre:dev`.

Every example below:

- gives the two admin passwords as files, from the engine's secrets, and
  never on a command line;
- mounts the library at `/books` and the settings at `/config`;
- gives `/tmp` 2 GiB, as [`/tmp`](#tmp) says;
- publishes both ports on the host's loopback alone;
- sets `TRUSTED_PROXIES` to `192.0.2.10`, which stands for your reverse
  proxy's address, as
  [Behind a reverse proxy](#behind-a-reverse-proxy) says. Leave it out
  when no proxy is in front of the image.

Each password file holds the password and nothing else; one newline at
its end is allowed. [First start](#first-start) says what each password is
for, and the rules both must follow.

### Podman

```bash
podman secret create calibre-admin-password ./calibre-admin-password
podman secret create calibre-web-admin-password ./calibre-web-admin-password
podman run -d --name calibre \
  --cap-drop ALL --read-only --read-only-tmpfs=false \
  --security-opt no-new-privileges \
  --user 65532:65532 \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=2g \
  --secret calibre-admin-password \
  --secret calibre-web-admin-password \
  -e CALIBRE_ADMIN_PASSWORD_FILE=/run/secrets/calibre-admin-password \
  -e CALIBRE_WEB_ADMIN_PASSWORD_FILE=/run/secrets/calibre-web-admin-password \
  -e TRUSTED_PROXIES=192.0.2.10 \
  -v calibre-books:/books \
  -v calibre-config:/config \
  -p 127.0.0.1:8080:8080 \
  -p 127.0.0.1:8083:8083 \
  --health-cmd '["/usr/local/bin/divbast-calibre","health"]' \
  ghcr.io/divbast/calibre:latest
```

Podman mounts each secret at `/run/secrets/<name>`, the file the `_FILE`
variables name. `--read-only-tmpfs=false` and `--health-cmd` are Podman's
alone. Without the first, Podman mounts writable tmpfs of its own at
`/run`, `/var/tmp` and elsewhere beside the read-only root; without the
second, it never runs the image's health check, as [Health](#health) says.

As a Quadlet, in `~/.config/containers/systemd/calibre.container`:

```ini
[Container]
Image=ghcr.io/divbast/calibre:latest
User=65532
Group=65532
DropCapability=ALL
NoNewPrivileges=true
ReadOnly=true
ReadOnlyTmpfs=false
Tmpfs=/tmp:rw,noexec,nosuid,nodev,size=2g
Secret=calibre-admin-password
Secret=calibre-web-admin-password
Environment=CALIBRE_ADMIN_PASSWORD_FILE=/run/secrets/calibre-admin-password
Environment=CALIBRE_WEB_ADMIN_PASSWORD_FILE=/run/secrets/calibre-web-admin-password
Environment=TRUSTED_PROXIES=192.0.2.10
Volume=calibre-books:/books
Volume=calibre-config:/config
PublishPort=127.0.0.1:8080:8080
PublishPort=127.0.0.1:8083:8083
HealthCmd=["/usr/local/bin/divbast-calibre","health"]

[Install]
WantedBy=default.target
```

### Docker and Compose

Docker takes the Podman command without `--read-only-tmpfs=false` and
`--health-cmd`, and runs the image's `HEALTHCHECK` itself. `docker run`
has no secrets outside Swarm, so in place of the two `--secret` lines,
mount the folder that holds the two files, read-only:
`-v /srv/calibre/secrets:/run/secrets:ro`, with the files named as the
`_FILE` variables say. With Compose:

```yaml
services:
  calibre:
    image: ghcr.io/divbast/calibre:latest
    user: "65532:65532"
    read_only: true
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    environment:
      CALIBRE_ADMIN_PASSWORD_FILE: /run/secrets/calibre-admin-password
      CALIBRE_WEB_ADMIN_PASSWORD_FILE: /run/secrets/calibre-web-admin-password
      TRUSTED_PROXIES: 192.0.2.10
    secrets:
      - calibre-admin-password
      - calibre-web-admin-password
    tmpfs:
      - /tmp:rw,noexec,nosuid,nodev,size=2g
    volumes:
      - calibre-books:/books
      - calibre-config:/config
    ports:
      - "127.0.0.1:8080:8080"
      - "127.0.0.1:8083:8083"
secrets:
  calibre-admin-password:
    file: ./secrets/calibre-admin-password
  calibre-web-admin-password:
    file: ./secrets/calibre-web-admin-password
volumes:
  calibre-books:
  calibre-config:
```

Compose mounts each secret's file as it is, at `/run/secrets/<name>`, and
does not change its owner or its mode, so the file on the host must be
readable by 65532: as root, `chown 65532:65532 secrets/*` and
`chmod 0400 secrets/*`. The same holds for the files in the folder that
`docker run` mounts.

### Kubernetes

Make the Secret from the two files first, so that no password is written
in the manifest:

```bash
kubectl create secret generic calibre-passwords \
  --from-file=calibre-admin-password=./calibre-admin-password \
  --from-file=calibre-web-admin-password=./calibre-web-admin-password
```

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: calibre
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels: {app: calibre}
  template:
    metadata:
      labels: {app: calibre}
    spec:
      securityContext:
        runAsUser: 65532
        runAsGroup: 65532
        runAsNonRoot: true
        fsGroup: 65532
        fsGroupChangePolicy: OnRootMismatch
        seccompProfile: {type: RuntimeDefault}
      containers:
        - name: calibre
          image: ghcr.io/divbast/calibre:latest
          env:
            - name: CALIBRE_ADMIN_PASSWORD_FILE
              value: /run/secrets/calibre/calibre-admin-password
            - name: CALIBRE_WEB_ADMIN_PASSWORD_FILE
              value: /run/secrets/calibre/calibre-web-admin-password
            - name: TRUSTED_PROXIES
              value: 192.0.2.10
          ports:
            - {name: content, containerPort: 8080}
            - {name: web, containerPort: 8083}
          securityContext:
            readOnlyRootFilesystem: true
            allowPrivilegeEscalation: false
            capabilities: {drop: [ALL]}
          volumeMounts:
            - {name: books, mountPath: /books}
            - {name: config, mountPath: /config}
            - {name: tmp, mountPath: /tmp}
            - {name: passwords, mountPath: /run/secrets/calibre, readOnly: true}
          startupProbe:
            exec:
              command: ["/usr/local/bin/divbast-calibre", "health"]
            periodSeconds: 2
            timeoutSeconds: 10
            failureThreshold: 30
          livenessProbe:
            exec:
              command: ["/usr/local/bin/divbast-calibre", "health"]
            periodSeconds: 30
            timeoutSeconds: 10
            failureThreshold: 3
      volumes:
        - name: books
          persistentVolumeClaim: {claimName: calibre-books}
        - name: config
          persistentVolumeClaim: {claimName: calibre-config}
        - name: tmp
          emptyDir: {medium: Memory, sizeLimit: 2Gi}
        - name: passwords
          secret:
            secretName: calibre-passwords
            defaultMode: 0440
---
apiVersion: v1
kind: Service
metadata:
  name: calibre
spec:
  selector: {app: calibre}
  ports:
    - {name: content, port: 8080, targetPort: content}
    - {name: web, port: 8083, targetPort: web}
```

- `fsGroup` gives the volumes and the Secret's files to group 65532,
  which the image runs as, so the apps can write the volumes and read the
  passwords. `defaultMode: 0440` lets that group read the passwords, and
  no one else.
- One replica, and `Recreate`: two Content Servers must never open one
  library at once.
- An `emptyDir` cannot be mounted `noexec`, as the `--tmpfs` above is.
  With `medium: Memory`, what `/tmp` holds counts as the pod's memory.
- The probes run the image's own health check, `divbast-calibre health`,
  as Docker and Podman do; see [Health](#health).
- `TRUSTED_PROXIES` stands, as in the other examples, for the address
  that the Ingress controller's pod connects from, as this pod sees it.
  List that address, or the addresses of the controller's pods, and not
  the whole pod network, such as k3s's `10.42.0.0/16`: with
  [Calibre-Web's reverse-proxy login](#behind-a-reverse-proxy) on, every
  pod in a listed range could log in to Calibre-Web as any user. A pod's
  address can change each time the pod is made again: change the list
  then.

### First start

On each start, PID 1 makes what is not there yet, and changes nothing
that is. On a first start, on empty volumes, it makes:

- an empty Calibre library in `/books`, when `/books` holds no
  `metadata.db`;
- the Content Server's users, in `/config/calibre/users.sqlite`: `admin`,
  with the password `CALIBRE_ADMIN_PASSWORD` gives, and `calibre-web`, a
  read-only user of the image's own, whose random password is in
  `/config/calibre/internal-password`, mode 0600;
- Calibre-Web's settings, in `/config/calibre-web/app.db`: its `admin`,
  with the password `CALIBRE_WEB_ADMIN_PASSWORD` gives, and the settings
  the image needs, which
  [Calibre-Web's first settings](#calibre-webs-first-settings) lists.
  Calibre-Web's own default password, `admin123`, never works: the image
  writes yours before Calibre-Web first starts.

The two passwords come from:

- `CALIBRE_ADMIN_PASSWORD`, or `CALIBRE_ADMIN_PASSWORD_FILE` naming a file
  that holds it: the Content Server's `admin`, on port 8080;
- `CALIBRE_WEB_ADMIN_PASSWORD`, or `CALIBRE_WEB_ADMIN_PASSWORD_FILE`:
  Calibre-Web's `admin`, on port 8083.

Each is needed only while its account is still to be made. The start
refuses both forms of one set at once, an empty value, and an empty file.
One newline at a file's end, `\n` or `\r\n`, is not part of the password.
Both passwords must follow Calibre-Web's default password rules, and two
more, for what an editor can leave in a file without showing it:

- at least 8 characters;
- a digit, a lower-case letter and an upper-case letter;
- a character that is not a letter, not white space and not a digit 0-9,
  such as `!` or `#`;
- no line break;
- no byte-order mark (U+FEFF), which some editors write at the start of a
  file;
- no carriage return (`\r`), but in the one `\r\n` that may end a file.

The start never takes such a character off, so the password is always the
one you wrote.

The Content Server's password must also hold no control character and no
invisible formatting character, which Calibre refuses. A password that
breaks a rule stops the start at once, exit 1, before anything is
written, with one line that names the variable and the rule, and never
the password:

```text
divbast-calibre: CALIBRE_ADMIN_PASSWORD_FILE: the password needs at least one digit (both admin passwords must follow Calibre-Web's default password rules)
```

Once the accounts exist, PID 1 changes nothing in their files and reads
neither variable: you can remove both, and the secrets, after the first
start. To change a password later, use each app: the Content Server's
[users](#the-content-servers-users), and Calibre-Web's own admin pages.
The passwords never appear on a command line, in a log, or in the
environment of a program PID 1 starts: PID 1 gives each one to the program
that writes it on its standard input, and removes the four variables from
its own environment before it starts the apps.

When measured on arm64, a first start was healthy in 2.4 to 2.8 seconds,
and a later start in 1.3 seconds. A stop (SIGTERM) gives each app 8
seconds to end before PID 1 kills it; an idle container stopped in under 2
seconds, with exit 0.

### `/books` and `/config`

The image carries an empty `/books` and an empty `/config`, both owned by
65532, and declares no `VOLUME`. A new named volume takes its owner from
the folder on first mount, so `-v calibre-books:/books` just works.

- `/books` is the Calibre library: the folder that holds `metadata.db` and
  a folder for each author. An empty one gets a new, empty library.
- `/config` holds `calibre/`, the Content Server's users and Calibre's own
  settings, and `calibre-web/`, Calibre-Web's settings, its key and its
  cache. The image makes both folders mode 0700.
- A `/books` or a `/config` that 65532 cannot write, such as a bind mount
  that root made, or a library a desktop Calibre left owned by your own
  user, stops the start at once, exit 1, with one line naming the folder
  or the file, and uid 65532. Give the folder, and everything in it, to
  65532, instead of running the image as root: `chown -R 65532:65532
  <dir>` as root, or under rootless Podman
  `podman unshare chown -R 65532:65532 <dir>`. For a Podman bind mount,
  `:U` does the same at each start, as in `-v ./books:/books:U`, which
  takes longer the larger the library is.
- On an SELinux host, such as Fedora or RHEL, a bind mount also needs
  Podman to relabel it, or the container cannot use it: add `:Z`, as in
  `-v ./books:/books:Z`. `:Z` keeps the folder to this container alone.
- A `/books` or a `/config` mounted read-only, as `:ro` mounts it, or
  with nothing mounted there, where it is part of the image's read-only
  root, stops the start at once, exit 1, with one line naming the folder
  and saying that it is on a read-only file system. No owner can write
  there: mount a writable volume or folder at that path.

### A library from a desktop Calibre

Stop the desktop Calibre, and keep a copy of the library's folder. Give
the folder to 65532, as above, and mount it at `/books`. Never open
`/books` with another Calibre program while the image runs: Calibre allows
one program to open a library at a time.

**A newer Calibre may upgrade a library once, one way.** The first time a
newer Calibre opens a library, it may upgrade the library's database, and
an older Calibre may then not open it again. That holds for a library from
a desktop Calibre older than the image's, and for each new image that
brings a newer Calibre. Back up `/books` before the image first opens a
library, and before you move to a new image.

### `/tmp`

Calibre-Web, and the Calibre programs it runs, keep their temporary files
in `/tmp`, Calibre's cache is there, through `CALIBRE_CACHE_DIRECTORY`,
and PID 1 keeps its state file there, `/tmp/divbast-calibre/state.json`.
The examples give it 2 GiB. A tmpfs keeps its files in
memory, so what `/tmp` holds counts as the container's memory.

The Content Server keeps its own temporary files in
`/config/calibre/tmp` instead, through `CALIBRE_TEMP_DIR`. Each book
added through it leaves two or three copies of itself there, and one
more for a few seconds while it is added. The Content Server deletes
them only when it stops, so they add up, book after book, until its next
restart: after any change made in Calibre-Web, or when the container
restarts. PID 1 empties that folder before each start of the Content
Server, and never while it runs. So a large import goes better through
Calibre-Web, on port 8083, or with a restart of the container after it.
When `/config` is full, Calibre-Web cannot save its settings either.

In the image's measurements:

- a 300 MiB book added through port 8080 grew `/tmp` by under 16 MiB, and
  left 900 MiB in `/config/calibre/tmp` until the Content Server's next
  restart;
- the same book uploaded through port 8083 held 600 MiB of `/tmp` at the
  most, for two copies, while Calibre-Web took it, and nothing after;
- Calibre-Web's cover of a PDF uses `/tmp` too, about 25 bytes for each
  pixel of the first page at 150 dots an inch: 1.2 GiB for a page 49
  inches square. ImageMagick's own policy lets it write up to 2 GiB of
  temporary files there.

### Uploads

- **Port 8080**, the Content Server, takes a book of up to 500 MiB, its
  own limit, `--max-request-body-size`, which the image keeps. PID 1
  answers a larger one itself with the Content Server's
  `413 Request Entity Too Large`, so that the reason reaches the browser.
  The Content Server's copies go to `/config/calibre/tmp` and stay there
  until its next restart, so `/config` needs room for two or three times
  each book added since then, and one more copy while a book is added.
- **Port 8083**, Calibre-Web, sets no limit of its own: a book of about
  1 GiB fits, since it holds two copies in the 2 GiB `/tmp` while it takes
  it.

## Configuration

Both apps keep their settings in `/config`, which their own pages write.
PID 1 reads three settings from the environment: the two passwords, each
as a value or a `_FILE`, on first start alone, and `TRUSTED_PROXIES`, at
each start.

### Calibre-Web's first settings

On a `/config` with no `calibre-web/app.db`, the image writes these, and
Calibre-Web keeps its own defaults for the rest:

- the library, `/books`;
- the folder of Calibre's programs, `/opt/calibre-bin`, which holds
  Calibre's `ebook-convert` and the image's `calibredb`;
- kepubify and unrar, `/usr/local/bin/kepubify` and
  `/usr/local/bin/unrar`;
- its port, 18083, on 127.0.0.1, behind PID 1;
- its log, to the container's log;
- uploads, allowed.

From then on, Calibre-Web's admin pages own every setting, and the image
never writes the file again. Leave the library, the folder of Calibre's
programs and the port as the image set them: PID 1 sends port 8083's
requests to 127.0.0.1:18083, and Calibre-Web reads the library through
the image's `calibredb`, as
[How the two apps stay in step](#how-the-two-apps-stay-in-step) says. An
`app.db` from another image keeps that image's settings.

### The Content Server's users

The Content Server's users are in `/config/calibre/users.sqlite`. Manage
them with Calibre's own `calibre-server --manage-users`, in the running
container. The image has no shell, so give the program's full path:

```bash
docker exec -it calibre /opt/calibre/calibre-server --userdb /config/calibre/users.sqlite --manage-users
```

It asks what to do: add a user, change one (its password, or whether it
may change the library), or remove one. For a script, name the command
after `--`. `add` and `chpass` read the password from standard input,
and `add --readonly` makes a user who can read the library but not change
it:

```bash
docker exec -i calibre /opt/calibre/calibre-server --userdb /config/calibre/users.sqlite --manage-users -- add --readonly alice < alice-password
docker exec calibre /opt/calibre/calibre-server --userdb /config/calibre/users.sqlite --manage-users -- list
```

- **Always give `--userdb /config/calibre/users.sqlite`.** Without it,
  Calibre makes and changes another file,
  `/config/calibre/server-users.sqlite`, which the Content Server never
  reads.
- Calibre takes all of standard input as the password, so the file must
  not end in a newline: Calibre refuses a password that holds a line
  break.
- A change takes effect at once: the running Content Server reads the
  file each time a user logs in.
- Leave `calibre-web` as it is. Calibre-Web reads books through the
  Content Server as that user, and the health check logs in as it, both
  with the password in `/config/calibre/internal-password`.
- The image's environment already names `/config/calibre` as Calibre's
  settings folder (`CALIBRE_CONFIG_DIRECTORY`), and `exec` keeps it, so
  the command needs nothing more. `podman exec` takes the same arguments;
  in Kubernetes, `kubectl exec -it deploy/calibre --` and then the same.

### How the two apps stay in step

Both apps use the one library in `/books`, and each keeps its own view of
it. The Content Server does not see a change another program makes to the
library until it starts again, and Calibre-Web must reconnect to the
library to see one. So PID 1 looks at the library's database,
`/books/metadata.db`, every second, and tells which app made each change
from the requests it passed on to each:

- When Calibre-Web changes the library, PID 1 restarts the Content Server,
  which then reads the library again.
- When the Content Server changes it, PID 1 tells Calibre-Web to
  reconnect to the library, which is quick, and Calibre-Web keeps
  running.
- The app that made a change is never reloaded for it.

A request that can change the library counts while the app is still
answering it. Once it is over, it counts for 10 more seconds, unless the
app itself answered it with 400 or more, such as `404 Not Found`: that
answer means that the app changed nothing. So the requests that robots
send to any address they find, such as a `POST` to a page Calibre-Web does
not have, never restart the Content Server after an edit made through port
8080. Only an answer from the app itself can stop a request from
counting. When the app gave no answer, for example because the client
left before the app answered, the request still counts, because the app
may still have made the change. The `502 Bad Gateway` that PID 1 gives
when it cannot reach the app is PID 1's own answer, not the app's.

PID 1 first waits until the library has had no change for 5 seconds. It
restarts the Content Server only when no request to it is still being
answered and no conversion it started is still running. A conversion
counts as running until it ends, or until 360 seconds (6 minutes) after
its start or the last check on its status, when the Content Server forgets
it. Neither reload waits more than 10 minutes, from the first change that
called for it.

While a restart of the Content Server waits, port 8080 refuses each
request that would change the library: it answers `503 Service
Unavailable`, with `Retry-After: 10` and a one-line reason. Reading still
works, and so do the status checks of a running conversion, so that the
conversion can end. During the restart itself, port 8080 answers 503 to
every request. A planned restart of the Content Server counts as healthy
for up to 2 minutes.

PID 1 sees a change at its next look, up to a second after it. In that
second, a change made through port 8080 to a book Calibre-Web has just
changed is not yet refused. In the image's tests the library stayed whole
each time, and both apps showed the later change.

Calibre allows one program to open a library at a time, and the Content
Server has it open. So Calibre-Web's calls to Calibre's `calibredb`,
which it makes to download a book with its metadata written in and before
it converts a book, go to the image's `calibredb`, which sends them to the
Content Server as the read-only user `calibre-web`. It refuses every other
call.

Calibre-Web's downloads and sends must carry what it has just changed.
So after an edit in Calibre-Web, its downloads and sends wait a few
seconds, until the Content Server has restarted with the edit in it:

- A download right after an edit has PID 1 restart the Content Server
  without the 5 seconds of quiet, as soon as no other request to it is
  being answered: at once, or, when the Content Server started less than
  5 seconds before, 5 seconds after that start. The download then waits
  for the restart, about a second. In the image's tests, a download right
  after an edit in a container that had just started came after 3 to 4
  seconds.
- A conversion to any format but KEPUB, started within about 5 seconds
  of an edit to the same book, may use the book's previous metadata:
  Calibre's own conversions read the metadata first, and that read does
  not wait.
- A conversion to KEPUB reads the book as a download does, and waits
  too, so the KEPUB carries the edit. This has a cost. Converting many books to KEPUB in Calibre-Web,
  mostly in a Kobo's first sync, which comes once for each device, takes
  about 5 seconds a book: the Content Server restarts once for each book.
  While those conversions run, the Content Server on port 8080 answers
  503 to about one request in five. In the image's tests, 20 books took
  99 seconds. Everyday use, a book at a time, does not notice it.
- While a conversion in the Content Server runs, a download does not
  wait, and carries what the Content Server has: the restart would wait
  for the conversion.
- During a restart, a request from a loopback address waits for the new
  Content Server rather than getting 503. The image's own tools send
  theirs from there. So can the other containers of a Kubernetes pod,
  `kubectl port-forward`, and, with host networking, the host's own
  programs: such a client waits out a restart too, and, logged in as
  `calibre-web`, can bring forward a restart that is already due.
- PID 1 knows Calibre-Web's calls by two signs together: they come from
  a loopback address and log in as `calibre-web`. Calibre-Web converts a
  book in the background, and writes it to the library a few seconds
  after such a call, often while the Content Server is still starting.
  PID 1 counts that write as Calibre-Web's, so that the Content Server
  restarts and shows the new format. An edit through port 8080 is never
  counted as Calibre-Web's for such a call, and never restarts the
  Content Server.

### Behind a reverse proxy

Point the proxy at port 8080 for the Content Server and port 8083 for
Calibre-Web. PID 1 answers on both and passes each request to its app on
loopback, so each app sees every request come from 127.0.0.1.
`TRUSTED_PROXIES` says whose forwarding headers reach the apps:

- It lists IP addresses and CIDR ranges, separated by commas, such as
  `192.0.2.10,198.51.100.0/24`. An entry that is neither stops the start,
  exit 1, with a line naming it. Unset or empty, it lists no one.
- An IPv4 address or range written in IPv6 form, such as
  `::ffff:198.51.100.0/120`, counts as the IPv4 one, here
  `198.51.100.0/24`. Such a range shorter than `/96` holds more than IPv4
  addresses, and stops the start, with a line naming it.
- From a listed source, PID 1 keeps `X-Forwarded-*`, `Forwarded`,
  `X-Real-IP`, `X-Scheme`, `X-Script-Name` and Calibre-Web's login header,
  and adds the source's address to `X-Forwarded-For`.
- From any other source, it removes all of them, and sets
  `X-Forwarded-For` to the source's address itself.
- On port 8080, it removes Calibre-Web's login header from every source:
  the Content Server has no use for it.

List the address your proxy connects from, as the container sees it, such
as the address of a proxy container on the same network. A CIDR range
trusts every address in it.

Calibre-Web reads `X-Script-Name`, to serve under a path such as
`/calibre-web/`, and `X-Scheme` and `X-Forwarded-Host`, for the links it
writes, only when they reach it: a proxy that sends them must be listed.
The image starts the Content Server with no URL prefix, so serve it at the
root of a host name of its own, such as `https://books.example.com/`.

**Calibre-Web's reverse-proxy login.** Calibre-Web can log a user in by a
header that its proxy sets, such as `X-Remote-User`, when you turn that on
in its admin pages. Behind the image, that works only through a listed
proxy: PID 1 removes the header, by the name Calibre-Web's settings give
it, from every other source. Leave Calibre-Web's own list of trusted proxy
addresses at its default, `127.0.0.1,::1`: every request reaches
Calibre-Web from PID 1, on loopback, so `TRUSTED_PROXIES` is the list that
decides.

**Turn the reverse-proxy login on only where nothing else shares the
container's network.** Calibre-Web itself listens on 127.0.0.1:18083,
and takes the login header from any program that reaches it there. A
program that shares the container's loopback can reach that port
directly, without passing PID 1, and log in to Calibre-Web as any user by
sending the header. Under `--network host`, that is every program on the
host. In a Kubernetes pod, it is every other container in the pod, such
as a sidecar. A container on its own network, as in the examples above,
in a pod with no other container, shares its loopback with no one.

After each change made in Calibre-Web's admin pages, PID 1 reads the
header's name again, which takes about a second, and requests from
unlisted sources wait meanwhile. If that read fails, port 8083 answers
`503` to unlisted sources, with `Retry-After: 10`, until a read works. PID
1 tries again at most once a second, and gives each read 10 seconds.

**Limits per address.** Behind PID 1, as behind any reverse proxy,
Calibre-Web sees every client as 127.0.0.1, so its limits per address are
shared by all clients:

- Kobo sync takes 3 requests a minute from one address, and each request
  with a valid Kobo token clears the count. So three requests with a wrong
  token within a minute, from any device, hold back every Kobo for up to a
  minute.
- Public registration, when you turn it on, takes 3 requests a minute and
  40 a day from one address: here, from all clients together.

Calibre-Web's limits on logins and on OPDS count per user name, so they
are not shared. `/reconnect` on port 8083 answers 404: only PID 1 calls
it, on loopback.

### What the image turns off

- **Calibre-Web's updater** (`UPDATER_AVAILABLE = False`), as upstream's
  packaged builds do: the image's files are read-only, and a new
  Calibre-Web arrives as a new image. This is one of the two lines the
  build changes in Calibre-Web. The other lets an upload named `.cbr` be a
  RAR archive that the image's libmagic names `application/vnd.rar`,
  beside the `application/x-rar` Calibre-Web looks for, so that a CBR
  comic can be uploaded.
- **The Content Server's Bonjour announcement** (`--disable-use-bonjour`):
  it would announce the container's own address, which other devices on
  your network cannot reach. With it off, nothing in the image listens on
  UDP.
- **Calibre-Web's Google Drive storage** (PyDrive2 and oauth2client). Its
  library requires oauth2client, a sign-in package with no release since
  2018, and older cryptography than Calibre-Web itself needs: cryptography
  older than 44, and pyOpenSSL 24.2.1 or older, where Calibre-Web needs
  cryptography 50 or newer. Calibre-Web's database settings page then
  does not offer Google Drive. Every other optional part of Calibre-Web is
  installed: Gmail, Goodreads, LDAP, OAuth sign-in, the metadata
  providers, comics and Kobo sync.
- **Mesa and LLVM.** Calibre's PDF output draws its pages in software, with
  no graphics card, so the image ships no graphics drivers, nor the LLVM
  and z3 they bring. The package entries of libEGL and libGLX, `libegl1`
  and `libglx0`, therefore each name a dependency that is not installed,
  `libegl-mesa0` and `libglx-mesa0`.

### PDF output

Calibre makes a PDF with QtWebEngine, Chromium's engine, and each process
that draws pages runs under Chromium's seccomp-bpf sandbox. The image sets
`QTWEBENGINE_CHROMIUM_FLAGS` to `--disable-gpu --disable-namespace-sandbox
--disable-dev-shm-usage`:

- the namespace sandbox, Chromium's other layer, cannot start under
  Docker's restrictions, and under rootless Podman it starts, then fails,
  and stops the PDF;
- shared memory goes to `/tmp`, since Podman, run as above, mounts
  `/dev/shm` read-only;
- there is no GPU, and Qt turns it off itself only while the variable is
  unset.

A value of your own replaces the image's. Keep `--disable-gpu` in it, or
every PDF fails, and keep the other two under Podman.

Calibre 9.19.0 comes with Qt WebEngine 6.10.1, which sometimes stops a PDF
conversion just as it starts to draw the pages. In the Content Server, the
conversion then fails, and its log ends at `Rendering 1 HTML files`,
followed by `EOFError`. For Calibre-Web, only `ebook-convert` was tested,
the Calibre program it runs to convert. It stopped in the same way, with
exit status 139. This happens more often when the machine is busy or has
few processors. In tests, these conversions stopped ("alone" means
Calibre's conversion run on its own, without the Content Server):

- one processor: 2 of 60 in the Content Server, 4 of 60 alone, and 1 of
  60 in `ebook-convert`;
- four processors, one conversion at a time, alone: 2 of 80;
- 17 processors, eight containers at once: 2 of 160 in the Content
  Server, and 4 to 10 of 160 alone;
- 17 processors, one conversion at a time, alone: none of 80.

Nothing is lost. The book keeps the formats it had, and converting it
again works. Qt fixed this in Qt WebEngine 6.10.2, so it stops once a
Calibre release comes with that version or a newer one.

### ImageMagick

Calibre-Web makes covers and thumbnails with ImageMagick, under the
image's own policy, `imagemagick/policy.xml`. It denies every format but
those Calibre-Web uses: PDF, which it reads alone, through Ghostscript,
and JPEG, PNG, WebP, GIF, TIFF and BMP. Its limits and its other rules are
upstream's own secure policy: no delegate program runs, and no `@file` or
`|command` path is read. Ghostscript is built into ImageMagick, not run as
a program, so a PDF's first page still becomes its cover. ImageMagick's
XML support is on, for the XMP metadata a PDF can carry. The build proves
the policy: a PDF reads to a JPEG, and SVG, MSL, PostScript and EPS are
refused, and so is writing a PDF.

### Names that are not ASCII

glibc's `C.utf8` locale ships, from `libc-bin`, so Qt, and with it
Calibre, runs with a UTF-8 locale. Titles and authors in Greek, in
Japanese and in accented Latin letters worked on both ports when tested.
Calibre names the folders and files it makes in ASCII,
`Ñandú` as `Nandu`, and Calibre-Web names those it makes in Unicode; both
apps work with both kinds of name.

### When an app stops

If either app stops on its own, PID 1 starts it again, while the other
goes on serving. It waits 1 second the first time, then twice as long
each time the app stops again, up to a minute, and back to 1 second once
the app has run for 10 minutes. Calibre-Web's own Restart, in its admin
pages, starts Calibre-Web again in the same process. Its Shutdown ends
Calibre-Web, and PID 1 then starts it again.

### Not in the image

- Calibre logs `Fontconfig error: No writable cache directories` three
  times as it starts: the root is read-only, and fontconfig reads the
  fonts with no cache.
- Calibre-Web warns that it finds no time zone setting, and uses UTC. The
  image carries the time zone data: set `TZ`, such as
  `TZ=Europe/Berlin`, for another.
- Calibre-Web warns that it keeps its rate limits in memory: they start
  again from zero each time Calibre-Web starts.

## Health

`divbast-calibre health`, from inside the container:

- reads the state file PID 1 keeps, `/tmp/divbast-calibre/state.json`,
  which says whether each app is up;
- asks the Content Server, on 127.0.0.1:18080, for `/ajax/library-info`,
  logged in as `calibre-web`. The answer must be 200, and its `Server`
  header must name this image's Calibre version exactly, such as
  `calibre 9.19.0`;
- asks Calibre-Web, on 127.0.0.1:18083, for `/login`, which must answer
  200. Calibre-Web's version is on a page that needs a login, and the root
  filesystem is read-only, so the Calibre-Web that answers can only be the
  image's;
- checks, in the container's `/proc/net/tcp6` and `/proc/net/tcp`, that
  ports 8080 and 8083 are listened on, on every address.

A Content Server that PID 1 is restarting, to load a change, counts as
healthy for up to 2 minutes, and is not asked. Each app's check gets 4
seconds, so the whole check ends within the `HEALTHCHECK`'s 10-second
timeout. It prints nothing and exits 0 when all of it holds, and exits 1
with one line for each part that does not: `state: <reason>`,
`calibre-server: <reason>`, `calibre-web: <reason>` or
`listen: <reason>`.

The image's `HEALTHCHECK` runs it every 30 seconds, with a timeout of 10
seconds, a start period of 60 seconds and 3 retries; Docker reads it.
Podman never sees the `HEALTHCHECK` of an image published as an OCI
index, so pass the same command as
`--health-cmd '["/usr/local/bin/divbast-calibre","health"]'`, or
`HealthCmd=` in a Quadlet, as above. In Kubernetes, the probes above run
the same command. The command must be given in exec form: there is no
shell to run a command given as one string.

## The pipeline

Every pull request and push to main runs divbast/ci's `app-check.yml`:
lint (the Go code, the Containerfile, the workflows, REUSE, the Python
helpers, and the Python locks against their pins), a build for amd64 and
arm64 once every image the build starts from is verified, the acceptance
suite under Docker and rootless Podman, and the scan. A daily slot
publishes main when it has moved since the last image.

### Publishing

A merge never publishes. The daily slot publishes `main` when `main` moved
since the commit the published `:latest` was built from, and the button, a
manual run of `build` from `main`, publishes at once.

Its publish job calls divbast/ci's `app-publish.yml` with
`image: ghcr.io/divbast/calibre`. A publish pushes an index of the amd64
and arm64 images, attests it, verifies the attestation, and only then tags
it three times: with Calibre's version, the Containerfile's
`CALIBRE_VERSION`; with the pair of both apps, its `IMAGE_PAIR`, such as
`9.19.0-cw.79d3571`; and with `latest`. It ends by checking, as an
anonymous client, that all three tags are the attested index.

### The scan

The scan fails a change only on a HIGH or CRITICAL finding that the
published image does not have already. No image scan can tell what is in
Calibre's own build, which carries its own libraries and Python packages,
or what ImageMagick, Ghostscript, kepubify and unrar are, since they are
built here. So the image names them in two SBOMs, which its
`io.github.divbast.scan.sboms` label declares, and the scan judges them by
those:

- `/usr/local/share/divbast/calibre-bundle.cdx.json`: the libraries and
  Python packages in Calibre's build, from the lists at Calibre's signed
  tag. The image marks this SBOM advisory, in its
  `io.github.divbast.scan.sboms.advisory` label: Calibre's build is
  upstream's own signed build of those libraries, which this repository
  cannot patch, so the scan lists its findings and never fails on them.
- `/usr/local/share/divbast/tools.cdx.json`: ImageMagick, Ghostscript and
  the jbig2dec inside it, kepubify and unrar. Its findings block as every
  other finding does.

## Dependency updates

Renovate opens a pull request for each new input, and merges it once every
check passes:

- each Calibre release. The update empties the two checksums of Calibre's
  build, and the pull request fills them in from the files
  download.calibre-ebook.com serves then; the build checks their
  signature. A release signed by any other key stops at its pull request,
  until a person checks the new key and replaces
  `keys/calibre-release.asc`;
- each new commit on Calibre-Web's master branch, with the checksum of its
  archive filled in the same way. Nothing signs Calibre-Web's commits, so
  the acceptance suite is what holds a broken one back;
- each ImageMagick release and each kepubify release, by tag and commit;
  the build checks that the tag still names the commit;
- each Ghostscript release, with the checksum of its tarball filled in
  the same way;
- each unrar tarball RARLAB's page links, betas included. RARLAB
  publishes no checksum, so the pull request records the sha256 of the
  tarball RARLAB served when it was made, and the build holds the
  download to it from then on;
- each line of `python/pins.txt`;
- the Docker Hardened Images, with the Python runtime and its `-dev`
  image in one pull request, and the packages of the toolchain and
  runtimelibs stages, all in one pull request.

A pull request that moves Calibre, Calibre-Web, Ghostscript, unrar or a
pin runs `go run ./cmd/divbast-pins` too, which fills in the checksums the
update emptied, writes the Python locks again from Calibre-Web's
requirements at the new commit and the pins, and writes `IMAGE_PAIR`, so
they all move together. A pin outside the range Calibre-Web's
requirements allow fails that command, so its pull request never merges.

So that Renovate does not propose such a pin at all, the command also
writes `python/renovate-bounds.json`, a Renovate preset that
`renovate.json` extends: for each pin that Calibre-Web's requirements, or
a package the image installs, bound from above, every version range asked
of it, as `pip check` reads the packages' requirements. Renovate reads it
from `main`, so a bound lifts once the pull request that moves its source,
such as a newer Calibre-Web, merges. A pin that asks another for its own
version, as python-Levenshtein asks Levenshtein, cannot move alone, so
the two move together, in one pull request, within the first one's
bounds. A source build's own requirements are
in its source release alone, so the one such bound, Cython's for lxml, is
a rule of its own in `renovate.json`.

`python/pins.txt` holds one `package==version` a line, each under a
comment saying which kind it is: `# upstream leaves it unpinned: <its
requirement>`, `# needed by <package> …`, for a package a pinned one
requires and upstream's list leaves out, or `# builds <packages> …`, for
the build venv alone. A package upstream lists that the image leaves out
is under `# leaves out: <why>`, by its name alone: PyDrive2 and
oauth2client.

The repository variable `CALIBRE_AUTOMERGE` stops Renovate merging when
it is anything but `enabled`:

```bash
gh variable set CALIBRE_AUTOMERGE --body disabled
```

## Development

- `just setup` checks out divbast/ci at the commit the workflows pin and
  installs the toolchain.
- `just build`, `just acceptance` and `just test` build the image and run
  the acceptance suite, under the engine `ENGINE` names.
- `just lint` runs every lint gate, ruff and ty over the Python helpers,
  and `go run ./cmd/divbast-pins -check`, which needs the network. It
  needs a running Docker daemon too, logged in to dhi.io, for
  `docker build --check`.
- `go run ./cmd/divbast-pins` writes `python/requirements.lock`,
  `python/build.lock`, `python/files.txt` and
  `python/renovate-bounds.json` from the pins, after a change
  to `python/pins.txt` or to the Containerfile's pins. It fills in a
  checksum only when it is empty, and writes `IMAGE_PAIR`: after moving
  `CALIBRE_VERSION`, `CALIBRE_WEB_COMMIT`, Ghostscript's version and tag,
  or `UNRAR_VERSION` by hand, empty its checksums, then run it.

The recipes that call GitHub, pinact's and zizmor's, read a fine-grained
personal access token from `GITHUB_TOKEN`: divbast/ci's README says which,
and how to pass it.

## AI/LLM Disclosure

This project was developed with significant LLM involvement. Each git commit contains an `Assisted-by:` tag detailing the particular model/tool used.
