// resources.go - Flow Guard canonical resource helpers (FG-002/M4).
//
// Shell uses the raw command line, file actions use scoped absolute paths
// (canonicalized by the caller via sandbox ResolveScopedPath), URLs use
// normalized host (lowercase, IDNA, no fragment, query kept for decision).
package permissions

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"

	"golang.org/x/net/idna"
)

// ArgsHash hashes call arguments for audit metadata (never raw args in
// logs). Full sha256 hex; callers truncate for display.
func ArgsHash(args ...string) string {
	h := sha256.New()
	for i, a := range args {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(a))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ShortHash returns the first 12 hex chars of ArgsHash for audit lines
// (matches the python code_hash convention).
func ShortHash(args ...string) string {
	full := ArgsHash(args...)
	if len(full) > 12 {
		return full[:12]
	}
	return full
}

// NormalizeURL canonicalizes a URL for policy matching: lowercase scheme
// and host, IDNA punycode for homograph hosts, strip fragment, keep query
// (query participates in the decision). Best-effort: unparseable input
// returns the trimmed input unchanged rather than erroring the gate.
func NormalizeURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return s
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if ascii, err := idna.Lookup.ToASCII(host); err == nil && ascii != "" {
		host = ascii
	}
	if port := u.Port(); port != "" {
		u.Host = host + ":" + port
	} else {
		u.Host = host
	}
	u.Fragment = ""
	return u.String()
}
