# SPDX-License-Identifier: GPL-3.0-only
# SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

"""Write Calibre-Web's first settings and admin password, or read its login header.

divbast-calibre runs this in Calibre-Web's own Python, ``/app/venv/bin/python``,
in one of two modes.

``init``, on first start, reads this from stdin::

    {"appdb": "<path>", "password": "<password>",
     "settings": {"config_calibre_dir": "/books", ...}}

It creates Calibre-Web's settings database at ``appdb`` with Calibre-Web's
own code, ``cps.ub`` and ``cps.config_sql``, as Calibre-Web does when it
starts with no database: the users ``admin`` and ``Guest``, the encryption
key in the same directory, and a settings row with every default. Then it
gives ``admin`` the password, hashed as Calibre-Web hashes it, and writes
each setting by its column's name. With ``config_binariesdir`` it also
writes ``config_converterpath``, that folder's ``ebook-convert``, as
Calibre-Web's own settings form does. It starts no web server. The password
comes on stdin alone, never on the command line or in the environment.
Every file it creates is readable by its own user alone.

``get-login-header <appdb>`` prints the name of the header Calibre-Web reads
a user's name from when a reverse proxy logs the user in, as saved in
``appdb``, and a newline. It prints nothing when no name is saved. It
needs no password, and writes nothing to the database, but for rolling
back a transaction that a stopped Calibre-Web left unfinished, as SQLite
does on opening it: a database opened read-only cannot do that, and
cannot be read until it is done. It never creates the database.

It prints nothing else. When it fails, it prints one line to stderr, the
error's class and the first line of its message, and exits with code 1.
That line never holds a password, a hash or the request. A wrong command
line exits with code 2.
"""

import json
import os
import sqlite3
import sys
from pathlib import Path

# Calibre-Web's source tree, which is not an installed package
CALIBRE_WEB = "/app/calibre-web"

USAGE = "usage: calibre_web_init.py init < request.json | get-login-header <app.db>"


def init(request: dict) -> None:
    """Create the settings database the request names, as the request says."""
    path = request["appdb"]
    if Path(path).exists():
        msg = f"{path} exists already"
        raise FileExistsError(msg)
    sys.path.insert(0, CALIBRE_WEB)
    from cps import config_sql, constants, ub  # noqa: PLC0415  # ty: ignore[import-not-found] - Calibre-Web's tree, on sys.path from the line above
    from werkzeug.security import generate_password_hash  # noqa: PLC0415  # ty: ignore[import-not-found] - in Calibre-Web's venv alone, which only init needs

    # What Calibre-Web's create_app does with a settings path that has no file
    ub.init_db(path)
    key, error = config_sql.get_encryption_key(str(Path(path).parent))
    if error:
        raise error
    config_sql.load_configuration(ub.session, key)

    admin = ub.session.query(ub.User).filter(ub.User.name == "admin").one()
    admin.password = generate_password_hash(request["password"])
    # Calibre-Web keeps its settings in this one row. Its ConfigSQL front
    # would also look for binaries and set up its logging, which init must
    # leave to Calibre-Web's own start.
    model = config_sql._Settings  # noqa: SLF001 - the table's own model, as the comment above says
    settings = ub.session.query(model).one()
    for name, value in request["settings"].items():
        if name not in model.__table__.columns:
            msg = f"Calibre-Web has no setting {name}"
            raise KeyError(msg)
        setattr(settings, name, value)
    # Calibre-Web's settings form saves the converter with the binaries
    # folder, as that folder's ebook-convert, and nothing derives one from
    # the other when Calibre-Web starts: without it, Calibre-Web converts
    # nothing with ebook-convert.
    if "config_binariesdir" in request["settings"]:
        binaries = settings.config_binariesdir
        converter = constants.SUPPORTED_CALIBRE_BINARIES["ebook-convert"]
        settings.config_converterpath = str(Path(binaries) / converter) if binaries else ""
    ub.session.commit()
    ub.session.close()
    ub.session.get_bind().dispose()


def login_header(path: str) -> str:
    """Return the login header's name saved in the settings database at path."""
    db = sqlite3.connect(Path(path).as_uri() + "?mode=rw", uri=True)
    try:
        row = db.execute("SELECT config_reverse_proxy_login_header_name FROM settings LIMIT 1").fetchone()
    finally:
        db.close()
    if row is None:
        msg = f"{path} holds no settings"
        raise LookupError(msg)
    return (row[0] or "").strip()


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


def main(argv: list[str]) -> int:
    """Run the mode argv names."""
    os.umask(0o077)
    try:
        match argv:
            case ["init"]:
                init(json.load(sys.stdin))
            case ["get-login-header", path]:
                name = login_header(path)
                if name:
                    sys.stdout.write(name + "\n")
            case _:
                sys.stderr.write(USAGE + "\n")
                return 2
    except Exception as e:  # noqa: BLE001 - one line for PID 1, never a traceback
        sys.stderr.write(f"calibre_web_init: {one_line(e)}\n")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
