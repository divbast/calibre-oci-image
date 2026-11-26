# SPDX-License-Identifier: GPL-3.0-only
# SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

"""Write the image's two CycloneDX SBOMs, of what no scanner sees in it.

The Containerfile's sbom stage runs this, in the build's own Python, as
``python3 -I sboms.py --sources <sources.json> --unrar ... --out <dir>``.

``calibre-bundle.cdx.json`` names the libraries Calibre's build compiled
into its Linux bundle, from the bundle's ``bypy/sources.json``: every
entry built for Linux, but for the build's own tools. Each is named by the
CPE the scan matches it by, with its licence, from the table below. A
library the table does not name fails the build, with a line saying what
to add, so that no library goes unlisted.

``tools.cdx.json`` names the tools the image builds itself: ImageMagick,
Ghostscript and the jbig2dec inside it, kepubify and unrar.

It prints nothing when it succeeds. When it fails, it prints one line to
stderr and exits with code 1.
"""

import argparse
import json
import sys
from pathlib import Path
from typing import NamedTuple


class Library(NamedTuple):
    """A library's CPE vendor and product, and its licence."""

    vendor: str
    product: str
    licence: str


# The bundle's build tools, which compile it and never ship in it
BUILD_TOOLS = frozenset(
    {
        "autoconf",
        "automake",
        "cmake",
        "gnuwin32",
        "libtool",
        "nasm",
        "ninja",
        "nodejs",
        "nv-codec-headers",
    },
)

