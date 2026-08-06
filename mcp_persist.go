package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// mcpRegistryMu guards ~/.hakase/mcp.json access.
var mcpRegistryMu sync.Mutex

// mcpRegistryFile is resolved once via resolveMCPFile().
var mcpRegistryFile string

// resolveMCPFile returns the path to the persisted MCP user registry,
// creating the parent directory if missing. Uses $HAKASE_HOME or ~/.hakase.
func resolveMCPFile() string {
	if mcpRegistryFile != "" {
		return mcpRegistryFile
	}
	home := hakaseHome()
	if home == "" {
		home = "."
	}
	_ = os.MkdirAll(home, 0755)
	mcpRegistryFile = filepath.Join(home, "mcp.json")
	return mcpRegistryFile
}

// loadMCPUserRegistryLocked reads the registry from disk under the mutex.
func loadMCPUserRegistryLocked() (MCPUserRegistry, error) {
	var reg MCPUserRegistry
	data, err := os.ReadFile(resolveMCPFile())
	if err != nil {
		if os.IsNotExist(err) {
			return MCPUserRegistry{}, nil
		}
		return MCPUserRegistry{}, err
	}
	if err := json.Unmarshal(data, &reg); err != nil {
		return MCPUserRegistry{}, err
	}
	return reg, nil
}

// loadMCPUserRegistry is the public locked loader.
func loadMCPUserRegistry() (MCPUserRegistry, error) {
	mcpRegistryMu.Lock()
	defer mcpRegistryMu.Unlock()
	return loadMCPUserRegistryLocked()
}

// saveMCPUserRegistryLocked writes the registry to disk atomically with a
// tmp-file + rename, protected by an exclusive flock for cross-process safety.
func saveMCPUserRegistryLocked(reg MCPUserRegistry) error {
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	file := resolveMCPFile()
	tmp := file + ".tmp"
	lockFile := file + ".lock"

	lf, err := os.OpenFile(lockFile, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)

	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// saveMCPUserRegistry is the public locked saver.
func saveMCPUserRegistry(reg MCPUserRegistry) error {
	mcpRegistryMu.Lock()
	defer mcpRegistryMu.Unlock()
	return saveMCPUserRegistryLocked(reg)
}

// updateMCPUserRegistry loads, mutates, and saves the registry under one lock
// hold. Used by TUI toggles.
func updateMCPUserRegistry(mutate func(*MCPUserRegistry) error) error {
	mcpRegistryMu.Lock()
	defer mcpRegistryMu.Unlock()

	reg, err := loadMCPUserRegistryLocked()
	if err != nil {
		return err
	}

	if err := mutate(&reg); err != nil {
		return err
	}

	return saveMCPUserRegistryLocked(reg)
}
