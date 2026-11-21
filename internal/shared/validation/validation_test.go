package validation

import "testing"

func TestEmailAndOneOf(t *testing.T) {
	good := []string{"a@b.co", "first.last@example.com"}
	bad := []string{"", "a", "a@b", "Name <a@b.co>", "a@@b.co", "a b@c.de"}
	for _, s := range good {
		v := New()
		v.Email("email", s)
		if !v.OK() {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range bad {
		v := New()
		v.Email("email", s)
		if v.OK() {
			t.Errorf("%q should be invalid", s)
		}
	}
	v := New()
	v.OneOf("x", "C", "A", "B")
	if v.OK() || v.Err() == nil {
		t.Error("OneOf must fail")
	}
}
