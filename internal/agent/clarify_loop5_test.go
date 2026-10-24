// clarify_loop5_test.go - loop 5 M4: choices decode fail-closed with an
// explicit type switch, every entry inspected.
package agent

import (
	"testing"
)

func TestParseClarifyChoicesTypeSwitch(t *testing.T) {
	if got, err := parseClarifyChoices(nil); err != nil || got != nil {
		t.Fatalf("nil = %v,%v, want nil,nil", got, err)
	}
	got, err := parseClarifyChoices([]any{"a", "b"})
	if err != nil || len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("[]any = %v,%v, want [a b],nil", got, err)
	}
	if _, err := parseClarifyChoices([]any{"a", 1}); err == nil {
		t.Fatal("wrong-typed entry must fail")
	}
	if _, err := parseClarifyChoices([]any{"a", ""}); err == nil {
		t.Fatal("empty entry must fail")
	}
	if _, err := parseClarifyChoices(42); err == nil {
		t.Fatal("wrong type must fail")
	}
	if got, err := parseClarifyChoices("solo"); err != nil || len(got) != 1 || got[0] != "solo" {
		t.Fatalf("string = %v,%v, want [solo],nil", got, err)
	}
	if got, err := parseClarifyChoices([]string{"x"}); err != nil || len(got) != 1 {
		t.Fatalf("[]string = %v,%v, want [x],nil", got, err)
	}
}

func TestValidateClarifyRequestFailClosed(t *testing.T) {
	if err := validateClarifyRequest(ClarifyRequest{}); err == nil {
		t.Fatal("empty question must fail")
	}
	if err := validateClarifyRequest(ClarifyRequest{Question: "q?", Choices: []string{""}}); err == nil {
		t.Fatal("empty choice must fail")
	}
	if err := validateClarifyRequest(ClarifyRequest{Question: "q?", MultiSelect: true}); err == nil {
		t.Fatal("multi_select without choices must fail")
	}
	if err := validateClarifyRequest(ClarifyRequest{Question: "q?", Choices: []string{"a", "b"}, MultiSelect: true}); err != nil {
		t.Fatalf("valid multi_select failed: %v", err)
	}
}
