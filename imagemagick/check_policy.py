# SPDX-License-Identifier: GPL-3.0-only
# SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

"""Check, in the image's tree, that ImageMagick's policy holds.

The Containerfile's libcheck stage runs this in a chroot of the image, in
Calibre-Web's Python, with MAGICK_HOME set, as
``python -I check_policy.py <pdf>``, where <pdf> is a one-page PDF.

A PDF's first page must read, through Ghostscript, to a JPEG, as
Calibre-Web makes a PDF's cover. SVG, MSL, PostScript and EPS must be
refused by the policy, and so must writing a PDF. Any other answer, a
failure for another reason included, means the policy no longer holds them
back, and fails the build.

It prints one line for each check that holds, and exits with code 1, naming
the check, at the first that does not.
"""

import sys
lazy from collections.abc import Callable

from wand.exceptions import PolicyError  # ty: ignore[import-not-found] - in Calibre-Web's venv alone
from wand.image import Image  # ty: ignore[import-not-found] - in Calibre-Web's venv alone

# A small file of each format the policy must refuse to read
REFUSED = {
    "svg": b'<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"/>',
    "msl": b'<?xml version="1.0"?><image><read filename="x.jpg"/></image>',
    "ps": b"%!PS\nshowpage\n",
    "eps": b"%!PS-Adobe-3.0 EPSF-3.0\n%%BoundingBox: 0 0 1 1\nshowpage\n",
}


def refused(what: str, attempt: Callable[[], object]) -> None:
    """Exit unless attempt is refused by the policy."""
    try:
        attempt()
    except PolicyError:
        sys.stdout.write(f"the policy refuses {what}\n")
        return
    except Exception as e:  # noqa: BLE001 - any other failure means the policy let it through
        sys.exit(f"ERROR: the ImageMagick policy lets {what} through, to fail as {e}")
    sys.exit(f"ERROR: the ImageMagick policy lets {what} through")


def read_blob(data: bytes, fmt: str) -> None:
    """Read data as fmt."""
    with Image(blob=data, format=fmt):
        pass


def main() -> None:
    """Run each check."""
    with Image(filename=sys.argv[1] + "[0]", resolution=72) as page:
        page.format = "jpeg"
        jpeg = page.make_blob()
    if not jpeg or jpeg[:3] != b"\xff\xd8\xff":
        sys.exit("ERROR: a PDF's first page did not read to a JPEG")
    sys.stdout.write(f"a PDF's first page reads to a {len(jpeg)}-byte JPEG\n")

    for fmt, data in REFUSED.items():
        refused(f"reading {fmt.upper()}", lambda data=data, fmt=fmt: read_blob(data, fmt))

    def write_pdf() -> None:
        with Image(blob=jpeg) as img:
            img.format = "pdf"
            img.make_blob()

    refused("writing PDF", write_pdf)


if __name__ == "__main__":
    main()
