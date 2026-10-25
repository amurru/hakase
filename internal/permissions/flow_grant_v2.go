// flow_grant_v2.go - joint-hash one-shot flow grants (S25).
//
// The v1 grant key is (session, tool, argsHash, labelVersion) assembled
// with NUL separators. S25 binds the same four legs into a single joint
// hash (JointGrantHash) minted as a v2 record alongside the v1 record on
// every fresh approval (dual records, one human decision), and consumed
// with dual-accept semantics:
//
//   - StartLabel (labelVersion 0, session-less v1-parity callers): a v1
//     OR a v2 record satisfies the check (dual-accept, no behavior
//     break for pre-upgrade grants).
//   - Narrowed labels (labelVersion != 0): only the v2 joint record
//     satisfies (fresh approval: a v1 record minted before the upgrade,
//     or for a different label version, never authorizes a narrowed
//     call).
//
// The joint hash carries every leg, so no leg can be swapped: a grant
// for (session A, tool T, hash H, version 3) never satisfies (session B,
// T, H, 3) or (A, T, H, 4). Stores are hash-only (never raw args),
// bounded (1024, evict-oldest fails closed to re-ask), TTL 15m lazy
// expiry, no goroutine - the same discipline as the v1 stores.
package permissions

import (
	"strconv"
	"sync"
	"time"
)

// flowGrantV2TTL bounds one approved flow ask (same 15m budget as v1).
const flowGrantV2TTL = 15 * time.Minute

// maxFlowGrantV2 bounds the v2 joint-grant store (same 1024 cap as v1).
const maxFlowGrantV2 = 1024

// flowGrantV2Now is the grant clock; a var so tests fake expiry without
// waiting. Production always uses time.Now.
var flowGrantV2Now = time.Now

// JointGrantHash binds (session, tool, argsHash, labelVersion) into one
// hash. ArgsHash NUL-separates legs so no value forges another leg;
// the tool is canonicalized first (system_exec_start/grep spellings bind
// one key, same as the v1 key).
func JointGrantHash(sessionID, tool, argsHash string, labelVersion uint64) string {
	return ArgsHash(sessionID, canonicalFlowGrantTool(tool), argsHash, strconv.FormatUint(labelVersion, 10))
}

// flowGrantV2Entry is one live joint-hash grant with its mint deadline.
type flowGrantV2Entry struct {
	expires time.Time
}

var flowGrantV2Store = struct {
	sync.Mutex
	m     map[string]flowGrantV2Entry
	order []string
}{m: make(map[string]flowGrantV2Entry)}

// sweepFlowGrantV2Locked drops expired entries. Callers hold the store.
func sweepFlowGrantV2Locked(now time.Time) {
	for k, e := range flowGrantV2Store.m {
		if !now.Before(e.expires) {
			delete(flowGrantV2Store.m, k)
		}
	}
	if len(flowGrantV2Store.order) > len(flowGrantV2Store.m)*2 {
		kept := flowGrantV2Store.order[:0]
		for _, k := range flowGrantV2Store.order {
			if _, ok := flowGrantV2Store.m[k]; ok {
				kept = append(kept, k)
			}
		}
		flowGrantV2Store.order = kept
	}
}

// GrantFlowBindingV2 installs the v2 joint-hash record for an approved
// flow ask. Empty-session versioned bindings never grant (same carve-out
// as v1: only the StartLabel labelVersion 0 may bind session-less).
func GrantFlowBindingV2(b FlowGrantBinding) {
	if emptyVersionedGrant(b) {
		return
	}
	flowGrantV2Store.Lock()
	defer flowGrantV2Store.Unlock()
	now := flowGrantV2Now()
	sweepFlowGrantV2Locked(now)
	k := JointGrantHash(b.SessionID, b.Tool, b.ArgsHash, b.LabelVersion)
	if _, ok := flowGrantV2Store.m[k]; !ok {
		flowGrantV2Store.order = append(flowGrantV2Store.order, k)
	}
	flowGrantV2Store.m[k] = flowGrantV2Entry{expires: now.Add(flowGrantV2TTL)}
	for len(flowGrantV2Store.order) > maxFlowGrantV2 {
		oldest := flowGrantV2Store.order[0]
		flowGrantV2Store.order = flowGrantV2Store.order[1:]
		delete(flowGrantV2Store.m, oldest)
	}
}

// hasFlowGrantV2Locked reports a live v2 record. Callers hold the store.
func hasFlowGrantV2Locked(k string, now time.Time) bool {
	e, ok := flowGrantV2Store.m[k]
	if !ok {
		return false
	}
	if !now.Before(e.expires) {
		delete(flowGrantV2Store.m, k)
		return false
	}
	return true
}

// HasFlowGrantV2 reports whether a v2 joint record for this exact call is
// pending. Empty-session versioned bindings never match (mirror of the
// mint guard).
func HasFlowGrantV2(b FlowGrantBinding) bool {
	if emptyVersionedGrant(b) {
		return false
	}
	flowGrantV2Store.Lock()
	defer flowGrantV2Store.Unlock()
	return hasFlowGrantV2Locked(JointGrantHash(b.SessionID, b.Tool, b.ArgsHash, b.LabelVersion), flowGrantV2Now())
}

// ConsumeFlowGrantV2 reports and consumes a matching v2 record
// (one-shot). Expired records report false.
func ConsumeFlowGrantV2(b FlowGrantBinding) bool {
	if emptyVersionedGrant(b) {
		return false
	}
	flowGrantV2Store.Lock()
	defer flowGrantV2Store.Unlock()
	k := JointGrantHash(b.SessionID, b.Tool, b.ArgsHash, b.LabelVersion)
	if !hasFlowGrantV2Locked(k, flowGrantV2Now()) {
		return false
	}
	delete(flowGrantV2Store.m, k)
	for i, q := range flowGrantV2Store.order {
		if q == k {
			flowGrantV2Store.order = append(flowGrantV2Store.order[:i], flowGrantV2Store.order[i+1:]...)
			break
		}
	}
	return true
}

// ConsumeFlowGrantDual is the S25 dual-accept consume (one-shot): on the
// StartLabel (labelVersion 0) a v1 record OR a v2 joint record satisfies
// the check (pre-upgrade grants keep working, v1 parity); on a narrowed
// label only the v2 joint record satisfies (fresh approval: a v1 record
// minted before the upgrade, or for any other version, never authorizes
// a narrowed call). One-shot across both stores: every twin record for
// the exact call is drained (a fresh approval mints both, one dispatch
// consumes both), so no grant leaks into a later turn.
func ConsumeFlowGrantDual(b FlowGrantBinding) bool {
	if emptyVersionedGrant(b) {
		return false
	}
	if b.LabelVersion == 0 {
		v2 := ConsumeFlowGrantV2(b)
		v1 := ConsumeFlowBinding(b)
		return v2 || v1
	}
	if !ConsumeFlowGrantV2(b) {
		return false
	}
	// Drain the same-approval v1 twin (minted alongside the v2 record):
	// the approval authorizes exactly one dispatch.
	ConsumeFlowBinding(b)
	return true
}

// HasFlowGrantDual is the dual-accept peek (same version rule, no
// consume): StartLabel accepts v1 or v2, narrowed requires v2.
func HasFlowGrantDual(b FlowGrantBinding) bool {
	if emptyVersionedGrant(b) {
		return false
	}
	if HasFlowGrantV2(b) {
		return true
	}
	if b.LabelVersion == 0 {
		return HasFlowBinding(b)
	}
	return false
}
