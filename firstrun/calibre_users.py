# SPDX-License-Identifier: GPL-3.0-only
# SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

"""Create the Content Server's first users, once, from JSON on stdin.

divbast-calibre runs this on first start, in Calibre's own Python, as
``calibre-debug -e calibre_users.py``, and writes this to its stdin::

    {"userdb": "<path>",
     "users": [{"name": "<name>", "password": "<password>", "readonly": false,
                "allow_change_password_via_http": true}, ...]}

It creates the user database at ``userdb`` with Calibre's own
``UserManager``, and adds every user in one transaction: either all of them
are added, or none is. The passwords come on stdin alone, never on the
command line or in the environment. Every file it creates is readable by
its own user alone, because the database holds the passwords as they are.

It prints nothing when it succeeds. When it fails, it prints one line to
stderr, the error's class and the first line of its message, and exits
with code 1. That line never holds a password or the request.
"""

import json
import os
import sys

from calibre.srv.users import UserManager  # ty: ignore[import-not-found] - in Calibre's bundle alone


def create(request: dict) -> None:
    """Create the user database the request names, holding its users."""
    users = UserManager(request["userdb"])
    with users.conn:
        for user in request["users"]:
            users.add_user(
                user["name"],
                user["password"],
                readonly=user["readonly"],
                allow_change_password_via_http=user["allow_change_password_via_http"],
            )
    users.conn.close()


def one_line(e: BaseException) -> str:
    """Return e's class and the first line of its message.

    For an error SQLAlchemy raises from the database driver, it is the
    driver's own error: SQLAlchemy's message also holds the statement and
    its parameters, which can be a password's hash.
    """
    orig = getattr(e, "orig", None)
    if isinstance(orig, BaseException):
        e = orig
    lines = str(e).splitlines()
    return f"{e.__class__.__name__}: {lines[0] if lines else ''}"


def main() -> int:
    """Read the request from stdin and create its users."""
    os.umask(0o077)
    try:
        create(json.load(sys.stdin))
    except Exception as e:  # noqa: BLE001 - one line for PID 1, never a traceback
        sys.stderr.write(f"calibre_users: {one_line(e)}\n")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
