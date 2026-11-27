// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package probe

import (
	"crypto/md5" //nolint:gosec // Digest login (RFC 7616) is defined with MD5, and Calibre's server takes no other
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

// The Content Server's login is Digest, with MD5 and qop=auth, and takes
// no Basic login: it answers 400 "Unsupported authentication method".

// challenge is what a Digest login asks for, in WWW-Authenticate.
type challenge struct {
	realm, nonce, opaque string
}

// paramRE is one name=value of a header's list, the value quoted or not.
var paramRE = regexp.MustCompile(`(\w+)=(?:"([^"]*)"|([^,\s]*))`)

// parseParams reads a header's list of name=value pairs.
func parseParams(list string) map[string]string {
	params := make(map[string]string)
	for _, m := range paramRE.FindAllStringSubmatch(list, -1) {
		params[m[1]] = m[2] + m[3]
	}
	return params
}

// parseChallenge reads a WWW-Authenticate header. It accepts the
// algorithm MD5, which is the default, and the quality of protection
// "auth", and nothing else.
func parseChallenge(header string) (challenge, error) {
	list, ok := strings.CutPrefix(header, "Digest ")
	if !ok {
		return challenge{}, fmt.Errorf("asked for a login that is not Digest: %q", header)
	}
	p := parseParams(list)
	if alg := p["algorithm"]; alg != "" && !strings.EqualFold(alg, "MD5") {
		return challenge{}, fmt.Errorf("asked for the Digest algorithm %q, not MD5", alg)
	}
	if !slices.Contains(strings.FieldsFunc(p["qop"], func(r rune) bool { return r == ',' || r == ' ' }), "auth") {
		return challenge{}, fmt.Errorf("asked for the Digest quality of protection %q, not auth", p["qop"])
	}
	if p["nonce"] == "" {
		return challenge{}, errors.New("asked for a Digest login with no nonce")
	}
	return challenge{realm: p["realm"], nonce: p["nonce"], opaque: p["opaque"]}, nil
}

// response is the Digest response for one request, as RFC 2617 section 3.2.2
// writes it for qop=auth.
func (c challenge) response(user, pass, method, uri, nc, cnonce string) string {
	sum := func(s string) string {
		h := md5.Sum([]byte(s)) //nolint:gosec // as above
		return hex.EncodeToString(h[:])
	}
	ha1 := sum(user + ":" + c.realm + ":" + pass)
	ha2 := sum(method + ":" + uri)
	return sum(ha1 + ":" + c.nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)
}

// authorization is the Authorization header that answers c for the
// request req, which is made once with this login.
func (c challenge) authorization(user, pass string, req *http.Request) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	const nc = "00000001"
	cnonce := hex.EncodeToString(b)
	uri := req.URL.RequestURI()
	h := fmt.Sprintf(`Digest username=%q, realm=%q, nonce=%q, uri=%q, algorithm=MD5, response=%q, qop=auth, nc=%s, cnonce=%q`,
		user, c.realm, c.nonce, uri, c.response(user, pass, req.Method, uri, nc, cnonce), nc, cnonce)
	if c.opaque != "" {
		h += fmt.Sprintf(", opaque=%q", c.opaque)
	}
	return h, nil
}
