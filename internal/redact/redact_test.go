package redact

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestRedactionSet(t *testing.T) {
	data, err := os.ReadFile("../../testdata/redact.json")
	if err != nil {
		t.Fatal(err)
	}
	var set struct {
		Secret []struct{ Text, Secret string }
		Keep   []string
	}
	if err := json.Unmarshal(data, &set); err != nil {
		t.Fatal(err)
	}
	for _, c := range set.Secret {
		out, found := Text(c.Text)
		if strings.Contains(out, c.Secret) || len(found) == 0 {
			t.Errorf("secret kept:\n  in:  %s\n  out: %s", c.Text, out)
		}
	}
	for _, s := range set.Keep {
		if out, found := Text(s); out != s {
			t.Errorf("ordinary text changed (%v):\n  in:  %s\n  out: %s", found, s, out)
		}
	}
}