# Each library of the bundle, by its name in sources.json: the CPE vendor
# and product its advisories are published under, and its licence, as
# SPDX writes it.
LIBRARIES = {
    "bzip2": Library("bzip", "bzip2", "bzip2-1.0.6"),
    "chmlib": Library("chmlib", "chmlib", "LGPL-2.1-or-later"),
    "dav1d": Library("videolan", "dav1d", "BSD-2-Clause"),
    "dbus": Library("freedesktop", "dbus", "AFL-2.1 OR GPL-2.0-or-later"),
    "dbusglib": Library("freedesktop", "dbus-glib", "AFL-2.1 OR GPL-2.0-or-later"),
    "espeak": Library("espeak-ng", "espeak-ng", "GPL-3.0-or-later"),
    "expat": Library("libexpat_project", "libexpat", "MIT"),
    "ffmpeg": Library("ffmpeg", "ffmpeg", "LGPL-2.1-or-later"),
    "glib": Library("gnome", "glib", "LGPL-2.1-or-later"),
    "graphite": Library("sil", "graphite2", "LGPL-2.1-or-later OR MPL-2.0 OR GPL-2.0-or-later"),
    "hunspell": Library("hunspell_project", "hunspell", "MPL-1.1 OR GPL-2.0-or-later OR LGPL-2.1-or-later"),
    "hyphen": Library("hunspell_project", "hyphen", "GPL-2.0-or-later OR LGPL-2.1-or-later OR MPL-1.1"),
    "icu": Library("icu-project", "international_components_for_unicode", "Unicode-3.0"),
    "iconv": Library("gnu", "libiconv", "LGPL-2.1-or-later"),
    "jxrlib": Library("microsoft", "jxrlib", "BSD-2-Clause"),
    "lcms2": Library("littlecms", "little_cms", "MIT"),
    "libavif": Library("aomedia", "libavif", "BSD-2-Clause"),
    "libbrotli": Library("google", "brotli", "MIT"),
    "libdeflate": Library("libdeflate_project", "libdeflate", "MIT"),
    "libffi": Library("libffi_project", "libffi", "MIT"),
    "libgcrypt": Library("gnupg", "libgcrypt", "LGPL-2.1-or-later"),
    "libgpg-error": Library("gnupg", "libgpg-error", "LGPL-2.1-or-later"),
    "libhighway": Library("google", "highway", "Apache-2.0 OR BSD-3-Clause"),
    "libjbig": Library("cambridge_enterprise", "jbig-kit", "GPL-2.0-or-later"),
    "libjpeg": Library("libjpeg-turbo", "libjpeg-turbo", "IJG AND BSD-3-Clause AND Zlib"),
    "libjxl": Library("libjxl_project", "libjxl", "BSD-3-Clause"),
    "libmtp": Library("libmtp_project", "libmtp", "LGPL-2.1-or-later"),
    "libpng": Library("libpng", "libpng", "libpng-2.0"),
    "libstemmer": Library("snowballstem", "snowball", "BSD-3-Clause"),
    "libtiff": Library("libtiff", "libtiff", "libtiff"),
    "libusb": Library("libusb", "libusb", "LGPL-2.1-or-later"),
    "libwebp": Library("webmproject", "libwebp", "BSD-3-Clause"),
    "libxml2": Library("xmlsoft", "libxml2", "MIT"),
    "libxslt": Library("xmlsoft", "libxslt", "MIT"),
    "libyuv": Library("chromium", "libyuv", "BSD-3-Clause"),
    "mozjpeg": Library("mozilla", "mozjpeg", "IJG AND BSD-3-Clause AND Zlib"),
    "ncurses": Library("gnu", "ncurses", "X11-distribute-modifications-variant"),
    "onnx": Library("microsoft", "onnx_runtime", "MIT"),
    "openjpeg": Library("uclouvain", "openjpeg", "BSD-2-Clause"),
    "openssl": Library("openssl", "openssl", "Apache-2.0"),
    "optipng": Library("optipng_project", "optipng", "Zlib"),
    "podofo": Library("podofo_project", "podofo", "LGPL-2.0-or-later"),
    "poppler": Library("freedesktop", "poppler", "GPL-2.0-only OR GPL-3.0-only"),
    "python": Library("python", "python", "PSF-2.0"),
    "qt-base": Library("qt", "qt", "LGPL-3.0-only"),
    "qt-declarative": Library("qt", "qtdeclarative", "LGPL-3.0-only"),
    "qt-imageformats": Library("qt", "qtimageformats", "LGPL-3.0-only"),
    "qt-multimedia": Library("qt", "qtmultimedia", "LGPL-3.0-only"),
    "qt-positioning": Library("qt", "qtpositioning", "LGPL-3.0-only"),
    "qt-sensors": Library("qt", "qtsensors", "LGPL-3.0-only"),
    "qt-shadertools": Library("qt", "qtshadertools", "LGPL-3.0-only"),
    "qt-speech": Library("qt", "qtspeech", "LGPL-3.0-only"),
    "qt-svg": Library("qt", "qtsvg", "LGPL-3.0-only"),
    "qt-wayland": Library("qt", "qtwayland", "LGPL-3.0-only"),
    "qt-webchannel": Library("qt", "qtwebchannel", "LGPL-3.0-only"),
    "qt-webengine": Library("qt", "qtwebengine", "LGPL-3.0-only AND BSD-3-Clause"),
    "readline": Library("gnu", "readline", "GPL-3.0-or-later"),
    "speech-dispatcher-client": Library("speech-dispatcher_project", "speech-dispatcher", "LGPL-2.1-or-later"),
    "sqlite": Library("sqlite", "sqlite", "blessing"),
    "uchardet": Library("freedesktop", "uchardet", "MPL-1.1 OR GPL-2.0-or-later OR LGPL-2.1-or-later"),
    "unrar": Library("rarlab", "unrar", "LicenseRef-UnRAR"),
    "xz": Library("tukaani", "xz", "0BSD"),
    "zlib": Library("zlib", "zlib", "Zlib"),
    "zstd": Library("facebook", "zstandard", "BSD-3-Clause OR GPL-2.0-only"),
}

# Where the bundle is in the image
BUNDLE = "/opt/calibre"


class SBOMError(Exception):
    """A reason the SBOMs cannot be written."""


def cpe(vendor: str, product: str, version: str) -> str:
    """Return the CPE 2.3 name of an application at a version."""
    return f"cpe:2.3:a:{vendor}:{product}:{version}:*:*:*:*:*:*:*"


def component(name: str, version: str, identifier: dict[str, str], licence: str, path: str) -> dict[str, object]:
    """Return one CycloneDX component, with the path it is at in the image."""
    return {
        "type": "library",
        "name": name,
        "version": version,
        **identifier,
        "licenses": [{"expression": licence}],
        "properties": [{"name": "path", "value": path}],
    }


def bom(components: list[dict[str, object]]) -> dict[str, object]:
    """Return a CycloneDX 1.6 document of components."""
    return {"bomFormat": "CycloneDX", "specVersion": "1.6", "version": 1, "components": components}


def built_for_linux(entry: dict[str, object]) -> bool:
    """Say whether Calibre's build compiles a sources.json entry for Linux.

    An entry with no "os" is built for every system.
    """
    systems = entry.get("os")
    if systems is None:
        return True
    if not isinstance(systems, str):
        msg = f"sources.json entry {entry.get('name')!r} has an os that is not a string"
        raise SBOMError(msg)
    return "linux" in {s.strip() for s in systems.split(",")}


