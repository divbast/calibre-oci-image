// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package pins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Fill is a checksum ARG of the Containerfile and the URL of the file it
// holds the build to. URL may name other ARGs as ${NAME}.
type Fill struct{ Arg, URL string }

// Fills are the downloads the image checks by sha256, none of which its
// publisher gives a checksum for. Calibre-Web's archive is GitHub's, made
// from the commit, and Calibre's bundles are checked by signature too.
var Fills = []Fill{
	{"CALIBRE_SHA256_X86_64", "https://download.calibre-ebook.com/${CALIBRE_VERSION}/calibre-${CALIBRE_VERSION}-x86_64.txz"},
	{"CALIBRE_SHA256_ARM64", "https://download.calibre-ebook.com/${CALIBRE_VERSION}/calibre-${CALIBRE_VERSION}-arm64.txz"},
	{"CALIBRE_WEB_SHA256", "https://github.com/janeczku/calibre-web/archive/${CALIBRE_WEB_COMMIT}.tar.gz"},
	{"GHOSTSCRIPT_SHA256", "https://github.com/ArtifexSoftware/ghostpdl-downloads/releases/download/gs${GHOSTSCRIPT_TAG}/ghostscript-${GHOSTSCRIPT_VERSION}.tar.xz"},
	{"UNRAR_SHA256", "https://www.rarlab.com/rar/unrarsrc-${UNRAR_VERSION}.tar.gz"},
}

// FillHashes returns the Containerfile cf with each fill's Arg set to the
// sha256 of the file at its URL, when the ARG is empty, and a note saying
// so. An ARG that is set must be what the URL serves now: when it is not,
// that is an error naming the ARG, and nothing is rewritten, since the
// file under a name must not change unseen. Moving a version means
// emptying its checksum ARGs and running the tool again.
//
// Each file is read once, whatever its size.
func FillHashes(ctx context.Context, client *http.Client, cf []byte, fills []Fill) (out []byte, notes []string, err error) {
	out = cf
	var errs []error
	for _, f := range fills {
		have, err := arg(out, f.Arg)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if have != "" && !sha256Hex.MatchString(have) {
			errs = append(errs, fmt.Errorf("the Containerfile's %s is %q, which is neither empty nor a sha256", f.Arg, have))
			continue
		}
		url, err := Expand(out, f.URL)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f.Arg, err))
			continue
		}
		sum, err := hashURL(ctx, client, url, nil)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f.Arg, err))
			continue
		}
		switch have {
		case "":
			if out, err = SetArg(out, f.Arg, sum); err != nil {
				errs = append(errs, err)
				continue
			}
			notes = append(notes, fmt.Sprintf("%s was empty: set to %s, the sha256 of %s", f.Arg, sum, url))
		case sum:
		default:
			errs = append(errs, fmt.Errorf("%s pins %s, but %s now serves a file whose sha256 is %s: the file has changed under the same name. "+
				"Check it by hand before changing the pin. If you moved the version, empty %s and run again",
				f.Arg, have, url, sum, f.Arg))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, nil, err
	}
	return out, notes, nil
}

// downloadTimeout bounds one try of a download: the bundles are hundreds
// of megabytes.
const downloadTimeout = 15 * time.Minute

// hashURL downloads u, which may be large, and returns its sha256. A try
// that fails to connect, to finish, or that the server answers with a 5xx
// or a 429 is retried after each of delays, DefaultDelays when nil, as
// get does. Any other answer but 200 fails at once, naming it.
func hashURL(ctx context.Context, client *http.Client, u string, delays []time.Duration) (string, error) {
	if delays == nil {
		delays = DefaultDelays
	}
	for try := 0; ; try++ {
		sum, retry, err := hashOnce(ctx, client, u)
		if err == nil || !retry || try == len(delays) {
			return sum, err
		}
		select {
		case <-ctx.Done():
			return "", errors.Join(err, ctx.Err())
		case <-time.After(delays[try]):
		}
	}
}

func hashOnce(ctx context.Context, client *http.Client, u string) (sum string, retry bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return "", false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", true, fmt.Errorf("fetch %s: %w", u, err)
	}
	defer func() { _ = resp.Body.Close() }() // read-only, so nothing to lose
	if resp.StatusCode != http.StatusOK {
		retry = resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
		return "", retry, fmt.Errorf("fetch %s: %s", u, resp.Status)
	}
	h := sha256.New()
	if _, err = io.Copy(h, resp.Body); err != nil {
		return "", true, fmt.Errorf("fetch %s: %w", u, err)
	}
	return hex.EncodeToString(h.Sum(nil)), false, nil
}

// imagePair is the tag that names both apps' versions: the Calibre
// version, "-cw.", and the first seven characters of Calibre-Web's commit.
func imagePair(cf []byte) (string, error) {
	version, err := arg(cf, "CALIBRE_VERSION")
	if err != nil {
		return "", err
	}
	commit, err := arg(cf, "CALIBRE_WEB_COMMIT")
	if err != nil {
		return "", err
	}
	switch {
	case version == "" || strings.ContainsAny(version, " \t/:"):
		return "", fmt.Errorf("the Containerfile's CALIBRE_VERSION is %q, which is not a version", version)
	case !pinnedCommit.MatchString(commit):
		return "", fmt.Errorf("the Containerfile's CALIBRE_WEB_COMMIT is %q, which is not a full commit", commit)
	}
	return version + "-cw." + commit[:7], nil
}

// pinnedCommit is a full commit SHA.
var pinnedCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)
