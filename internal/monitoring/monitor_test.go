package monitoring

import "testing"

func TestTargetValidate(t *testing.T) {
	ok := Target{EntityID: "d44ikdomt0tac4ejrmmg", Kind: "vm", Name: "web-1"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for name, mutate := range map[string]func(*Target){
		"no key":  func(t *Target) { t.EntityID = "" },
		"no kind": func(t *Target) { t.Kind = "" },
	} {
		target := ok
		mutate(&target)
		if err := target.Validate(); err == nil {
			t.Errorf("%s: Validate succeeded", name)
		}
	}
}
