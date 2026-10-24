// flowhydrate.go - shared cold-registry hydration for channel push gates.
//
// After a process restart the in-memory flow registry is cold while the
// persisted narrowed labels are on disk. Hydration reloads the persisted
// label verbatim / Meet-merges with a live entry (never Fold, never
// wider) before the egress gate evaluates, so a non-narrowed session
// delivers instead of falsely withholding. A session missing from disk or
// unloadable stays fail-closed via CheckChannelEgress's unregistered-session
// path (narrowest). Read-only: no fold, never sticky.
package channel

import (
	hakasesession "amurru/hakase/internal/session"
)

// HydrateFlowLabel loads sessionID from the session store into the flow
// registry when the registry holds no live entry (cold restart). It mirrors
// the resume path (RegisterLoaded: verbatim import or Meet-merge, never
// wider). It returns nil on success or when there is nothing to do (empty
// id, nil service/store, already initialized); otherwise the load error so
// the caller can log the skip distinctly from withhold lines. Callers bound
// the load to one attempt per session per process.
func HydrateFlowLabel(sessions *hakasesession.SessionService, sessionID string) error {
	if sessions == nil || sessionID == "" {
		return nil
	}
	if hakasesession.DefaultRegistry.IsInitialized(sessionID) {
		return nil
	}
	store := sessions.Store()
	if store == nil {
		return nil
	}
	sess, err := store.Load(sessionID)
	if err != nil {
		return err
	}
	hakasesession.RegisterLoaded(sess)
	return nil
}
