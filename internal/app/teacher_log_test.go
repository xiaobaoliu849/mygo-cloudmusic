package app

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// A new session's log moves each kept one a session back, and the oldest
// past keptTeacherLogs goes, rather than the last session's being lost.
func TestRotateTeacherLogs(t *testing.T) {
	dir := t.TempDir()
	name := func(n int) string {
		if n == 0 {
			return filepath.Join(dir, "teacher.log")
		}
		return filepath.Join(dir, fmt.Sprintf("teacher.%d.log", n))
	}
	for n := range keptTeacherLogs {
		if err := os.WriteFile(name(n), []byte(fmt.Sprint("session ", n)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rotateTeacherLogs(dir)
	if _, err := os.Stat(name(0)); !os.IsNotExist(err) {
		t.Fatalf("teacher.log is still there, for the new session to overwrite: %v", err)
	}
	for n := 1; n < keptTeacherLogs; n++ {
		got, err := os.ReadFile(name(n))
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprint("session ", n-1); string(got) != want {
			t.Errorf("%s holds %q, want %q", filepath.Base(name(n)), got, want)
		}
	}
	if _, err := os.Stat(name(keptTeacherLogs)); !os.IsNotExist(err) {
		t.Errorf("more than %d logs kept", keptTeacherLogs)
	}
}
