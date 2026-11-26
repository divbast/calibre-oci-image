# SPDX-License-Identifier: GPL-3.0-only
# SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

# Calibre's version, which tags the image: app-publish.yml reads it from
# this line, by the name build.yml gives as version-arg. The two checksums
# are of the bundles for x86-64 and for ARM, which download.calibre-ebook.com
# serves, empty until go run ./cmd/divbast-pins fills them in.
ARG CALIBRE_VERSION=9.19.0
ARG CALIBRE_SHA256_X86_64=35c1cbda1a2548aceeb89cb0a4be47a36025b5dc102ac046f7eaf98ba46a452d
ARG CALIBRE_SHA256_ARM64=8de5c5d3af3f28e0227e215a27e8d69c14c50a38cc3065b249996997df392a2f
# Calibre-Web is built from its master branch, by a full
# commit, and the checksum of the archive GitHub makes from it
ARG CALIBRE_WEB_COMMIT=79d35711d73491881ecd2b13ded02c277543dbcb
ARG CALIBRE_WEB_SHA256=0f4f0bf0e918904c13fee5052decc03cded8ac511b773f7d71245bc05309f609
# The tag that names both apps, <Calibre version>-cw.<first 7 of the
# Calibre-Web commit>, written by go run ./cmd/divbast-pins, which -check holds to them
ARG IMAGE_PAIR=9.19.0-cw.79d3571
# ImageMagick, built from the commit its annotated tag names
ARG IMAGEMAGICK_VERSION=7.1.2-36
ARG IMAGEMAGICK_COMMIT=dcec873ede2912a1b045a50fd54cd138ee5f3fa4
# Ghostscript's source tarball, from the release tagged gs<GHOSTSCRIPT_TAG>
ARG GHOSTSCRIPT_VERSION=10.08.0
ARG GHOSTSCRIPT_TAG=10080
ARG GHOSTSCRIPT_SHA256=c20492bc8ebb96c87fa2e52a0926e1cda8cde95d66145e018ac713fed5da38cf
# kepubify, built from the commit its tag names
ARG KEPUBIFY_VERSION=5.0.0
ARG KEPUBIFY_COMMIT=29f338fe1239f3aa61e9df606fe3c5b9bda30362
# unrar's source, which RARLAB publishes with no checksum: the one first
# filled in holds the tarball from then on
ARG UNRAR_VERSION=7.3.2
ARG UNRAR_SHA256=f61a1160d6e2ebbda2ad353c8e82f62ab4312f97fde23c6c2c982d25e0014f99
# The runtime: Docker Hardened Images' Python on Debian 13, with no shell,
# no package manager, its nonroot user 65532, and pip, setuptools and wheel,
# which DHI ships in it and nothing here runs. The app runs on its Python,
# from a venv the -dev image's Python of the same release builds: Renovate
# moves the two in one PR, and divbast-pins fails if they differ. Its
# version is also the Python the locks are generated for. CI verifies its
# signature by Docker before anything is built, as it does every stage's
# image below.
ARG RUNTIME_IMAGE=dhi.io/python:3.15.2-debian13@sha256:d5387cad673412ef60e7fdadec757bd653c7dc289bfdccb4beff6d1ccec94f36

FROM ${RUNTIME_IMAGE} AS runtime

