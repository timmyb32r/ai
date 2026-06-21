package context

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// UserContext holds user-provided context from TIMMY.md.
type UserContext struct {
	TIMMYmd     string
	CurrentDate string
}

// SystemContext holds VCS state of the working directory.
type SystemContext struct {
	GitStatus     string
	GitBranch     string
	RecentCommits string
}

// Service assembles user and system context.
type Service interface {
	GetUserContext(workDir string) (*UserContext, error)
	GetSystemContext(workDir string) (*SystemContext, error)
}

// NewService creates a new Service.
func NewService() Service {
	return &serviceImpl{}
}

type serviceImpl struct{}

// GetUserContext reads TIMMY.md from workDir and stamps the current date.
func (s *serviceImpl) GetUserContext(workDir string) (*UserContext, error) {
	uc := &UserContext{
		CurrentDate: time.Now().Format("2006-01-02"),
	}
	data, err := os.ReadFile(filepath.Join(workDir, "TIMMY.md"))
	if err != nil {
		if !os.IsNotExist(err) {
			return uc, fmt.Errorf("reading TIMMY.md: %w", err)
		}
		// missing file is not fatal — return empty TIMMYmd
		return uc, nil
	}
	uc.TIMMYmd = string(data)
	return uc, nil
}

// GetSystemContext queries git for status, branch and recent commits.
func (s *serviceImpl) GetSystemContext(workDir string) (*SystemContext, error) {
	sc := &SystemContext{}

	out, err := runGitCmd(workDir, "status", "-s")
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}
	sc.GitStatus = out

	out, err = runGitCmd(workDir, "branch", "--show-current")
	if err != nil {
		return nil, fmt.Errorf("git branch: %w", err)
	}
	sc.GitBranch = strings.TrimSpace(out)

	out, err = runGitCmd(workDir, "log", "-5", "--oneline")
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	sc.RecentCommits = out

	return sc, nil
}

func runGitCmd(workDir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}
