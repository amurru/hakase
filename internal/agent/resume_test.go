package agent

import (
	"amurru/hakase/internal/config"
	"testing"
	"time"
)

func TestResumeMaxAgeDefault(t *testing.T) {
	saved := deps
	deps = nil
	t.Cleanup(func() { deps = saved })
	if got := ResumeMaxAge(); got != 30*time.Minute {
		t.Errorf("ResumeMaxAge nil deps = %v, want 30m", got)
	}
}

func TestResumeMaxAgeConfigured(t *testing.T) {
	saved := deps
	deps = &Deps{Config: &config.Config{}}
	deps.Config.DurableResume.MaxResumeAgeMinutes = 10
	t.Cleanup(func() { deps = saved })
	if got := ResumeMaxAge(); got != 10*time.Minute {
		t.Errorf("ResumeMaxAge = %v, want 10m", got)
	}
}