# The tools every build stage below uses, from DHI's own repository alone:
# Debian's sources are deleted first, so apt takes nothing DHI does not
# build, and fails on anything it lacks rather than falling back. Each
# package is pinned, one name=version a line, to DHI's build, which
# SABnzbd's Renovate manager reads; DHI deletes a superseded build, so a
# pin can fail the build until Renovate's PR moves it. The C and Rust
# compilers, and git and gpg for an upstream's fetch, sit idle for the
# demo, which has no package to build. Python's headers come with the image.
FROM dhi.io/python:3.15.2-debian13-dev@sha256:a1069444b00f18a15f0088a7f6c33ed6070d7f2593919130892b22c5a4f124b7 AS toolchain
SHELL ["/bin/bash", "-euo", "pipefail", "-c"]
RUN rm /etc/apt/sources.list.d/debian.sources \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        binutils=2.44-3+deb13u0+dhi10 \
        cargo=1.100.0-4 \
        curl=8.14.1-2+deb13u7+dhi1 \
        g++=4:14.2.0-1+deb13u0+dhi0 \
        gcc=4:14.2.0-1+deb13u0+dhi0 \
        git=1:2.47.3-0+deb13u1+dhi3 \
        gpg=2.4.7-21+deb13u1+dhi5 \
        gpg-agent=2.4.7-21+deb13u1+dhi5 \
        gzip=1.13-1+deb13u1+dhi1 \
        libffi-dev=3.4.8-2+deb13u0+dhi2 \
        libssl-dev=3.5.9-1~deb13u1+dhi2 \
        make=4.4.1-2+deb13u0+dhi1 \
        pkgconf=1.8.1-4+deb13u0+dhi1 \
        rustc=1.100.0-4 \
    && rm -rf /var/lib/apt/lists/*

# The app's tree, at the path it has in the runtime. The demo is the
# repository's own app/. An app built from an upstream replaces this COPY
# with SABnzbd's fetch: the commit fetched by its hash, its signature
# checked against the release key in keys/, the tag checked to still name
# it, and .git removed, with each fetch retried.
FROM toolchain AS fetch
COPY app/ /app/template/

# A frontend an app builds, as Lidarr's, goes in a stage of its own here,
# on DHI's node -dev image, and its output is copied into the app's tree.

# The Python packages, from requirements.lock: the pure ones from their
# wheels, and those with C or Rust code built from source here, against
# DHI's OpenSSL and libraries. Every file is first fetched by the URL and
# checked against the sha256 files.txt names, with curl and sha256sum, so
# no package's code runs while the network is on: pip download would run
# each source release's build backend to read its metadata. The Rust
# crates of each source release that carries a Cargo.lock are fetched with
# it, by cargo, which checks each against that Cargo.lock. Each fetch gets
# three retries, 10s, 30s and 90s apart, so one dropped connection does
# not cost the daily slot.
FROM toolchain AS deps
COPY python/files.txt python/requirements.lock python/build.lock /src/python/
SHELL ["/bin/bash", "-euo", "pipefail", "-c"]
RUN retry() { \
        for delay in 10 30 90; do "$@" && return 0; sleep "${delay}"; done; \
        "$@"; \
    }; \
    shopt -s nullglob; \
    install -d /dl; \
    sed '/^#/d' /src/python/files.txt | while read -r sha url; do \
        retry curl -fsSL --proto '=https' --tlsv1.2 -o "/dl/${url##*/}" "${url}"; \
        echo "${sha}  /dl/${url##*/}" | sha256sum -c --quiet -; \
    done; \
    export CARGO_HOME=/cargo; \
    for sdist in /dl/*.tar.gz; do \
        listing=$(tar -tzf "${sdist}"); \
        top=${listing%%/*}; \
        if grep -qx "${top}/Cargo.lock" <<<"${listing}"; then \
            tar -xzf "${sdist}" -C /tmp "${top}/Cargo.toml" "${top}/Cargo.lock"; \
            tar -xzf "${sdist}" -C /tmp --wildcards "${top}/src/*" 2>/dev/null || true; \
            retry cargo fetch --locked --manifest-path "/tmp/${top}/Cargo.toml"; \
            rm -rf "/tmp/${top:?}"; \
        fi; \
    done
# The build backends, from build.lock, into a venv of their own, which
# never ships. Then, with no network at all, every source release into a
# wheel. --check-build-dependencies holds each build to what its
# pyproject.toml asks for, from that venv, whose bin/ comes first on PATH,
# since some backends, as maturin and cmake, are run as programs. A
# package whose build calls another's code, as cryptography's calls cffi,
# needs that one built and installed into the build venv first, as
# SABnzbd's deps stage does. A Rust crate that links OpenSSL takes DHI's,
# never one it vendors.
RUN --network=none python3 -m venv /opt/build \
    && /opt/build/bin/pip install --no-index --find-links /dl --no-deps --require-hashes -r /src/python/build.lock
RUN --network=none PATH="/opt/build/bin:${PATH}" CARGO_HOME=/cargo CARGO_NET_OFFLINE=true OPENSSL_NO_VENDOR=1 \
    /opt/build/bin/pip wheel --no-index --find-links /dl --no-deps --no-build-isolation --check-build-dependencies \
        --require-hashes -w /wheels -r /src/python/requirements.lock
# The app's venv, at the path it has in the runtime, with no pip of its
# own: every wheel installed into it by the build venv's pip, when there is
# one, as the demo has none, then pip's own check that each package's
# requirements are met there.
RUN --network=none python3 -m venv --without-pip /app/venv \
    && shopt -s nullglob \
    && wheels=(/wheels/*.whl) \
    && if ((${#wheels[@]})); then \
        /opt/build/bin/pip --python /app/venv/bin/python install --no-index --no-deps "${wheels[@]}"; \
    fi \
    && /opt/build/bin/pip --python /app/venv/bin/python check \
    && site=$(/app/venv/bin/python -c 'import sysconfig; print(sysconfig.get_path("purelib"))') \
    && for tool in pip setuptools wheel; do \
        if [[ -e "${site}/${tool}" ]]; then echo "ERROR: the venv holds ${tool}" >&2; exit 1; fi; \
    done
# Bytecode for -OO, compiled now since the root is read-only at run time.
# A hash-based .pyc that is never checked against its source needs no
# timestamp, so the same sources give the same files.
COPY --from=fetch /app/template /app/template
RUN --network=none python3 -m compileall -q -j0 -o 2 --invalidation-mode unchecked-hash /app/venv/lib /app/template

# A native tool the app runs, as SABnzbd's unrar and 7zz, goes in a stage
# of its own here, FROM toolchain: from its source, held to a sha256 or a
# commit, linked to the runtime's libraries, hardened, and checked with
# SABnzbd's check-elf. No image scan can see such a binary, so the app
# also writes an SBOM of it, in a stage of its own, from the same ARGs,
# and names the file in the io.github.divbast.scan.sboms label below,
# which divbast/ci's scan reads.

# The image's health probe, static and on the standard library alone.
FROM dhi.io/golang:1.27.4-debian13-dev@sha256:121afb8b390b1d6babba8cd0a93eaf2ffa3735f835d388b9d79c2be38b41ee4f AS probe
ARG CALIBRE_VERSION
WORKDIR /src
# go.mod's replace names ./.ci, which the build context leaves out: the
# probe imports nothing from divbast/ci, so Go never looks for it
COPY go.mod ./
COPY cmd/divbast-calibre/ cmd/divbast-calibre/
COPY internal/probe/ internal/probe/
RUN CGO_ENABLED=0 GOFLAGS=-mod=readonly GOTOOLCHAIN=local \
    go build -trimpath -ldflags "-s -w -X main.version=${CALIBRE_VERSION}" \
    -o /out/divbast-calibre ./cmd/divbast-calibre

FROM runtime
ARG CALIBRE_VERSION

# Root-owned, and never written
COPY --from=deps /app/venv /app/venv
COPY --from=deps /app/template /app/template
COPY --from=probe /out/divbast-calibre /usr/local/bin/divbast-calibre

# UTF-8 for the app; no .pyc written at run time, on a read-only root,
# which the build compiled already; and each log line to the engine as it
# is written.
ENV LC_ALL=C.UTF-8 \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

# An app adds the licences of everything it ships beyond its base: its
# own, its upstream's, and each Python package's, which its dist-info
# names; and, when it writes one, the io.github.divbast.scan.sboms label.
LABEL org.opencontainers.image.source="https://github.com/divbast/oci-image-template" \
    org.opencontainers.image.url="https://github.com/divbast/oci-image-template" \
    org.opencontainers.image.title="oci-image-template" \
    org.opencontainers.image.version="${CALIBRE_VERSION}" \
    org.opencontainers.image.licenses="GPL-3.0-only"

EXPOSE 8080
# Exec form: the shell form would need /bin/sh, which the image lacks.
# --timeout must clear divbast-calibre health's own budget: it allows each
# of its two exchanges 4 s and runs them one after the other, so a listener
# that accepts and never answers costs 8 s. A shorter timeout kills the
# probe mid-flight and records a bare engine timeout instead of the probe's
# own line, which is the only diagnostic there is. Podman reads no
# HEALTHCHECK from an image published as an OCI index: its users pass the
# same array as --health-cmd.
HEALTHCHECK --interval=30s --timeout=10s --start-period=60s --retries=3 \
    CMD ["/usr/local/bin/divbast-calibre", "health"]
USER 65532:65532
# The app itself runs as PID 1, with nothing before it: the demo handles
# SIGTERM and stops. -OO matches the bytecode the build compiled.
ENTRYPOINT ["/app/venv/bin/python", "-OO", "/app/template/server.py"]
