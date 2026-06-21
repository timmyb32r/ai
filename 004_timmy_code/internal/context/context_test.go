package context

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetUserContext_WithFile(t *testing.T) {
	dir := t.TempDir()
	content := "# Test\nproject info"
	if err := os.WriteFile(filepath.Join(dir, "TIMMY.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewService()
	uc, err := svc.GetUserContext(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if uc.TIMMYmd != content {
		t.Errorf("TIMMYmd = %q, want %q", uc.TIMMYmd, content)
	}
	if uc.CurrentDate == "" {
		t.Error("CurrentDate should not be empty")
	}
}

func TestGetUserContext_MissingFile(t *testing.T) {
	dir := t.TempDir()

	svc := NewService()
	uc, err := svc.GetUserContext(dir)
	if err != nil {
		t.Fatalf("unexpected error when file is missing: %v", err)
	}
	if uc.TIMMYmd != "" {
		t.Errorf("expected empty TIMMYmd, got %q", uc.TIMMYmd)
	}
}

func TestGetSystemContext_OutsideGitRepo(t *testing.T) {
	dir := t.TempDir()

	svc := NewService()
	_, err := svc.GetSystemContext(dir)
	if err == nil {
		t.Fatal("expected error outside git repo")
	}
}

func TestGetSystemContext_InGitRepo(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	svc := NewService()
	sc, err := svc.GetSystemContext(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc.GitBranch == "" {
		t.Error("expected non-empty branch")
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %s", args, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "README.md"},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %s", args, out)
		}
	}
}

func TestRunGitCmd_StringOutput(t *testing.T) {
	out, err := runGitCmd(".", "status", "-s")
	if err != nil {
		t.Fatalf("runGitCmd failed: %v", err)
	}
	if !strings.Contains(out, "") {
		// just checking it runs without error
	}
}
