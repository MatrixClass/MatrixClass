package main

import (
	"errors"
	"testing"
)

func TestTeacherRuntimeCloseErrorsAreJoined(t *testing.T) {
	first := errors.New("media close")
	second := errors.New("transport close")
	joined := joinRuntimeCloseErrors(first, nil, second)
	if !errors.Is(joined, first) || !errors.Is(joined, second) {
		t.Fatalf("joined error does not preserve both causes: %v", joined)
	}
}
