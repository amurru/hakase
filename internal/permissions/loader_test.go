package permissions

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// trustMap is a TrustChecker over explicit fingerprints.
type trustMap map[string]bool

func (m trustMap) Trusted(fp string) bool { return m[fp] }

func writePolicy(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testLoader() *Loader {
	return &Loader{EnterprisePath: filepath.Join("nonexistent"), UserPath: "!"}
}

// TestLayeringOrder pins user < project merge with enterprise deny
// winning over lower allows (non-widening pin).
func TestLayeringOrder(t *testing.T) {
	root := t.TempDir()
	proj := ProjectPolicyPath(root)
	writePolicy(t, proj, `{"version":1,"rules":[
		{"action":"shell","resource":"git push *","effect":"allow"}]}`)
	raw, _ := os.ReadFile(proj)
	trust := trustMap{FingerprintPolicy(raw): true}

	ent := filepath.Join(t.TempDir(), "enterprise.json")
	writePolicy(t, ent, `{"version":1,"rules":[
		{"action":"shell","resource":"git push *","effect":"deny"}]}`)

	l := testLoader()
	l.EnterprisePath = ent
	l.Trust = trust
	lp, err := l.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !lp.ProjectTrusted {
		t.Error("ProjectTrusted = false, want true")
	}
	if got, _ := lp.Policy.Evaluate("shell", "git push origin"); got != EffectDeny {
		t.Errorf("merged Evaluate = %q, want deny (enterprise wins)", got)
	}
}

// TestProjectTrustGate pins an untrusted project file dropped entirely.
func TestProjectTrustGate(t *testing.T) {
	root := t.TempDir()
	writePolicy(t, ProjectPolicyPath(root), `{"version":1,"rules":[
		{"action":"shell","resource":"git push *","effect":"deny"}]}`)

	l := testLoader() // nil trust trusts nothing
	lp, err := l.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if lp.ProjectTrusted {
		t.Error("ProjectTrusted = true for untrusted file, want false")
	}
	if got, _ := lp.Policy.Evaluate("shell", "git push origin"); got != EffectAsk {
		t.Errorf("Evaluate = %q, want default ask (layer dropped)", got)
	}
}

// TestTrustLapse pins a project rewrite lapsing trust until re-approved.
func TestTrustLapse(t *testing.T) {
	root := t.TempDir()
	pp := ProjectPolicyPath(root)
	writePolicy(t, pp, `{"version":1,"rules":[
		{"action":"shell","resource":"git *","effect":"allow"}]}`)
	raw, _ := os.ReadFile(pp)
	trust := trustMap{FingerprintPolicy(raw): true}

	l := testLoader()
	l.Trust = trust
	lp, err := l.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, _ := lp.Policy.Evaluate("shell", "git status"); got != EffectAllow {
		t.Fatalf("trusted Evaluate = %q, want allow", got)
	}

	// Rewrite with widened rules under the same path.
	writePolicy(t, pp, `{"version":1,"rules":[
		{"action":"shell","resource":"*","effect":"allow"}]}`)
	lp, err = l.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if lp.ProjectTrusted {
		t.Error("ProjectTrusted = true after rewrite, want false (trust lapsed)")
	}
	if got, _ := lp.Policy.Evaluate("shell", "rm -rf /tmp/x"); got != EffectAsk {
		t.Errorf("rewritten Evaluate = %q, want default ask", got)
	}
}

// TestAllowManagedOnly pins enterprise allowManagedOnly stripping lower
// allows while keeping enterprise allows and lower denies.
func TestAllowManagedOnly(t *testing.T) {
	root := t.TempDir()
	proj := ProjectPolicyPath(root)
	writePolicy(t, proj, `{"version":1,"rules":[
		{"action":"shell","resource":"git *","effect":"allow"},
		{"action":"shell","resource":"rm *","effect":"deny"}]}`)
	raw, _ := os.ReadFile(proj)
	trust := trustMap{FingerprintPolicy(raw): true}

	ent := filepath.Join(t.TempDir(), "enterprise.json")
	writePolicy(t, ent, `{"version":1,
		"rules":[{"action":"shell","resource":"deploy *","effect":"allow"}],
		"enterprise":{"allow_managed_only":true}}`)

	l := testLoader()
	l.EnterprisePath = ent
	l.Trust = trust
	lp, err := l.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, _ := lp.Policy.Evaluate("shell", "deploy prod"); got != EffectAllow {
		t.Errorf("enterprise allow = %q, want allow", got)
	}
	if got, _ := lp.Policy.Evaluate("shell", "git status"); got != EffectAsk {
		t.Errorf("lower allow = %q, want ask (stripped)", got)
	}
	if got, _ := lp.Policy.Evaluate("shell", "rm file"); got != EffectDeny {
		t.Errorf("lower deny = %q, want deny (kept)", got)
	}
}

// TestStrictKeys pins unknown JSON keys rejected at load.
func TestStrictKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "permissions.json")
	writePolicy(t, path, `{"version":1,"rulez":[]}`)
	if _, _, err := LoadPolicyFile(path); err == nil {
		t.Error("LoadPolicyFile accepted unknown key, want error")
	}
	writePolicy(t, path, `{"version":99,"rules":[]}`)
	if _, _, err := LoadPolicyFile(path); err == nil {
		t.Error("LoadPolicyFile accepted version 99, want error")
	}
}

