package audit

import (
	"encoding/json"
	"testing"
)

func TestRedact(t *testing.T) {
	in := `{"username":"a","password":"p","nested":{"refresh_token":"t","ok":1},"list":[{"Authorization":"x"}],"password_hash":"h"}`
	var v any
	_ = json.Unmarshal([]byte(in), &v)
	out, _ := json.Marshal(redact(v))
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["password"] != "[REDACTED]" || m["password_hash"] != "[REDACTED]" {
		t.Fatalf("password not redacted: %s", out)
	}
	if m["nested"].(map[string]any)["refresh_token"] != "[REDACTED]" || m["nested"].(map[string]any)["ok"].(float64) != 1 {
		t.Fatalf("nested handling wrong: %s", out)
	}
	if m["list"].([]any)[0].(map[string]any)["Authorization"] != "[REDACTED]" || m["username"] != "a" {
		t.Fatalf("list/plain handling wrong: %s", out)
	}
}
