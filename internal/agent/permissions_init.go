package agent

import (
	"fmt"
	"strings"

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
	l := &permissions.Loader{
		EnterprisePath: cfg.Permissions.EnterprisePath,
		PolicyURL:      cfg.Permissions.EnterpriseURL,
		PollMinutes:    cfg.Permissions.PollMinutes,
		Trust:          trust,
	}
	lp, err := l.Load(root)
	if err != nil {
		return err
	}
	permissions.InstallLayered(lp)
	if log != nil {
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
	return nil
}
