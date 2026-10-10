package permissions

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Layering (Phase 2, T2.1): user < project < enterprise.
//
//   - User: ~/.hakase/permissions.json (HAKASE_HOME honored).
//   - Project: <root>/.hakase/permissions.json, content-hash trust-gated:
//     the file's fingerprint must be trusted or the whole layer is dropped.
//   - Enterprise: /etc/hakase/enterprise.json plus an optional URL poll
//     (explicit PolicyURL > HAKASE_ENTERPRISE_POLICY_URL > the file's
//     policy_url). Enterprise deny/ask always win under deny > ask > allow
//     evaluation; allowManagedOnly additionally drops allow rules from
//     lower layers.
//
// All file reads are mtime-cached (project cache keyed per root); loader
// state is mutex-guarded. Missing files are empty layers, never errors.
// A present-but-corrupt user/enterprise file IS an error (surfaces at
// startup wiring, T2.2); a corrupt project file simply fails its trust
// check and is dropped.

const (
	// PermissionsFile is the policy filename in the user home and per root.
	PermissionsFile = "permissions.json"
	// DefaultEnterprisePath is the host-admin enterprise policy path.
	DefaultEnterprisePath = "/etc/hakase/enterprise.json"
	// EnterpriseURLVar overrides the enterprise policy URL.
	EnterpriseURLVar = "HAKASE_ENTERPRISE_POLICY_URL"
	// defaultPollMinutes is the enterprise URL poll interval default.
	defaultPollMinutes = 60
	// maxEnterpriseBytes caps a fetched enterprise policy (1 MB).
	maxEnterpriseBytes = 1 << 20
	// enterpriseFetchTimeout caps one enterprise poll.
	enterpriseFetchTimeout = 15 * time.Second
)

// TrustChecker answers whether a content fingerprint is trusted. It is
// satisfied structurally by hooks.TrustStore; a nil checker trusts
// nothing (project layer always dropped).
type TrustChecker interface {
	Trusted(fingerprint string) bool
}

// FingerprintPolicy hashes raw policy file bytes into the trust identity
// ("perm:<sha256hex>"). The fingerprint covers the exact bytes, never the
// path, so a rewrite lapses trust until re-approved.
func FingerprintPolicy(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "perm:" + hex.EncodeToString(sum[:])
}

// hakaseHome mirrors the hooks home resolution (HAKASE_HOME honored)
// locally to avoid coupling this package to hooks.
func hakaseHome() string {
	if h := os.Getenv("HAKASE_HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".hakase")
	}
	return ""
}

// UserPolicyPath returns the user-layer path or "" when homeless.
func UserPolicyPath() string {
	if home := hakaseHome(); home != "" {
		return filepath.Join(home, PermissionsFile)
	}
	return ""
}

// ProjectPolicyPath returns the project-layer path for a root.
func ProjectPolicyPath(root string) string {
	return filepath.Join(root, ".hakase", PermissionsFile)
}

// LoadPolicyFile reads and strict-decodes one policy file: unknown keys
// and unsupported versions are errors.
func LoadPolicyFile(path string) (Policy, []byte, error) {
	var p Policy
	raw, err := os.ReadFile(path)
	if err != nil {
		return p, nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Policy{}, raw, fmt.Errorf("permissions: %s: %w", path, err)
	}
	if p.Version != 0 && p.Version != 1 {
		return Policy{}, raw, fmt.Errorf("permissions: %s: unsupported version %d (want 1)", path, p.Version)
	}
	return p, raw, nil
}

// LayeredPolicy is the merged, compiled result of Load.
type LayeredPolicy struct {
	// Policy merges enterprise + user + trusted-project rules.
	Policy *CompiledPolicy
	// Enterprise carries the merged enterprise flags.
	Enterprise EnterprisePolicy
	// ProjectPath is the project file considered ("" when none).
	ProjectPath string
	// ProjectTrusted reports whether the project layer applied.
	ProjectTrusted bool
	// Warnings holds operator-actionable notes (H2: a rule-less policy
	// with a non-ask default enforces nothing - the additive design acts
	// on matched rules only, so closed world needs an explicit catch-all
	// rule, not just "default":"deny").
	Warnings []string
}

