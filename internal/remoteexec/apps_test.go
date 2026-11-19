package remoteexec

import "testing"

func TestNormalizeDisplayIcon(t *testing.T) {
	got, ok := normalizeDisplayIcon(`"C:\\Program Files\\Demo\\demo.exe",0`)
	if !ok || got != `C:\\Program Files\\Demo\\demo.exe` {
		t.Fatalf("unexpected icon path: %q, %v", got, ok)
	}
}

func TestNormalizeDisplayIconRejectsEmpty(t *testing.T) {
	if _, ok := normalizeDisplayIcon(" "); ok {
		t.Fatal("expected empty display icon to be rejected")
	}
}
