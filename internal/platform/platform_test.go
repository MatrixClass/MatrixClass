package platform

import "testing"

func TestDetectReturnsRuntimeIdentity(t *testing.T) {
	capabilities := Detect()
	if capabilities.OS == "" || capabilities.Architecture == "" {
		t.Fatalf("expected runtime identity, got %+v", capabilities)
	}
}