// fileEntry is one mtime-cached policy file.
type fileEntry struct {
	path   string
	mtime  time.Time
	size   int64
	fp     string // content fingerprint (project entries only)
	policy Policy
	ok     bool
}

// Loader resolves and merges the three policy layers. Zero value is
// usable except where noted; override paths/fetchers for tests.
type Loader struct {
	// EnterprisePath overrides DefaultEnterprisePath ("" = default).
	EnterprisePath string
	// UserPath overrides UserPolicyPath ("" = default; "!" = skip layer).
	UserPath string
	// PolicyURL overrides every URL source ("" = env > file).
	PolicyURL string
	// PollMinutes overrides the poll interval (<=0 = file > default 60).
	PollMinutes int
	// Trust gates the project layer; nil trusts nothing.
	Trust TrustChecker
	// Fetch overrides the enterprise URL fetch (tests); nil = net/http.
	Fetch func(url string) ([]byte, error)
	// CachePath overrides the URL disk-cache path ("" = default home
	// path; "!" = disable disk cache).
	CachePath string

	mu             sync.Mutex
	user           fileEntry
	projects       map[string]fileEntry
	enterpriseFile fileEntry
	urlPolicy      Policy
	urlFetchedAt   time.Time
	urlHave        bool
}

// Load resolves, merges, and compiles every layer for root.
func (l *Loader) Load(root string) (*LayeredPolicy, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var (
		merged       []Rule
		agentRules   = map[string][]Rule{}
		def          = EffectAsk
		haveDef      bool
		entFlags     EnterprisePolicy
		entLen       int
		entAgentLens = map[string]int{}
	)

	strictest := func(d Effect) {
		if !haveDef || precedence(d) > precedence(def) {
			def, haveDef = d, true
		}
	}
	// absorb concatenates one layer; enterprise layers are tracked so
	// allowManagedOnly can strip allows from lower layers only. Source
	// tags each rule with its layer for audit citations.
	absorb := func(p Policy, enterprise bool, source string) {
		for i := range p.Rules {
			p.Rules[i].Source = source
		}
		merged = append(merged, p.Rules...)
		for name, ap := range p.Agents {
			key := strings.ToLower(name)
			for i := range ap.Rules {
				ap.Rules[i].Source = source
			}
			agentRules[key] = append(agentRules[key], ap.Rules...)
		}
		if enterprise {
			entLen = len(merged)
			for name := range p.Agents {
				entAgentLens[strings.ToLower(name)] = len(agentRules[strings.ToLower(name)])
			}
		}
		d := p.Default
		if d == "" {
			d = EffectAsk
		}
		strictest(d)
	}

	// Enterprise file first (lowest merge position, highest authority
	// under deny > ask > allow).
	entPath := l.EnterprisePath
	if entPath == "" {
		entPath = DefaultEnterprisePath
	}
	if p, ok, err := l.loadCachedFile(&l.enterpriseFile, entPath); err != nil {
		return nil, err
	} else if ok {
		absorb(p, true, "enterprise")
		orEnterprise(&entFlags, p.Enterprise)
	}

	// Enterprise URL poll (same authority as the file).
	if up, changed, err := l.pollEnterprise(entFlags); err != nil {
		return nil, err
	} else if changed {
		absorb(*up, true, "enterprise")
		orEnterprise(&entFlags, up.Enterprise)
	} else if l.urlHave {
		absorb(l.urlPolicy, true, "enterprise")
		orEnterprise(&entFlags, l.urlPolicy.Enterprise)
	}

	// User layer.
	userPath := l.UserPath
	if userPath == "" {
		userPath = UserPolicyPath()
	}
	if userPath != "" && userPath != "!" {
		if p, ok, err := l.loadCachedFile(&l.user, userPath); err != nil {
			return nil, err
		} else if ok {
			absorb(p, false, "user")
		}
	}

	// Project layer: trust-gated on the exact file bytes.
	out := &LayeredPolicy{Enterprise: entFlags}
	if root != "" {
		pp := ProjectPolicyPath(root)
		out.ProjectPath = pp
		raw, err := os.ReadFile(pp)
		switch {
		case os.IsNotExist(err):
			out.ProjectPath = ""
		case err != nil:
			return nil, fmt.Errorf("permissions: %s: %w", pp, err)
		default:
			fp := FingerprintPolicy(raw)
			if l.Trust != nil && l.Trust.Trusted(fp) {
				p, err := l.cachedProject(root, pp, raw)
				if err != nil {
					return nil, err
				}
				absorb(p, false, "project")
				out.ProjectTrusted = true
			}
		}
	}

	// allowManagedOnly strips allow rules from non-enterprise layers:
	// only the enterprise prefix (and enterprise agent prefixes) keep
	// their allows.
	if entFlags.AllowManagedOnly {
		merged = append(append([]Rule{}, merged[:entLen]...), dropAllows(merged[entLen:])...)
		for name, rules := range agentRules {
			if el, ok := entAgentLens[name]; ok && el < len(rules) {
				agentRules[name] = append(append([]Rule{}, rules[:el]...), dropAllows(rules[el:])...)
			} else if !ok {
				agentRules[name] = dropAllows(rules)
			}
		}
	}

	base := Policy{Version: 1, Default: def, Rules: merged}
	for name, rules := range agentRules {
		if base.Agents == nil {
			base.Agents = map[string]AgentPolicy{}
		}
		base.Agents[name] = AgentPolicy{Rules: rules}
	}
	base.Enterprise = entFlags
	cp, err := Compile(base)
	if err != nil {
		return nil, err
	}
	out.Policy = cp
	if len(merged) == 0 && len(agentRules) == 0 && def != EffectAsk {
		out.Warnings = append(out.Warnings,
			"permissions: merged policy has no rules with default "+string(def)+
				" - the default alone enforces nothing (additive design acts on matched rules only);"+
				" for closed world add an explicit {\"action\":\"*\",\"resource\":\"*\",\"effect\":\"deny\"} rule")
	}
	return out, nil
}

