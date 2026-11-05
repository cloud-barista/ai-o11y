package monitoring

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func identityTarget() Target {
	return Target{
		EntityID: "d44ikdomt0tac4ejrmmg",
		Kind:     "vm",
		Name:     "web-1",
		Provider: "aws",
		Region:   "ap-northeast-2",
		Extra:    map[string]string{"namespace": "ns01"},
	}
}

// TestIdentityFragmentSnapshot pins the processor configuration a target
// renders to. Run with -update to rewrite the golden file after a deliberate
// change.
func TestIdentityFragmentSnapshot(t *testing.T) {
	frag, err := IdentityFragment(identityTarget())
	if err != nil {
		t.Fatalf("IdentityFragment: %v", err)
	}
	got, err := json.MarshalIndent(frag.Processors, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got = append(got, '\n')

	golden := filepath.Join("testdata", "identity_fragment.golden.json")
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("fragment differs from %s:\n%s", golden, got)
	}
}

// TestIdentityFragmentStampsOnlyTheKey guards the decision that series carry
// the key and nothing else.
func TestIdentityFragmentStampsOnlyTheKey(t *testing.T) {
	frag, err := IdentityFragment(identityTarget())
	if err != nil {
		t.Fatalf("IdentityFragment: %v", err)
	}
	raw, _ := json.Marshal(frag)
	for _, leaked := range []string{"aws", "ap-northeast-2", "ns01", "web-1"} {
		if strings.Contains(string(raw), leaked) {
			t.Errorf("fragment carries the attribute value %q:\n%s", leaked, raw)
		}
	}
}

func TestIdentityFragmentRejectsTargetWithoutKey(t *testing.T) {
	target := identityTarget()
	target.EntityID = ""
	if _, err := IdentityFragment(target); err == nil {
		t.Fatal("IdentityFragment accepted a target without a key")
	}
}

func TestValidateLabelName(t *testing.T) {
	for _, ok := range []string{"entity_id", "uuid", "_x", "A1"} {
		if err := ValidateLabelName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "cloud.provider", "cloud-region", "1abc", "__name__"} {
		if err := ValidateLabelName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
