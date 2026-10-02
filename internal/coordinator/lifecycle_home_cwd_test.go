package coordinator

import (
	"os"
	"path/filepath"
	"testing"
)

// The home directory itself is a valid launch cwd; agents are commonly started
// in ~ and the relay should accept it like any other directory inside home.
func TestResolveCwdAcceptsHome(t *testing.T) {
	home := t.TempDir()
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	l := &Lifecycle{home: home}

	got, err := l.ResolveCwd(home)
	if err != nil {
		t.Fatalf("ResolveCwd(home) = %v, want home accepted", err)
	}
	if got != resolvedHome {
		t.Fatalf("ResolveCwd(home) = %q, want %q", got, resolvedHome)
	}

	sub := filepath.Join(home, "project")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("create subdirectory: %v", err)
	}
	if _, err := l.ResolveCwd(sub); err != nil {
		t.Fatalf("ResolveCwd(subdirectory) = %v, want accepted", err)
	}

	outside := t.TempDir()
	if _, err := l.ResolveCwd(outside); err == nil {
		t.Fatalf("ResolveCwd(directory outside home) succeeded, want rejection")
	}
}