// dropAllows returns rules without EffectAllow entries.
func dropAllows(rules []Rule) []Rule {
	var out []Rule
	for _, r := range rules {
		if strings.ToLower(strings.TrimSpace(string(r.Effect))) == string(EffectAllow) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// orEnterprise merges enterprise flags (booleans OR; URL/minutes first set wins).
func orEnterprise(dst *EnterprisePolicy, src EnterprisePolicy) {
	dst.AllowManagedOnly = dst.AllowManagedOnly || src.AllowManagedOnly
	dst.DisableBypass = dst.DisableBypass || src.DisableBypass
	if dst.PolicyURL == "" {
		dst.PolicyURL = src.PolicyURL
	}
	if dst.PollMinutes == 0 {
		dst.PollMinutes = src.PollMinutes
	}
}

// loadCachedFile parses path unless the entry is fresh (mtime + size).
// ok=false means missing (empty layer, not an error); a present-but-
// corrupt file is an error.
func (l *Loader) loadCachedFile(entry *fileEntry, path string) (Policy, bool, error) {
	if path == "" {
		return Policy{}, false, nil
	}
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		*entry = fileEntry{}
		return Policy{}, false, nil
	}
	if err != nil {
		return Policy{}, false, fmt.Errorf("permissions: %s: %w", path, err)
	}
	if entry.ok && entry.path == path && entry.mtime.Equal(fi.ModTime()) && entry.size == fi.Size() {
		return entry.policy, true, nil
	}
	p, _, err := LoadPolicyFile(path)
	if err != nil {
		return Policy{}, false, err
	}
	*entry = fileEntry{path: path, mtime: fi.ModTime(), size: fi.Size(), policy: p, ok: true}
	return p, true, nil
}

// cachedProject parses already-read project bytes unless the per-root
// entry is fresh for the same bytes (mtime + size + content hash: a
// rewrite inside one mtime tick still lapses the cache).
func (l *Loader) cachedProject(root, path string, raw []byte) (Policy, error) {
	if l.projects == nil {
		l.projects = map[string]fileEntry{}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return Policy{}, fmt.Errorf("permissions: %s: %w", path, err)
	}
	want := FingerprintPolicy(raw)
	if e, ok := l.projects[root]; ok && e.ok && e.mtime.Equal(fi.ModTime()) && e.size == int64(len(raw)) && e.fp == want {
		return e.policy, nil
	}
	dec := jsonDecoder(raw)
	var p Policy
	if err := dec.Decode(&p); err != nil {
		return Policy{}, fmt.Errorf("permissions: %s: %w", path, err)
	}
	if p.Version != 0 && p.Version != 1 {
		return Policy{}, fmt.Errorf("permissions: %s: unsupported version %d (want 1)", path, p.Version)
	}
	l.projects[root] = fileEntry{path: path, mtime: fi.ModTime(), size: int64(len(raw)), fp: want, policy: p, ok: true}
	return p, nil
}

// jsonDecoder is a strict policy decoder over raw bytes.
func jsonDecoder(raw []byte) *json.Decoder {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec
}

// enterpriseSource resolves the poll URL (explicit > env > file) and interval.
func (l *Loader) enterpriseSource(fileFlags EnterprisePolicy) (url string, minutes int) {
	url = l.PolicyURL
	if url == "" {
		url = os.Getenv(EnterpriseURLVar)
	}
	if url == "" {
		url = fileFlags.PolicyURL
	}
	minutes = l.PollMinutes
	if minutes <= 0 {
		minutes = fileFlags.PollMinutes
	}
	if minutes <= 0 {
		minutes = defaultPollMinutes
	}
	return url, minutes
}

// pollEnterprise fetches the enterprise URL policy when due. It returns
// the fresh policy with changed=true, or nil/false to keep using the
// cached one. Fetch/parse failures keep the last good policy (memory,
// else disk cache) and never error: a poll outage must not change
// verdicts mid-session.
func (l *Loader) pollEnterprise(fileFlags EnterprisePolicy) (*Policy, bool, error) {
	url, minutes := l.enterpriseSource(fileFlags)
	if url == "" {
		return nil, false, nil
	}
	if l.urlHave && time.Since(l.urlFetchedAt) < time.Duration(minutes)*time.Minute {
		return nil, false, nil
	}
	fetch := l.Fetch
	if fetch == nil {
		fetch = defaultFetch
	}
	raw, err := fetch(url)
	if err != nil {
		return nil, false, nil
	}
	var p Policy
	if err := jsonDecoder(raw).Decode(&p); err != nil {
		return nil, false, nil
	}
	l.urlPolicy, l.urlFetchedAt, l.urlHave = p, time.Now(), true
	l.writeURLCache(raw)
	return &p, true, nil
}

// defaultFetch GETs a policy URL with a timeout and size cap.
func defaultFetch(url string) ([]byte, error) {
	client := &http.Client{Timeout: enterpriseFetchTimeout}
	resp, err := client.Get(url) //nolint:gosec // admin-configured policy URL
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("permissions: GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxEnterpriseBytes+1))
}

// urlCachePath resolves the URL disk-cache path ("!" disables).
func (l *Loader) urlCachePath() string {
	if l.CachePath == "!" {
		return ""
	}
	if l.CachePath != "" {
		return l.CachePath
	}
	if home := hakaseHome(); home != "" {
		return filepath.Join(home, "enterprise-policy-cache.json")
	}
	return ""
}

// writeURLCache persists fetched bytes atomically (temp + rename);
// failures are best-effort and never propagate.
func (l *Loader) writeURLCache(raw []byte) {
	path := l.urlCachePath()
	if path == "" || len(raw) > maxEnterpriseBytes {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".enterprise-cache-*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
	}
}
