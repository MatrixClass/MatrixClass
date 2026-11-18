package main

import (
	"testing"
)

func TestNewStudentRuntime(t *testing.T) {
	rt, err := NewRuntime("301机房", "stu-test", "测试学生", "202699", "127.0.0.1:39199", t.TempDir())
	if err != nil {
		t.Fatalf("NewRuntime failed: %v", err)
	}
	defer rt.Close()
	if rt.App == nil || rt.Transport == nil || rt.Media == nil || rt.Control == nil {
		t.Fatalf("expected all runtime modules initialized")
	}
}
