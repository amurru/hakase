package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/permissions"
)

// initPermissions loads and installs the layered permissions policy for
// root (user + trust-gated project + enterprise). Disabled config skips
// everything (nil installed policy). A corrupt user/enterprise file
// errors (startup fails loudly, landlock precedent); an untrusted
// project file is dropped and a URL outage keeps the last good policy.
func initPermissions(cfg *config.Config, root string, trust permissions.TrustChecker, log interfaces.LogFunc) error {
	if cfg == nil || !cfg.Permissions.LoadEnabled() {
		return nil
	}
	lp, err := newLoader(cfg, trust).Load(root)
	if err != nil {
		return err
	}
	installLayered(lp, log)
	return nil
}

// newLoader builds the layer loader from config.
func newLoader(cfg *config.Config, trust permissions.TrustChecker) *permissions.Loader {
	return &permissions.Loader{
		EnterprisePath: cfg.Permissions.EnterprisePath,
		PolicyURL:      cfg.Permissions.EnterpriseURL,
		PollMinutes:    cfg.Permissions.PollMinutes,
		Trust:          trust,
	}
}

// installLayered installs a snapshot and reports warnings/enterprise state.
func installLayered(lp *permissions.LayeredPolicy, log interfaces.LogFunc) {
	permissions.InstallLayered(lp)
	if log == nil {
		return
	}
	for _, w := range lp.Warnings {
		log(w)
	}
	if lp.ProjectTrusted {
		log(fmt.Sprintf("permissions: project layer trusted (%s)", lp.ProjectPath))
	}
	var restrictions []string
	if lp.Enterprise.DisableBypass {
		restrictions = append(restrictions, "disable_bypass")
	}
	if lp.Enterprise.AllowManagedOnly {
		restrictions = append(restrictions, "allow_managed_only")
	}
	if len(restrictions) > 0 {
		log(fmt.Sprintf("permissions: enterprise restrictions active (%s)", strings.Join(restrictions, ", ")))
	}
}

// permissionsRefreshMin clamps the enterprise refresh ticker (overridable in tests).
var permissionsRefreshMin = 5 * time.Minute

// permissionsRefreshForced overrides the interval entirely when positive
// (tests only; production always computes from config).
var permissionsRefreshForced time.Duration

// refreshInterval resolves the reload cadence: the installed enterprise
// poll minutes, else config, else the loader default; clamped to the min.
func refreshInterval(cfg *config.Config) time.Duration {
	if permissionsRefreshForced > 0 {
		return permissionsRefreshForced
	}
	minutes := 0
	if lp := permissions.Installed(); lp != nil {
		minutes = lp.Enterprise.PollMinutes
	}
	if minutes <= 0 && cfg != nil {
		minutes = cfg.Permissions.PollMinutes
	}
	if minutes <= 0 {
		minutes = 60
	}
	d := time.Duration(minutes) * time.Minute
	if d < permissionsRefreshMin {
		d = permissionsRefreshMin
	}
	return d
}

// StartPermissionsRefresh re-loads and re-installs the layered policy on
// the enterprise poll interval (M2: long-lived serve processes must pick
// up revocations and allowlist removals). Tick failures log loudly and
// keep the previous snapshot; the returned func stops the loop.
func StartPermissionsRefresh(ctx context.Context, cfg *config.Config, root string, trust permissions.TrustChecker, log interfaces.LogFunc) func() {
	stop := make(chan struct{})
	var once sync.Once
	var wg sync.WaitGroup
	halt := func() {
		once.Do(func() { close(stop) })
		// Wait for any in-flight tick: a tick past its select would
		// otherwise InstallLayered after the caller cleaned up (test
		// pollution, and a stale write on shutdown).
		wg.Wait()
	}
	if cfg == nil || !cfg.Permissions.LoadEnabled() {
		return halt
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(refreshInterval(cfg))
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-t.C:
				lp, err := newLoader(cfg, trust).Load(root)
				if err != nil {
					if log != nil {
						log(fmt.Sprintf("permissions: refresh failed (keeping previous policy): %v", err))
					}
					continue
				}
				installLayered(lp, log)
			}
		}
	}()
	return halt
}