// TestEnterpriseURLPoll pins fetch-then-cache: a poll outage keeps the
// last good policy instead of erroring or opening up.
func TestEnterpriseURLPoll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"version":1,"rules":[
			{"action":"shell","resource":"deploy *","effect":"deny"}]}`)
	}))
	defer srv.Close()

	l := testLoader()
	l.PolicyURL = srv.URL
	l.CachePath = "!"
	lp, err := l.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, _ := lp.Policy.Evaluate("shell", "deploy prod"); got != EffectDeny {
		t.Fatalf("URL policy Evaluate = %q, want deny", got)
	}

	// Outage: point at a dead server with interval forcing re-poll.
	srv.Close()
	l.PolicyURL = "http://127.0.0.1:1"
	l.PollMinutes = -1 // every Load re-polls
	lp, err = l.Load("")
	if err != nil {
		t.Fatalf("Load during outage: %v (want nil, keep last)", err)
	}
	if got, _ := lp.Policy.Evaluate("shell", "deploy prod"); got != EffectDeny {
		t.Errorf("post-outage Evaluate = %q, want deny (last good kept)", got)
	}
}

// TestLayeringMatrix pins the full enterprise/user/project x
// allow/ask/deny merge matrix for one resource.
func TestLayeringMatrix(t *testing.T) {
	cases := []struct {
		name       string
		enterprise Effect
		user       Effect
		project    Effect
		want       Effect
	}{
		{"all deny", EffectDeny, EffectDeny, EffectDeny, EffectDeny},
		{"enterprise deny wins over allows", EffectDeny, EffectAllow, EffectAllow, EffectDeny},
		{"user deny beats project allow", EffectAsk, EffectDeny, EffectAllow, EffectDeny},
		{"project deny beats allows above", EffectAllow, EffectAllow, EffectDeny, EffectDeny},
		{"ask beats allow", EffectAllow, EffectAsk, EffectAllow, EffectAsk},
		{"project ask escalates", EffectAllow, EffectAllow, EffectAsk, EffectAsk},
		{"all allow", EffectAllow, EffectAllow, EffectAllow, EffectAllow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			proj := ProjectPolicyPath(root)
			writePolicy(t, proj, fmt.Sprintf(`{"version":1,"rules":[
				{"action":"shell","resource":"run *","effect":%q}]}`, tc.project))
			raw, _ := os.ReadFile(proj)

			home := t.TempDir()
			t.Setenv("HAKASE_HOME", home)
			writePolicy(t, filepath.Join(home, "permissions.json"), fmt.Sprintf(`{"version":1,"rules":[
				{"action":"shell","resource":"run *","effect":%q}]}`, tc.user))

			ent := filepath.Join(t.TempDir(), "enterprise.json")
			writePolicy(t, ent, fmt.Sprintf(`{"version":1,"rules":[
				{"action":"shell","resource":"run *","effect":%q}]}`, tc.enterprise))

			l := &Loader{EnterprisePath: ent, Trust: trustMap{FingerprintPolicy(raw): true}}
			lp, err := l.Load(root)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got, _ := lp.Policy.Evaluate("shell", "run job"); got != tc.want {
				t.Errorf("Evaluate = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestProjectRootIsolation pins per-root cache separation: trusting one
// root's project file (the PinnedTo non-widening case) never leaks its
// layer into another root's load.
func TestProjectRootIsolation(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	writePolicy(t, ProjectPolicyPath(rootA), `{"version":1,"rules":[
		{"action":"shell","resource":"deploy *","effect":"allow"}]}`)
	writePolicy(t, ProjectPolicyPath(rootB), `{"version":1,"rules":[
		{"action":"shell","resource":"deploy *","effect":"deny"}]}`)
	rawA, _ := os.ReadFile(ProjectPolicyPath(rootA))

	l := testLoader()
	l.Trust = trustMap{FingerprintPolicy(rawA): true}
	lpA, err := l.Load(rootA)
	if err != nil {
		t.Fatalf("Load A: %v", err)
	}
	lpB, err := l.Load(rootB)
	if err != nil {
		t.Fatalf("Load B: %v", err)
	}
	if got, _ := lpA.Policy.Evaluate("shell", "deploy prod"); got != EffectAllow {
		t.Errorf("root A = %q, want allow (trusted)", got)
	}
	if got, _ := lpB.Policy.Evaluate("shell", "deploy prod"); got != EffectAsk {
		t.Errorf("root B = %q, want ask (untrusted file dropped)", got)
	}
}

// TestLoaderConcurrent pins lock safety: parallel Load + Lookup under
// -race must stay clean and consistent.
func TestLoaderConcurrent(t *testing.T) {
	root := t.TempDir()
	proj := ProjectPolicyPath(root)
	writePolicy(t, proj, `{"version":1,"rules":[
		{"action":"shell","resource":"run *","effect":"deny"}]}`)
	raw, _ := os.ReadFile(proj)

	l := testLoader()
	l.Trust = trustMap{FingerprintPolicy(raw): true}
	done := make(chan error, 16)
	for i := 0; i < 8; i++ {
		go func() {
			lp, err := l.Load(root)
			if err != nil {
				done <- err
				return
			}
			if got, _ := lp.Policy.Evaluate("shell", "run job"); got != EffectDeny {
				done <- fmt.Errorf("Evaluate = %q, want deny", got)
				return
			}
			done <- nil
		}()
		go func() {
			_, _, _ = Lookup("shell", "run job")
			done <- nil
		}()
	}
	for i := 0; i < 16; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

// TestAgentOverlay pins per-agent rule merging under deny > ask > allow.
func TestAgentOverlay(t *testing.T) {
	cp, err := Compile(Policy{Rules: []Rule{
		{Action: "shell", Resource: "git *", Effect: EffectAllow},
	}, Agents: map[string]AgentPolicy{
		"reviewer": {Rules: []Rule{
			{Action: "shell", Resource: "git push *", Effect: EffectDeny},
		}},
	}})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got, _ := cp.EvaluateForAgent("reviewer", "shell", "git push origin"); got != EffectDeny {
		t.Errorf("reviewer push Evaluate = %q, want deny (overlay)", got)
	}
	if got, _ := cp.EvaluateForAgent("reviewer", "shell", "git status"); got != EffectAllow {
		t.Errorf("reviewer status Evaluate = %q, want allow (base)", got)
	}
	if got, _ := cp.EvaluateForAgent("coder", "shell", "git push origin"); got != EffectAllow {
		t.Errorf("other-agent push Evaluate = %q, want base allow", got)
	}
	if got, _ := cp.EvaluateForAgent("coder", "shell", "make test"); got != EffectAsk {
		t.Errorf("unknown-agent Evaluate = %q, want base ask", got)
	}
	if got, _ := cp.EvaluateForAgent("", "shell", "make test"); got != EffectAsk {
		t.Errorf("empty-agent Evaluate = %q, want base ask", got)
	}
}

// TestDisableBypass pins the enterprise flag reaching the installed
// snapshot for the approval gate to consult.
func TestDisableBypass(t *testing.T) {
	ent := filepath.Join(t.TempDir(), "enterprise.json")
	writePolicy(t, ent, `{"version":1,"enterprise":{"disable_bypass":true}}`)
	l := testLoader()
	l.EnterprisePath = ent
	lp, err := l.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	InstallLayered(lp)
	t.Cleanup(func() { InstallLayered(nil) })
	if !BypassDisabled() {
		t.Error("BypassDisabled = false, want true")
	}
	if _, _, ok := Lookup("shell", "anything"); !ok {
		t.Error("Lookup ok = false with layered policy, want true")
	}
}
