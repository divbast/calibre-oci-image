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
# Renovate's deb manager reads; DHI deletes a superseded build, so a pin
# can fail the build until Renovate's PR moves it. Beside SABnzbd's set:
# xz for Calibre's bundle and Ghostscript's tarball, gpgv for the bundle's
# signature, and the headers that ImageMagick, Ghostscript and the Python
# packages with C code build against. Python's headers come with the
# image.
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
        gpgv=2.4.7-21+deb13u1+dhi5 \
        gzip=1.13-1+deb13u1+dhi1 \
        libffi-dev=3.4.8-2+deb13u0+dhi2 \
        libfreetype-dev=2.13.3+dfsg-1+deb13u2+dhi0 \
        libjpeg62-turbo-dev=1:2.1.5-4+deb13u0+dhi0 \
        liblcms2-dev=2.16-2+deb13u2+dhi2 \
        libldap-dev=2.6.10+dfsg-1+deb13u0+dhi1 \
        libopenjp2-7-dev=2.5.3-2.1~deb13u2+dhi3 \
        libpng-dev=1.6.48-1+deb13u6+dhi0 \
        libsasl2-dev=2.1.28+dfsg1-9+deb13u0+dhi2 \
        libssl-dev=3.5.9-1~deb13u1+dhi2 \
        libtiff-dev=4.7.0-3+deb13u3+dhi3 \
        libwebp-dev=1.5.0-0.1+deb13u0+dhi0 \
        libxml2-dev=2.12.7+dfsg+really2.9.14-2.1+deb13u3+dhi3 \
        libxslt1-dev=1.1.35-1.2+deb13u3+dhi2 \
        make=4.4.1-2+deb13u0+dhi1 \
        pkgconf=1.8.1-4+deb13u0+dhi1 \
        rustc=1.100.0-4 \
        xz-utils=5.8.1-1+deb13u2+dhi0 \
        zlib1g-dev=1:1.3.dfsg+really1.3.1-1+deb13u1+dhi0 \
    && rm -rf /var/lib/apt/lists/*
# The check each native binary below must pass: position-independent, bound
# at load, with a read-only relocation table, and needing no library the
# runtime image does not ship, read from that image's own tree, as
# SABnzbd's. A stage whose binary links the packages the runtimelibs stage
# adds copies those into /runtime first.
COPY --from=runtime /usr/lib /runtime/usr/lib
RUN install -d /usr/local/bin && cat >/usr/local/bin/check-elf <<'EOF' && chmod 0755 /usr/local/bin/check-elf
#!/bin/bash
set -euo pipefail
bin=$1
readelf -h "${bin}" | grep -q 'Type: *DYN' || { echo "ERROR: ${bin} is not position-independent" >&2; exit 1; }
readelf -d "${bin}" | grep -Eq 'BIND_NOW|FLAGS_1.* NOW' || { echo "ERROR: ${bin} is not bound at load (BIND_NOW)" >&2; exit 1; }
readelf -lW "${bin}" | grep -q GNU_RELRO || { echo "ERROR: ${bin} has no RELRO segment" >&2; exit 1; }
for lib in $(readelf -d "${bin}" | sed -n 's/.*(NEEDED).*\[\(.*\)\]/\1/p'); do
    if ! find /runtime -name "${lib}" -print -quit | grep -q .; then
        echo "ERROR: ${bin} needs ${lib}, which the runtime image does not ship" >&2
        exit 1
    fi
    echo "${bin} needs ${lib}"
done
EOF

# The system libraries Calibre's bundle, Calibre-Web, ImageMagick and
# Ghostscript need beyond the runtime's own, from DHI's repository: each a
# pinned .deb, unpacked into /out, which the image takes as it is. A
# runtime has no dpkg to install them, so each package's control file goes
# to /var/lib/dpkg/status.d/<name>, as DHI lists the runtime's own
# packages there, and the scanners read it. The list is the packages the
# bundle and the tools link, and every package those depend on, as apt
# resolved them against the runtime's packages: Mesa, behind libEGL and
# libGLX, the X11 client libraries, NSS and NSPR, ALSA's library and
# libudev, which QtWebEngine links, PCRE2 and libmount, which the bundle's
# GLib links, fontconfig and a font, libmagic, OpenLDAP's library, libxml2
# and libxslt, and the image libraries; and, so that as many of the
# bundle's files as DHI can serve find their libraries, those Qt's X11,
# Wayland, sound and speech plugins link: PulseAudio's client, flite, and
# the X11 session, render, shape, keyboard and RandR libraries. Those pull
# in x11-common, and through it sysvinit-utils. apt-get check then proves
# nothing is missing: every package's dependencies are met by the
# runtime's packages and these. Two packages are needed only on x86-64,
# by Mesa's Intel driver. No maintainer script runs, and none is needed:
# the libraries are found in the loader's default directories.
FROM toolchain AS runtimelibs
ARG TARGETARCH
COPY --from=runtime /var/lib/dpkg/status.d /runtime/var/lib/dpkg/status.d
SHELL ["/bin/bash", "-euo", "pipefail", "-c"]
WORKDIR /tmp/debs
RUN extra=(); \
    if [[ "${TARGETARCH}" == amd64 ]]; then \
        extra=( \
            libdrm-intel1=2.4.124-2+deb13u0+dhi0 \
            libpciaccess0=0.17-3+deb13u0+dhi0 \
        ); \
    fi; \
    apt-get update; \
    install -d /out/var/lib/dpkg/status.d; \
    apt-get download "${extra[@]}" \
        fontconfig-config=2.15.0-2.3+deb13u0+dhi0 \
        fonts-dejavu-core=2.37-8+deb13u0+dhi0 \
        fonts-dejavu-mono=2.37-8+deb13u0+dhi0 \
        libasound2-data=1.2.14-1+deb13u1+dhi0 \
        libasound2t64=1.2.14-1+deb13u1+dhi0 \
        libasyncns0=0.8-6+deb13u0+dhi0 \
        libblkid1=2.41.5-0+deb13u1+dhi3 \
        libbrotli1=1.1.0-2+deb13u1+dhi2 \
        libbsd0=0.12.2-2+deb13u0+dhi1 \
        libcap2=1:2.75-10+deb13u1+dhi1 \
        libdbus-1-3=1.16.2-2+deb13u0+dhi1 \
        libdeflate0=1.23-2+deb13u0+dhi0 \
        libdrm-amdgpu1=2.4.124-2+deb13u0+dhi0 \
        libdrm-common=2.4.124-2+deb13u0+dhi0 \
        libdrm2=2.4.124-2+deb13u0+dhi0 \
        libedit2=3.1-20250104-1+deb13u0+dhi2 \
        libegl-mesa0=25.0.7-2+deb13u1+dhi0 \
        libegl1=1.7.0-1+deb13u0+dhi1 \
        libelf1t64=0.192-4+deb13u0+dhi1 \
        libflac14=1.5.0+ds-2+deb13u0+dhi0 \
        libflite1=2.2-7+deb13u0+dhi0 \
        libfontconfig1=2.15.0-2.3+deb13u0+dhi0 \
        libfreetype6=2.13.3+dfsg-1+deb13u2+dhi0 \
        libgbm1=25.0.7-2+deb13u1+dhi0 \
        libgcrypt20=1.11.0-7+deb13u1+dhi1 \
        libgl1-mesa-dri=25.0.7-2+deb13u1+dhi0 \
        libglvnd0=1.7.0-1+deb13u0+dhi1 \
        libglx-mesa0=25.0.7-2+deb13u1+dhi0 \
        libglx0=1.7.0-1+deb13u0+dhi1 \
        libgpg-error0=1.51-4+deb13u0+dhi2 \
        libice6=2:1.1.1-1+deb13u0+dhi0 \
        libjbig0=2.1-6.1+deb13u0+dhi1 \
        libjpeg62-turbo=1:2.1.5-4+deb13u0+dhi0 \
        liblcms2-2=2.16-2+deb13u2+dhi2 \
        libldap2=2.6.10+dfsg-1+deb13u0+dhi1 \
        liblerc4=4.0.0+ds-5+deb13u0+dhi1 \
        libllvm19=1:19.1.7-3+deb13u0+dhi1 \
        libmagic-mgc=1:5.46-5+deb13u0+dhi1 \
        libmagic1t64=1:5.46-5+deb13u0+dhi1 \
        libmd0=1.1.0-2+deb13u0+dhi1 \
        libmount1=2.41.5-0+deb13u1+dhi3 \
        libnspr4=2:4.36-1+deb13u0+dhi1 \
        libnss3=2:3.134-1+deb13u0+dhi0 \
        libogg0=1.3.5-3+deb13u0+dhi0 \
        libopengl0=1.7.0-1+deb13u0+dhi1 \
        libopenjp2-7=2.5.3-2.1~deb13u2+dhi3 \
        libopus0=1.5.2-2+deb13u0+dhi0 \
        libpcre2-8-0=10.46-1~deb13u3+dhi0 \
        libpng16-16t64=1.6.48-1+deb13u6+dhi0 \
        libpulse0=17.0+dfsg1-2+dhi0 \
        libsasl2-2=2.1.28+dfsg1-9+deb13u0+dhi2 \
        libsasl2-modules-db=2.1.28+dfsg1-9+deb13u0+dhi2 \
        libselinux1=3.8.1-1+deb13u0+dhi2 \
        libsensors-config=1:3.6.2-2+deb13u0+dhi0 \
        libsensors5=1:3.6.2-2+deb13u0+dhi0 \
        libsharpyuv0=1.5.0-0.1+deb13u0+dhi0 \
        libsm6=2:1.2.6-1+deb13u0+dhi0 \
        libsndfile1=1.2.2-2+deb13u1+dhi4 \
        libsystemd0=257.13-1~deb13u1+dhi2 \
        libtiff6=4.7.0-3+deb13u3+dhi3 \
        libudev1=257.13-1~deb13u1+dhi2 \
        libvorbis0a=1.3.7-3+deb13u0+dhi0 \
        libvorbisenc2=1.3.7-3+deb13u0+dhi0 \
        libvulkan1=1.4.309.0-1+deb13u0+dhi0 \
        libwayland-client0=1.23.1-3+deb13u0+dhi0 \
        libwayland-cursor0=1.23.1-3+deb13u0+dhi0 \
        libwayland-egl1=1.23.1-3+deb13u0+dhi0 \
        libwayland-server0=1.23.1-3+deb13u0+dhi0 \
        libwebp7=1.5.0-0.1+deb13u0+dhi0 \
        libwebpdemux2=1.5.0-0.1+deb13u0+dhi0 \
        libwebpmux3=1.5.0-0.1+deb13u0+dhi0 \
        libx11-6=2:1.8.12-1+deb13u0+dhi0 \
        libx11-data=2:1.8.12-1+deb13u0+dhi0 \
        libx11-xcb1=2:1.8.12-1+deb13u0+dhi0 \
        libxau6=1:1.0.11-1+deb13u0+dhi0 \
        libxcb-dri3-0=1.17.0-2+deb13u0+dhi3 \
        libxcb-glx0=1.17.0-2+deb13u0+dhi3 \
        libxcb-present0=1.17.0-2+deb13u0+dhi3 \
        libxcb-randr0=1.17.0-2+deb13u0+dhi3 \
        libxcb-render0=1.17.0-2+deb13u0+dhi3 \
        libxcb-shape0=1.17.0-2+deb13u0+dhi3 \
        libxcb-shm0=1.17.0-2+deb13u0+dhi3 \
        libxcb-sync1=1.17.0-2+deb13u0+dhi3 \
        libxcb-xfixes0=1.17.0-2+deb13u0+dhi3 \
        libxcb-xkb1=1.17.0-2+deb13u0+dhi3 \
        libxcb1=1.17.0-2+deb13u0+dhi3 \
        libxdmcp6=1:1.1.5-1+deb13u0+dhi0 \
        libxext6=2:1.3.4-1+deb13u0+dhi1 \
        libxkbcommon-x11-0=1.7.0-2+deb13u0+dhi0 \
        libxkbcommon0=1.7.0-2+deb13u0+dhi0 \
        libxml2=2.12.7+dfsg+really2.9.14-2.1+deb13u3+dhi3 \
        libxrandr2=2:1.5.4-1+deb13u0+dhi0 \
        libxrender1=1:0.9.12-1+deb13u0+dhi1 \
        libxshmfence1=1.3.3-1+deb13u0+dhi0 \
        libxslt1.1=1.1.35-1.2+deb13u3+dhi2 \
        libxxf86vm1=1:1.1.4-1+deb13u0+dhi0 \
        libz3-4=4.13.3-1+deb13u0+dhi1 \
        lsb-base=11.6+deb13u0+dhi1 \
        mesa-libgallium=25.0.7-2+deb13u1+dhi0 \
        sysvinit-utils=3.14-4+deb13u0+dhi1 \
        x11-common=1:7.7+24+deb13u1+dhi0 \
        xkb-data=2.42-1+deb13u0+dhi0 \
        ; \
    for deb in /tmp/debs/*.deb; do \
        name=$(dpkg-deb -f "${deb}" Package); \
        if [[ -e "/runtime/var/lib/dpkg/status.d/${name}" ]]; then \
            echo "ERROR: the runtime already ships ${name}" >&2; \
            exit 1; \
        fi; \
        dpkg-deb -x "${deb}" /out; \
        dpkg-deb -f "${deb}" >"/out/var/lib/dpkg/status.d/${name}"; \
    done; \
    for f in /runtime/var/lib/dpkg/status.d/* /out/var/lib/dpkg/status.d/*; do \
        sed '1a Status: install ok installed' "${f}"; \
        echo; \
    done >/tmp/status; \
    if ! apt-get check -o Dir::State::status=/tmp/status >/tmp/check 2>&1; then \
        cat /tmp/check >&2; \
        echo "ERROR: a runtime package needs one the Containerfile does not download: add it to the list above" >&2; \
        exit 1; \
    fi; \
    rm -rf /out/usr/share/man /out/usr/share/lintian /out/usr/share/bug /tmp/debs/*.deb /tmp/status /var/lib/apt/lists/*; \
    echo "$(find /out/var/lib/dpkg/status.d -type f | wc -l) packages unpacked"

# Calibre's own build for Linux, the bundle for this arch, held to its
# sha256, then to its signature, by Kovid Goyal's release key in
# keys/calibre-release.asc. That file must hold that one key and nothing
# else, whose fingerprint the build checks, so a signature by any other key
# fails. Then the list of what Calibre's build compiled into the bundle,
# bypy/sources.json, for the SBOM: from Calibre's tag for this version,
# whose signature git checks against the same key, alone in the keyring.
# Only the tag's objects are fetched, and the one file, which git checks
# against the tagged commit. Each fetch gets three retries, 10s, 30s and
# 90s apart, so one dropped connection does not cost the daily slot.
FROM toolchain AS calibre
ARG TARGETARCH
ARG CALIBRE_VERSION
ARG CALIBRE_SHA256_X86_64
ARG CALIBRE_SHA256_ARM64
COPY keys/calibre-release.asc /tmp/calibre-release.asc
SHELL ["/bin/bash", "-euo", "pipefail", "-c"]
RUN retry() { \
        for delay in 10 30 90; do "$@" && return 0; sleep "${delay}"; done; \
        "$@"; \
    }; \
    key=3CE1780F78DD88DF45194FD706BC317B515ACE7C; \
    case "${TARGETARCH}" in \
        amd64) arch=x86_64 sum=${CALIBRE_SHA256_X86_64} ;; \
        arm64) arch=arm64 sum=${CALIBRE_SHA256_ARM64} ;; \
        *) echo "ERROR: Calibre publishes no bundle for ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    export GNUPGHOME=/tmp/gnupg; \
    install -d -m 0700 "${GNUPGHOME}"; \
    keys=$(gpg --batch --with-colons --show-keys /tmp/calibre-release.asc \
        | awk -F: '$1 == "pub" { primary = 1; next } $1 == "fpr" && primary { print $10; primary = 0 }' | paste -sd ' ' -); \
    if [[ "${keys}" != "${key}" ]]; then \
        echo "ERROR: keys/calibre-release.asc holds the keys '${keys}', not Calibre's release key ${key} alone." >&2; \
        echo "If Calibre's release key changed, check the new key and replace keys/calibre-release.asc and the fingerprint above." >&2; \
        exit 1; \
    fi; \
    gpg --batch --dearmor -o /tmp/calibre-release.gpg /tmp/calibre-release.asc; \
    file=calibre-${CALIBRE_VERSION}-${arch}.txz; \
    retry curl -fsSL --proto '=https' --tlsv1.2 -o "/tmp/${file}" "https://download.calibre-ebook.com/${CALIBRE_VERSION}/${file}"; \
    echo "${sum}  /tmp/${file}" | sha256sum -c --quiet -; \
    retry curl -fsSL --proto '=https' --tlsv1.2 -o "/tmp/${file}.sig" "https://calibre-ebook.com/signatures/${file}.sig"; \
    if ! gpgv --status-fd 1 --keyring /tmp/calibre-release.gpg "/tmp/${file}.sig" "/tmp/${file}" >/tmp/gpgv \
        || ! grep -Eq "^\[GNUPG:\] VALIDSIG ([^ ]+ ){9}${key}\$" /tmp/gpgv; then \
        cat /tmp/gpgv >&2; \
        echo "ERROR: ${file} is not signed by Calibre's release key ${key}" >&2; \
        exit 1; \
    fi; \
    install -d /opt/calibre; \
    tar -xJf "/tmp/${file}" -C /opt/calibre; \
    rm "/tmp/${file}" "/tmp/${file}.sig"; \
    gpg --batch --quiet --import /tmp/calibre-release.asc; \
    echo "${key}:6:" | gpg --batch --quiet --import-ownertrust; \
    tag="v${CALIBRE_VERSION}"; \
    git init -q /tmp/src; \
    git -C /tmp/src remote add origin https://github.com/kovidgoyal/calibre.git; \
    retry git -C /tmp/src fetch -q --depth 1 --filter=blob:none origin "refs/tags/${tag}:refs/tags/${tag}"; \
    if ! git -C /tmp/src verify-tag --raw "${tag}" 2>/tmp/tagsig \
        || ! grep -Eq "^\[GNUPG:\] VALIDSIG ([^ ]+ ){9}${key}\$" /tmp/tagsig; then \
        cat /tmp/tagsig >&2; \
        echo "ERROR: Calibre's tag ${tag} is not signed by Calibre's release key ${key}" >&2; \
        exit 1; \
    fi; \
    retry git -C /tmp/src show "${tag}:bypy/sources.json" >/sources.json; \
    gpgconf --kill gpg-agent; \
    rm -rf /tmp/src "${GNUPGHOME}" /tmp/calibre-release.* /tmp/gpgv /tmp/tagsig

# Calibre-Web at its commit, from the archive GitHub makes of it, held to
# its sha256: Calibre-Web signs neither its commits nor its tags. Its own
# updater is turned off, as upstream's packaged builds do, by the one line
# that sets it, and the build fails unless exactly that line changed. The
# archive fills in the commit it was made from, which the build checks,
# since Calibre-Web reports it as its version. The tests and GitHub's
# files stay out.
FROM toolchain AS calibreweb
ARG CALIBRE_WEB_COMMIT
ARG CALIBRE_WEB_SHA256
ADD --checksum=sha256:${CALIBRE_WEB_SHA256} https://github.com/janeczku/calibre-web/archive/${CALIBRE_WEB_COMMIT}.tar.gz /tmp/calibre-web.tar.gz
SHELL ["/bin/bash", "-euo", "pipefail", "-c"]
RUN install -d /app/calibre-web \
    && tar -xzf /tmp/calibre-web.tar.gz -C /app/calibre-web --strip-components=1 \
    && constants=/app/calibre-web/cps/constants.py \
    && cp "${constants}" /tmp/constants.py \
    && sed -i 's/^UPDATER_AVAILABLE = True$/UPDATER_AVAILABLE = False/' "${constants}" \
    && { diff /tmp/constants.py "${constants}" >/tmp/diff || true; } \
    && changed=$(grep -c '^>' /tmp/diff || true) \
    && if [[ "${changed}" != 1 ]] || ! grep -qx 'UPDATER_AVAILABLE = False' "${constants}"; then \
        echo "ERROR: turning off Calibre-Web's updater changed ${changed} lines of cps/constants.py, not the one that sets UPDATER_AVAILABLE" >&2; \
        exit 1; \
    fi \
    && if ! grep -qx "NIGHTLY_VERSION\[0\] = '${CALIBRE_WEB_COMMIT}'" "${constants}"; then \
        echo "ERROR: cps/constants.py does not name the commit ${CALIBRE_WEB_COMMIT}" >&2; \
        exit 1; \
    fi \
    && rm -rf /app/calibre-web/test /app/calibre-web/.github /tmp/calibre-web.tar.gz /tmp/constants.py /tmp/diff

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
# never ships. A few packages Calibre-Web also runs are needed to build
# others, so they go into that venv too, from the files requirements.lock
# names: setuptools and packaging, which most backends import; cffi and
# the pycparser it needs, which cryptography's build calls; and greenlet,
# whose headers gevent's build reads. cffi and greenlet are built from
# source first. Then, with no network at all, every source release into a
# wheel. --check-build-dependencies holds each build to what its
# pyproject.toml asks for, from that venv, whose bin/ comes first on PATH,
# since maturin, cmake and ninja are run as programs. A Rust crate that
# links OpenSSL takes DHI's, never one it vendors.
RUN --network=none python3 -m venv /opt/build \
    && /opt/build/bin/pip install --no-index --find-links /dl --no-deps --require-hashes -r /src/python/build.lock \
    && /opt/build/bin/pip install --no-index --no-deps /dl/setuptools-*.whl /dl/packaging-*.whl /dl/pycparser-*.whl \
    && PATH="/opt/build/bin:${PATH}" /opt/build/bin/pip wheel --no-index --no-deps --no-build-isolation \
        --check-build-dependencies -w /wheels /dl/cffi-*.tar.gz /dl/greenlet-*.tar.gz \
    && /opt/build/bin/pip install --no-index --no-deps /wheels/cffi-*.whl /wheels/greenlet-*.whl
RUN --network=none PATH="/opt/build/bin:${PATH}" CARGO_HOME=/cargo CARGO_NET_OFFLINE=true OPENSSL_NO_VENDOR=1 \
    /opt/build/bin/pip wheel --no-index --find-links /dl --no-deps --no-build-isolation --check-build-dependencies \
        --require-hashes -w /wheels -r /src/python/requirements.lock
# The app's venv, at the path it has in the runtime, with no pip of its
# own: every wheel installed into it by the build venv's pip, then pip's
# own check that each package's requirements are met there. setuptools is
# one of them, since pycountry asks for it, but neither pip nor wheel is.
RUN --network=none python3 -m venv --without-pip /app/venv \
    && /opt/build/bin/pip --python /app/venv/bin/python install --no-index --no-deps /wheels/*.whl \
    && /opt/build/bin/pip --python /app/venv/bin/python check \
    && site=$(/app/venv/bin/python -c 'import sysconfig; print(sysconfig.get_path("purelib"))') \
    && for tool in pip wheel; do \
        if [[ -e "${site}/${tool}" ]]; then echo "ERROR: the venv holds ${tool}" >&2; exit 1; fi; \
    done
# Bytecode for -OO, compiled now since the root is read-only at run time.
# A hash-based .pyc that is never checked against its source needs no
# timestamp, so the same sources give the same files.
COPY --from=calibreweb /app/calibre-web /app/calibre-web
RUN --network=none python3 -m compileall -q -j0 -o 2 --invalidation-mode unchecked-hash /app/venv/lib /app/calibre-web

# Ghostscript, through which ImageMagick reads a PDF, as Calibre-Web does
# for a PDF's cover: from its release tarball, held to its sha256, with a
# stack protector, FORTIFY_SOURCE, PIE and full RELRO. It is built as a
# library, which ImageMagick calls in its own process: ImageMagick runs
# the gs program only through a shell, which the image does not have. The image libraries
# it can take from DHI it takes from DHI, so the scan and DHI's updates
# cover them, and its own copies of them are deleted first; it keeps its
# own jbig2dec, which DHI does not build, and the SBOM names. No X, no
# printing, no system font list, and nothing it would only use for OCR or
# printer drivers. The library ships with its resources compiled in, and
# the gs program that runs it; its headers go only to ImageMagick's build.
FROM toolchain AS ghostscript
ARG GHOSTSCRIPT_VERSION
ARG GHOSTSCRIPT_TAG
ARG GHOSTSCRIPT_SHA256
ADD --checksum=sha256:${GHOSTSCRIPT_SHA256} https://github.com/ArtifexSoftware/ghostpdl-downloads/releases/download/gs${GHOSTSCRIPT_TAG}/ghostscript-${GHOSTSCRIPT_VERSION}.tar.xz /tmp/ghostscript.tar.xz
COPY --from=runtimelibs /out/usr/lib /runtime/usr/lib
SHELL ["/bin/bash", "-euo", "pipefail", "-c"]
RUN install -d /src \
    && tar -xJf /tmp/ghostscript.tar.xz -C /src --strip-components=1 \
    && rm -rf /tmp/ghostscript.tar.xz /src/brotli /src/freetype /src/jpeg /src/lcms2mt /src/libpng /src/openjpeg /src/tiff /src/zlib
WORKDIR /src
RUN ./configure --prefix=/opt/ghostscript --without-x --disable-cups --disable-dbus --disable-gtk --disable-fontconfig \
        --without-libidn --without-libpaper --without-ijs --without-tesseract --without-pdftoraster --without-urf \
        --with-system-libtiff \
        CFLAGS="-O2 -D_FORTIFY_SOURCE=3 -fstack-protector-strong" \
        LDFLAGS="-Wl,-z,relro,-z,now -Wl,-rpath,/opt/ghostscript/lib" \
    && make -j"$(nproc)" so \
    && so=$(find sobin -maxdepth 1 -type f -name 'libgs.so.*.*' -printf '%f\n') \
    && install -D -m 0755 "sobin/${so}" "/opt/ghostscript/lib/${so}" \
    && ln -s "${so}" "/opt/ghostscript/lib/${so%.*}" \
    && ln -s "${so}" /opt/ghostscript/lib/libgs.so \
    && install -D -m 0755 sobin/gsc /opt/ghostscript/bin/gs \
    && install -D -m 0644 -t /out/include/ghostscript psi/iapi.h psi/ierrors.h base/gserrors.h devices/gdevdsp.h \
    && install -d /runtime/opt \
    && cp -a /opt/ghostscript /runtime/opt/ \
    && check-elf /opt/ghostscript/bin/gs \
    && check-elf "/opt/ghostscript/lib/${so}" \
    && /opt/ghostscript/bin/gs -h >/tmp/banner \
    && grep -m1 "Ghostscript ${GHOSTSCRIPT_VERSION} " /tmp/banner \
    && test "$(/opt/ghostscript/bin/gs --version)" = "${GHOSTSCRIPT_VERSION}" \
    && install -D -m 0644 LICENSE /out/doc/LICENSE \
    && install -D -m 0644 jbig2dec/COPYING /out/doc/jbig2dec/COPYING \
    && read -r major minor < <(awk '/#define JBIG2_VERSION_MAJOR/ { gsub(/[()]/, "", $3); M = $3 } /#define JBIG2_VERSION_MINOR/ { gsub(/[()]/, "", $3); m = $3 } END { print M, m }' jbig2dec/jbig2.h) \
    && printf '%s.%s\n' "${major}" "${minor}" >/out/jbig2dec.version

# ImageMagick, which Calibre-Web reaches through Wand for covers and
# thumbnails: from its GitHub repository by commit, with the tag checked
# as 7-Zip's is in SABnzbd's image. Only the delegates Calibre-Web's
# covers need: PDF, through the library of the Ghostscript built above,
# then JPEG, PNG, WebP, TIFF and FreeType from DHI's libraries, and
# libxml2, for the XMP metadata a PDF can carry; GIF needs none. No
# OpenMP, no command-line programs and no loadable modules: the libraries
# alone, with the same hardening as Ghostscript's, under /opt/imagemagick,
# which MAGICK_HOME names for Wand. Its policy is imagemagick/policy.xml,
# which denies every format but those Calibre-Web uses.
FROM toolchain AS imagemagick
ARG IMAGEMAGICK_VERSION
ARG IMAGEMAGICK_COMMIT
COPY --from=runtimelibs /out/usr/lib /runtime/usr/lib
COPY --from=ghostscript /opt/ghostscript /opt/ghostscript
COPY --from=ghostscript /out/include /tmp/ghostscript/include
COPY imagemagick/policy.xml /tmp/policy.xml
SHELL ["/bin/bash", "-euo", "pipefail", "-c"]
RUN retry() { \
        for delay in 10 30 90; do "$@" && return 0; sleep "${delay}"; done; \
        "$@"; \
    }; \
    git init -q /src; \
    git -C /src remote add origin https://github.com/ImageMagick/ImageMagick.git; \
    retry git -C /src fetch -q --depth 1 origin "${IMAGEMAGICK_COMMIT}"; \
    git -C /src checkout -q --detach FETCH_HEAD; \
    head=$(git -C /src rev-parse HEAD); \
    test "${head}" = "${IMAGEMAGICK_COMMIT}"; \
    retry git -C /src ls-remote origin \
        "refs/tags/${IMAGEMAGICK_VERSION}" "refs/tags/${IMAGEMAGICK_VERSION}^{}" >/tmp/tag; \
    tagged=$(awk -v tag="refs/tags/${IMAGEMAGICK_VERSION}" \
        '$2 == tag "^{}" { peeled = $1 } $2 == tag { plain = $1 } END { print (peeled != "" ? peeled : plain) }' /tmp/tag); \
    if [[ "${tagged}" != "${IMAGEMAGICK_COMMIT}" ]]; then \
        echo "tag ${IMAGEMAGICK_VERSION} names '${tagged}', the Containerfile pins ${IMAGEMAGICK_COMMIT}" >&2; \
        exit 1; \
    fi; \
    rm -rf /src/.git /tmp/tag
WORKDIR /src
RUN ./configure --prefix=/opt/imagemagick --disable-static --enable-shared --without-modules --without-utilities \
        --disable-openmp --disable-opencl --disable-docs --without-perl --without-magick-plus-plus --without-x \
        --with-freetype --with-jpeg --with-png --with-tiff --with-webp --with-zlib \
        --without-bzlib --without-djvu --without-dmr --without-fftw --without-flif --without-fontconfig --without-fpx \
        --with-gslib --without-gvc --without-heic --without-jbig --without-jxl --without-lcms --without-lqr \
        --without-lzma --without-openexr --without-openjp2 --without-pango --without-raqm --without-raw --without-rsvg \
        --without-uhdr --without-wmf --with-xml --without-zip --without-zstd \
        CPPFLAGS="-I/tmp/ghostscript/include" \
        CFLAGS="-O2 -D_FORTIFY_SOURCE=3 -fstack-protector-strong" \
        LDFLAGS="-L/opt/ghostscript/lib -Wl,-rpath,/opt/ghostscript/lib -Wl,-z,relro,-z,now" \
    && make -j"$(nproc)" \
    && make install \
    && grep -q '#define MAGICKCORE_GS_DELEGATE 1' /opt/imagemagick/include/ImageMagick-7/MagickCore/magick-baseconfig.h \
    && grep -q '#define MAGICKCORE_XML_DELEGATE 1' /opt/imagemagick/include/ImageMagick-7/MagickCore/magick-baseconfig.h \
    && install -m 0644 /tmp/policy.xml /opt/imagemagick/etc/ImageMagick-7/policy.xml \
    && rm -rf /opt/imagemagick/bin /opt/imagemagick/include /opt/imagemagick/share/doc /opt/imagemagick/share/man \
        /opt/imagemagick/lib/pkgconfig /opt/imagemagick/lib/*.la \
    && install -d /runtime/opt \
    && cp -a /opt/imagemagick /opt/ghostscript /runtime/opt/ \
    && find /opt/imagemagick/lib -maxdepth 1 -type f -name 'libMagick*.so.*' -print0 | xargs -0 -n1 check-elf \
    && install -D -m 0644 -t /out/doc LICENSE NOTICE

# kepubify, which Calibre-Web runs to send a book to a Kobo: from its
# GitHub repository by commit, with the tag checked as ImageMagick's is,
# its Go modules fetched by go.sum, then built with no network, static,
# with its version set as upstream's release builds set it.
FROM dhi.io/golang:1.27.4-debian13-dev@sha256:121afb8b390b1d6babba8cd0a93eaf2ffa3735f835d388b9d79c2be38b41ee4f AS kepubify
ARG KEPUBIFY_VERSION
ARG KEPUBIFY_COMMIT
SHELL ["/bin/bash", "-euo", "pipefail", "-c"]
RUN retry() { \
        for delay in 10 30 90; do "$@" && return 0; sleep "${delay}"; done; \
        "$@"; \
    }; \
    tag="v${KEPUBIFY_VERSION}"; \
    git init -q /src; \
    git -C /src remote add origin https://github.com/pgaskin/kepubify.git; \
    retry git -C /src fetch -q --depth 1 origin "${KEPUBIFY_COMMIT}"; \
    git -C /src checkout -q --detach FETCH_HEAD; \
    head=$(git -C /src rev-parse HEAD); \
    test "${head}" = "${KEPUBIFY_COMMIT}"; \
    retry git -C /src ls-remote origin "refs/tags/${tag}" "refs/tags/${tag}^{}" >/tmp/tag; \
    tagged=$(awk -v tag="refs/tags/${tag}" \
        '$2 == tag "^{}" { peeled = $1 } $2 == tag { plain = $1 } END { print (peeled != "" ? peeled : plain) }' /tmp/tag); \
    if [[ "${tagged}" != "${KEPUBIFY_COMMIT}" ]]; then \
        echo "tag ${tag} names '${tagged}', the Containerfile pins ${KEPUBIFY_COMMIT}" >&2; \
        exit 1; \
    fi; \
    rm -rf /src/.git /tmp/tag
WORKDIR /src
RUN retry() { \
        for delay in 10 30 90; do "$@" && return 0; sleep "${delay}"; done; \
        "$@"; \
    }; \
    GOFLAGS=-mod=readonly GOTOOLCHAIN=local retry go mod download
RUN --network=none CGO_ENABLED=0 GOFLAGS=-mod=readonly GOTOOLCHAIN=local \
    go build -trimpath -ldflags "-s -w -X github.com/pgaskin/kepubify/v5/internal.Version=v${KEPUBIFY_VERSION}" \
        -o /out/kepubify . \
    && /out/kepubify --version >/tmp/version \
    && grep -qx "kepubify v${KEPUBIFY_VERSION}" /tmp/version \
    && install -D -m 0644 LICENSE /out/doc/LICENSE

# unrar, RARLAB's own, which Calibre-Web runs to read CBR comics: as
# SABnzbd's image builds it, from the tarball, held to its sha256, linked
# to the runtime's glibc and libstdc++, so the scan and DHI's updates
# cover them, with a stack protector, FORTIFY_SOURCE, PIE and full RELRO.
# upstream's -std=c++11 stays, since setting CXXFLAGS replaces its flags.
# Its version, from version.hpp, goes to the SBOM.
FROM toolchain AS unrar
ARG UNRAR_VERSION
ARG UNRAR_SHA256
ADD --checksum=sha256:${UNRAR_SHA256} https://www.rarlab.com/rar/unrarsrc-${UNRAR_VERSION}.tar.gz /tmp/unrar.tar.gz
SHELL ["/bin/bash", "-euo", "pipefail", "-c"]
RUN tar -xzf /tmp/unrar.tar.gz -C /tmp \
    && make -C /tmp/unrar -j"$(nproc)" unrar \
        CXXFLAGS="-O2 -std=c++11 -D_FORTIFY_SOURCE=3 -fstack-protector-strong -fPIE" \
        LDFLAGS="-pthread -pie -Wl,-z,relro,-z,now" \
    && install -D -m 0755 /tmp/unrar/unrar /out/unrar \
    && install -D -m 0644 /tmp/unrar/license.txt /out/doc/license.txt \
    && check-elf /out/unrar \
    && { /out/unrar || true; } >/tmp/banner \
    && grep -m1 'Alexander Roshal' /tmp/banner \
    && read -r major minor beta < <(awk '/#define RARVER_MAJOR/ {M=$3} /#define RARVER_MINOR/ {m=$3} /#define RARVER_BETA/ {b=$3} END {print M, m, b}' /tmp/unrar/version.hpp) \
    && printf '%s %s %s\n' "${major}" "${minor}" "${beta}" >/out/unrar.version

# The SBOMs of what no scanner sees in the image, each component by the
# identifier the scan matches it by, which ci's divbast-scan reads through
# the labels below: the libraries Calibre's bundle carries, from its
# sources.json, which the scan reports and never blocks on, since DHI's
# updates never reach them; and the four tools built here, with the
# jbig2dec inside Ghostscript.
FROM toolchain AS sbom
ARG IMAGEMAGICK_VERSION
ARG GHOSTSCRIPT_VERSION
ARG KEPUBIFY_VERSION
COPY --from=calibre /sources.json /tmp/sources.json
COPY --from=unrar /out/unrar.version /tmp/unrar.version
COPY --from=ghostscript /out/jbig2dec.version /tmp/jbig2dec.version
COPY sbom/sboms.py /tmp/sboms.py
SHELL ["/bin/bash", "-euo", "pipefail", "-c"]
RUN read -r major minor beta </tmp/unrar.version \
    && read -r jbig2dec </tmp/jbig2dec.version \
    && install -d /out \
    && python3 -I /tmp/sboms.py \
        --sources /tmp/sources.json \
        --unrar "${major}.${minor}" --unrar-beta "${beta}" \
        --imagemagick "${IMAGEMAGICK_VERSION}" \
        --ghostscript "${GHOSTSCRIPT_VERSION}" \
        --jbig2dec "${jbig2dec}" \
        --kepubify "${KEPUBIFY_VERSION}" \
        --out /out

# The image's own command, PID 1: it starts both apps, serves both ports,
# keeps them in step, and is the HEALTHCHECK's probe and the calibredb
# Calibre-Web runs. Static, on the standard library. Calibre-Web finds
# its two Calibre programs in /opt/calibre-bin: ebook-convert is
# Calibre's, and calibredb is this command.
FROM dhi.io/golang:1.27.4-debian13-dev@sha256:121afb8b390b1d6babba8cd0a93eaf2ffa3735f835d388b9d79c2be38b41ee4f AS probe
ARG CALIBRE_VERSION
ARG CALIBRE_WEB_COMMIT
WORKDIR /src
# go.mod's replace names ./.ci, which the build context leaves out: the
# command imports nothing from divbast/ci, so Go never looks for it
COPY go.mod ./
COPY cmd/divbast-calibre/ cmd/divbast-calibre/
COPY internal/calibredb/ internal/calibredb/
COPY internal/firststart/ internal/firststart/
COPY internal/probe/ internal/probe/
COPY internal/proxy/ internal/proxy/
COPY internal/reload/ internal/reload/
COPY internal/supervise/ internal/supervise/
RUN CGO_ENABLED=0 GOFLAGS=-mod=readonly GOTOOLCHAIN=local \
    go build -trimpath -ldflags "-s -w -X main.version=${CALIBRE_VERSION} -X main.cwCommit=${CALIBRE_WEB_COMMIT}" \
    -o /out/divbast-calibre ./cmd/divbast-calibre \
    && install -d /out/config /out/books /out/opt/calibre-bin \
    && ln -s /usr/local/bin/divbast-calibre /out/opt/calibre-bin/calibredb \
    && ln -s /opt/calibre/ebook-convert /out/opt/calibre-bin/ebook-convert

# The last check before the image: its tree, the runtime with every
# native file the image adds, put together under /check, where each ELF
# file must find every library it needs, in a chroot, as ldd lists them:
# the runtime's own loader, with LD_TRACE_LOADED_OBJECTS set through the
# toolchain's env, since the runtime has none. Calibre's launchers put the
# bundle's lib/ first on the library path of every program they start, so
# the check does too, for the bundle's files. A static file needs nothing,
# and a file built for another arch, as one of selenium's, is never run.
# Left out, by name, are the 19 files of the bundle that need a library
# neither the bundle nor DHI ships, all for a display, a device or speech
# that a server has none of: Qt's X11 plugin and its library and GL
# integrations (libxcb-cursor, -icccm, -image, -keysyms, -render-util);
# the framebuffer and Vulkan display plugins (libinput, libmtdev); the
# EGLFS plugin and its integrations (Qt's EGLFS libraries); the Wayland
# wl-shell plugin (Qt's WlShellIntegration); the speech-dispatcher plugin
# (libspeechd); and the PyQt6 modules, with Qt's PDF image plugin, for the
# Qt PDF, PDF widgets, Quick Controls, SVG widgets and Test libraries the
# bundle does not ship. Every other file must find all it needs, so a new
# file in a Calibre update that misses a library fails the build. A
# package file that would replace one of the runtime's fails too. Then
# Ghostscript, and the libraries Python loads by name rather than by
# link, libmagic and ImageMagick's, are run there once, and a format the
# policy denies must be refused. Calibre's launchers need /proc, which a
# build cannot mount there, so Calibre first runs in the image's tests.
# The image takes the packages from this stage, so none ships unless the
# check passed.
FROM runtimelibs AS libcheck
ARG GHOSTSCRIPT_VERSION
ARG IMAGEMAGICK_VERSION
COPY --from=runtime / /check/
COPY --from=calibre /opt/calibre /check/opt/calibre
COPY --from=deps /app /check/app
COPY --from=ghostscript /opt/ghostscript /check/opt/ghostscript
COPY --from=imagemagick /opt/imagemagick /check/opt/imagemagick
COPY --from=unrar /out/unrar /check/usr/local/bin/unrar
SHELL ["/bin/bash", "-euo", "pipefail", "-c"]
RUN while read -r f; do \
        if [[ -e "/check/${f}" || -L "/check/${f}" ]]; then \
            echo "ERROR: the runtime packages would replace the runtime's own /${f}" >&2; \
            exit 1; \
        fi; \
    done < <(find /out ! -type d -printf '%P\n'); \
    cp -a /out/. /check/; \
    cp /usr/bin/env /check/tmp/env; \
    interp=$(readelf -lW /check/opt/calibre/calibre-server | sed -n 's/.*Requesting program interpreter: \(.*\)\]$/\1/p'); \
    machine=$(readelf -h /check/opt/calibre/calibre-server | sed -n 's/^ *Machine: *//p'); \
    printf '\177ELF' >/tmp/elf; \
    files=0; \
    exempt=0; \
    : >/tmp/missing; \
    while IFS= read -r -d '' f; do \
        cmp -s -n 4 "${f}" /tmp/elf || continue; \
        [[ "$(readelf -h "${f}" | sed -n 's/^ *Machine: *//p')" == "${machine}" ]] || continue; \
        readelf -d "${f}" 2>/dev/null | grep -q '(NEEDED)' || continue; \
        path=${f#/check}; \
        case "${path#/opt/calibre/}" in \
            lib/libQt6XcbQpa.so.6 | plugins/platforms/libqxcb.so \
            | plugins/xcbglintegrations/libqxcb-egl-integration.so \
            | plugins/xcbglintegrations/libqxcb-glx-integration.so \
            | plugins/platforms/libqlinuxfb.so | plugins/platforms/libqvkkhrdisplay.so \
            | plugins/platforms/libqeglfs.so | plugins/egldeviceintegrations/libqeglfs-emu-integration.so \
            | plugins/egldeviceintegrations/libqeglfs-kms-egldevice-integration.so \
            | plugins/egldeviceintegrations/libqeglfs-kms-integration.so \
            | plugins/egldeviceintegrations/libqeglfs-x11-integration.so \
            | plugins/wayland-shell-integration/libwl-shell-plugin.so \
            | plugins/texttospeech/libqtexttospeech_speechd.so \
            | lib/calibre-extensions/PyQt6.QtPdf.so | lib/calibre-extensions/PyQt6.QtPdfWidgets.so \
            | plugins/imageformats/libqpdf.so | lib/calibre-extensions/PyQt6.QtQuickControls2.so \
            | lib/calibre-extensions/PyQt6.QtSvgWidgets.so | lib/calibre-extensions/PyQt6.QtTest.so) \
                exempt=$((exempt + 1)); \
                continue ;; \
        esac; \
        files=$((files + 1)); \
        libs=(); \
        if [[ "${path}" == /opt/calibre/* ]]; then libs=(LD_LIBRARY_PATH=/opt/calibre/lib); fi; \
        if ! chroot /check /tmp/env LD_TRACE_LOADED_OBJECTS=1 "${libs[@]}" "${interp}" "${path}" >/tmp/list 2>&1; then \
            echo "${path}: the loader failed on it" >>/tmp/missing; \
            sed "s|^|${path}: |" /tmp/list >>/tmp/missing; \
        fi; \
        grep 'not found' /tmp/list | sed "s|^|${path}: |" >>/tmp/missing || true; \
    done < <(find /check/opt /check/app /check/usr/local/bin -type f \( -name '*.so' -o -name '*.so.*' -o -perm -u+x \) -print0); \
    if [[ -s /tmp/missing ]]; then \
        sort -u /tmp/missing >&2; \
        echo "ERROR: these files need libraries the image does not ship" >&2; \
        exit 1; \
    fi; \
    echo "${files} ELF files find every library they need; ${exempt} of the bundle's are left out"; \
    test "$(chroot /check /opt/ghostscript/bin/gs --version)" = "${GHOSTSCRIPT_VERSION}"; \
    printf '%s\n' \
        'import magic, wand.version' \
        'from wand.exceptions import PolicyError' \
        'from wand.image import Image' \
        'print(magic.from_buffer(b"%PDF-1.4"), wand.version.MAGICK_VERSION)' \
        'try:' \
        '    Image(blob=b"<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"1\" height=\"1\"/>", format="svg")' \
        'except PolicyError:' \
        '    print("the policy refuses SVG")' \
        'except Exception as e:' \
        '    raise SystemExit(f"ERROR: the ImageMagick policy lets SVG through, to fail as {e}") from None' \
        'else:' \
        '    raise SystemExit("ERROR: the ImageMagick policy lets SVG through")' \
        >/check/tmp/imagemagick.py; \
    MAGICK_HOME=/opt/imagemagick chroot /check /app/venv/bin/python -I /tmp/imagemagick.py >/tmp/loaded; \
    cat /tmp/loaded; \
    grep -q "ImageMagick ${IMAGEMAGICK_VERSION} " /tmp/loaded; \
    rm -rf /check /tmp/elf /tmp/list /tmp/missing /tmp/loaded

FROM runtime
ARG CALIBRE_VERSION

# Root-owned, and never written: the apps write to /config, /books and
# /tmp alone. The runtime's extra packages come from the stage that
# checked them, with their entries in /var/lib/dpkg/status.d.
COPY --from=libcheck /out /
COPY --from=calibre /opt/calibre /opt/calibre
COPY --from=deps /app/venv /app/venv
COPY --from=deps /app/calibre-web /app/calibre-web
COPY --from=ghostscript /opt/ghostscript /opt/ghostscript
COPY --from=imagemagick /opt/imagemagick /opt/imagemagick
COPY --from=kepubify /out/kepubify /usr/local/bin/kepubify
COPY --from=unrar /out/unrar /usr/local/bin/unrar
# The licences the four tools ship under, beside the binaries' own notices
COPY --from=ghostscript /out/doc /usr/local/share/doc/ghostscript
COPY --from=imagemagick /out/doc /usr/local/share/doc/imagemagick
COPY --from=kepubify /out/doc /usr/local/share/doc/kepubify
COPY --from=unrar /out/doc /usr/local/share/doc/unrar
COPY --from=sbom /out/calibre-bundle.cdx.json /out/tools.cdx.json /usr/local/share/divbast/
# The first-start helpers PID 1 runs, one in Calibre's Python and one in
# Calibre-Web's
COPY firstrun/calibre_users.py firstrun/calibre_web_init.py /usr/local/share/divbast-calibre/
COPY --from=probe /out/divbast-calibre /usr/local/bin/divbast-calibre
COPY --from=probe /out/opt/calibre-bin /opt/calibre-bin
# The library and the settings, empty, uid 65532's, and not VOLUMEs: a
# new named volume mounted on either takes its owner from it, so the apps
# can write to it.
COPY --from=probe --chown=65532:65532 /out/config /config
COPY --from=probe --chown=65532:65532 /out/books /books

# UTF-8 for both apps and the tools they run; no .pyc written at run
# time, on a read-only root, which the build compiled already; and each
# log line to the engine as it is written. Calibre keeps its settings in
# /config/calibre, its temporary files and its cache in /tmp; Calibre-Web
# its cache in /config/calibre-web. Wand finds ImageMagick by MAGICK_HOME.
ENV LC_ALL=C.UTF-8 \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    CALIBRE_CONFIG_DIRECTORY=/config/calibre \
    CALIBRE_TEMP_DIR=/tmp \
    CALIBRE_CACHE_DIRECTORY=/tmp/calibre-cache \
    CACHE_DIRECTORY=/config/calibre-web/cache \
    MAGICK_HOME=/opt/imagemagick

# The licences of everything the image ships beyond its base and DHI's
# packages, whose own are in /usr/share/doc: this repository's, Calibre's
# and the libraries its bundle carries, Calibre-Web's, each Python
# package's, Ghostscript's, ImageMagick's, kepubify's and its Go modules',
# and unrar's. Which file carries which is in each package's dist-info, in
# the bundle's SBOM, and in /usr/local/share/doc for the tools. The SBOM
# labels are what ci's scan reads: it reports the bundle's findings, and
# blocks on none of them.
LABEL org.opencontainers.image.source="https://github.com/divbast/calibre-oci-image" \
    org.opencontainers.image.url="https://github.com/divbast/calibre-oci-image" \
    org.opencontainers.image.title="Calibre and Calibre-Web" \
    org.opencontainers.image.version="${CALIBRE_VERSION}" \
    org.opencontainers.image.licenses="GPL-3.0-only AND 0BSD AND AGPL-3.0-or-later AND Apache-2.0 AND blessing AND BSD-2-Clause AND BSD-3-Clause AND bzip2-1.0.6 AND CNRI-Python AND GPL-2.0-or-later AND GPL-3.0-or-later AND IJG AND ImageMagick AND ISC AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-2.1-or-later AND LGPL-3.0-only AND libpng-2.0 AND libtiff AND LicenseRef-Public-Domain AND LicenseRef-UnRAR AND MIT AND MIT-0 AND MIT-CMU AND MPL-2.0 AND OFL-1.1 AND PSF-2.0 AND python-ldap AND Unicode-3.0 AND Unlicense AND X11-distribute-modifications-variant AND Zlib AND ZPL-2.1 AND (0BSD OR CC0-1.0) AND (AFL-2.1 OR GPL-2.0-or-later) AND (Apache-2.0 OR BSD-2-Clause) AND (Apache-2.0 OR BSD-3-Clause) AND (Apache-2.0 OR MIT) AND (BSD-2-Clause OR GPL-2.0-or-later) AND (BSD-3-Clause OR GPL-2.0-only) AND (GPL-2.0-only OR GPL-3.0-only) AND (GPL-2.0-or-later OR LGPL-2.1-or-later OR MPL-1.1) AND (GPL-2.0-or-later OR LGPL-2.1-or-later OR MPL-2.0)" \
    io.github.divbast.scan.sboms="/usr/local/share/divbast/calibre-bundle.cdx.json,/usr/local/share/divbast/tools.cdx.json" \
    io.github.divbast.scan.sboms.advisory="/usr/local/share/divbast/calibre-bundle.cdx.json"

EXPOSE 8080 8083
# Exec form: the shell form would need /bin/sh, which the image lacks.
# --timeout must clear divbast-calibre health's own budget: it allows each
# of its two app checks 4 s and runs them one after the other, so an app
# that accepts and never answers costs 8 s. A shorter timeout kills the
# probe mid-flight and records a bare engine timeout instead of the probe's
# own line, which is the only diagnostic there is. Podman reads no
# HEALTHCHECK from an image published as an OCI index: its users pass the
# same array as --health-cmd.
HEALTHCHECK --interval=30s --timeout=10s --start-period=60s --retries=3 \
    CMD ["/usr/local/bin/divbast-calibre", "health"]
USER 65532:65532
# PID 1 starts both apps and stays, handling SIGTERM and SIGINT for both.
ENTRYPOINT ["/usr/local/bin/divbast-calibre", "serve"]