def bundle(sources: list[dict[str, object]]) -> list[dict[str, object]]:
    """Return the bundle's components, from the entries of its sources.json."""
    named: list[tuple[str, str | None]] = []
    for entry in sources:
        full = entry.get("name")
        if not isinstance(full, str) or not full.strip():
            msg = f"a sources.json entry has no name: {entry!r}"
            raise SBOMError(msg)
        name, _, version = full.strip().partition(" ")
        if name in BUILD_TOOLS or not built_for_linux(entry):
            continue
        if entry.get("type") == "build":
            msg = (
                f"sources.json lists {name}, a build tool not in BUILD_TOOLS: add it there, or to LIBRARIES if it ships"
            )
            raise SBOMError(msg)
        named.append((name, version.strip() or None))

    qt = next((v for n, v in named if n == "qt-base"), None)
    components = []
    for name, version in named:
        library = LIBRARIES.get(name)
        if library is None:
            msg = f"sources.json lists {name}, which LIBRARIES does not name: add its CPE vendor, product and licence"
            raise SBOMError(msg)
        # Qt's modules carry no version of their own: each is qt-base's
        at = version or (qt if name.startswith("qt-") else None)
        if at is None:
            msg = f"sources.json gives {name} no version"
            raise SBOMError(msg)
        components.append(
            component(name, at, {"cpe": cpe(library.vendor, library.product, at)}, library.licence, BUNDLE),
        )
    if not components:
        msg = "sources.json lists no library built for Linux"
        raise SBOMError(msg)
    return components


def tools(args: argparse.Namespace) -> list[dict[str, object]]:
    """Return the components of the tools the image builds."""
    unrar = f"{args.unrar} beta {args.unrar_beta}" if args.unrar_beta > 0 else args.unrar
    return [
        component(
            "imagemagick",
            args.imagemagick,
            {"cpe": cpe("imagemagick", "imagemagick", args.imagemagick)},
            "ImageMagick",
            "/opt/imagemagick",
        ),
        component(
            "ghostscript",
            args.ghostscript,
            {"cpe": cpe("artifex", "ghostscript", args.ghostscript)},
            "AGPL-3.0-or-later",
            "/opt/ghostscript/bin/gs",
        ),
        component(
            "jbig2dec",
            args.jbig2dec,
            {"cpe": cpe("artifex", "jbig2dec", args.jbig2dec)},
            "AGPL-3.0-or-later",
            "/opt/ghostscript/bin/gs",
        ),
        # Grype matches a Go module's advisories only by its module path,
        # as the name, and its Go version
        component(
            "github.com/pgaskin/kepubify/v5",
            f"v{args.kepubify}",
            {"purl": f"pkg:golang/github.com/pgaskin/kepubify/v5@v{args.kepubify}"},
            "MIT",
            "/usr/local/bin/kepubify",
        ),
        component(
            "unrar",
            unrar,
            {"cpe": cpe("rarlab", "unrar", args.unrar)},
            "LicenseRef-UnRAR",
            "/usr/local/bin/unrar",
        ),
    ]


def read_sources(path: Path) -> list[dict[str, object]]:
    """Return the entries of the sources.json at path."""
    sources = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(sources, list) or not all(isinstance(e, dict) for e in sources):
        msg = f"{path} is not a list of sources"
        raise SBOMError(msg)
    return sources


def write(path: Path, document: dict[str, object]) -> None:
    """Write document to path as indented JSON, ending in a newline."""
    path.write_text(json.dumps(document, indent=2) + "\n", encoding="utf-8")


def main() -> int:
    """Write both SBOMs, or say in one line why not."""
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--sources", type=Path, required=True)
    parser.add_argument("--unrar", required=True)
    parser.add_argument("--unrar-beta", type=int, required=True)
    parser.add_argument("--imagemagick", required=True)
    parser.add_argument("--ghostscript", required=True)
    parser.add_argument("--jbig2dec", required=True)
    parser.add_argument("--kepubify", required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    try:
        write(args.out / "calibre-bundle.cdx.json", bom(bundle(read_sources(args.sources))))
        write(args.out / "tools.cdx.json", bom(tools(args)))
    except (OSError, ValueError, SBOMError) as e:
        sys.stderr.write(f"sboms.py: {e}\n")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
