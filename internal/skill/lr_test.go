// lr_test.go - SL-030 acceptance: the edit-budget schedule is pure,
// deterministic, and floors correctly.
package skill

import "testing"

func TestScheduledEditBudget(t *testing.T) {
	cases := []struct {
		name      string
		base      int
		scheduler string
		epoch     int
		horizon   int
		floor     int
		want      int
	}{
		{"zero base passthrough", 0, "linear", 3, 10, 1, 0},
		{"empty scheduler is constant", 4, "", 5, 10, 1, 4},
		{"unknown scheduler fails closed to constant", 4, "exponential", 5, 10, 1, 4},
		{"constant ignores epoch", 4, "constant", 9, 10, 1, 4},
		{"no horizon never decays", 4, "linear", 5, 0, 1, 4},
		{"linear start", 4, "linear", 0, 10, 1, 4},
		{"linear midpoint rounds half up", 4, "linear", 5, 10, 1, 3},
		{"linear end clamps to floor", 4, "linear", 20, 10, 1, 1},
		{"cosine end clamps to floor", 4, "cosine", 20, 10, 1, 1},
		{"cosine start is base", 4, "cosine", 0, 10, 1, 4},
		{"floor zero decays to zero", 4, "linear", 10, 10, 0, 0},
		{"negative floor clamps to zero", 4, "linear", 10, 10, -3, 0},
		{"floor above base clamps to base", 4, "linear", 5, 10, 9, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ScheduledEditBudget(tc.base, tc.scheduler, tc.epoch, tc.horizon, tc.floor)
			if got != tc.want {
				t.Errorf("ScheduledEditBudget(%d, %q, %d, %d, %d) = %d, want %d",
					tc.base, tc.scheduler, tc.epoch, tc.horizon, tc.floor, got, tc.want)
			}
		})
	}
}
