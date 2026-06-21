// Package docker provides shared Docker image management utilities used by
// both the server (timmy-code) and client (timmy-client) binaries.
package docker

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// IsInDocker returns true if running inside a Docker container.
func IsInDocker() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

// IsTerminal returns true if f is a character device (real terminal).
func IsTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// ImageExists returns true if a Docker image is present locally.
func ImageExists(name string) bool {
	cmd := exec.Command("docker", "image", "inspect", name)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// FindProjectRoot locates the timmy-code project root by walking up from the
// executable path or current working directory, looking for a Dockerfile.
func FindProjectRoot() (string, error) {
	if exe, err := os.Executable(); err == nil {
		if exe, err = filepath.EvalSymlinks(exe); err == nil {
			if root := walkUp(filepath.Dir(exe)); root != "" {
				return root, nil
			}
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		if root := walkUp(cwd); root != "" {
			return root, nil
		}
	}
	return "", fmt.Errorf("project root not found")
}

func walkUp(dir string) string {
	dir, _ = filepath.Abs(dir)
	for {
		if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// SourceHash computes a SHA-256 hash over all files that affect the binary:
// go.mod, go.sum, all .go files, and the internal/rawlog/webui/ directory.
func SourceHash(root string) (string, error) {
	h := sha256.New()
	for _, f := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			continue
		}
		io.WriteString(h, f)
		h.Write(data)
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", ".timmy-code", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		relPath, _ := filepath.Rel(root, path)
		if !strings.HasSuffix(relPath, ".go") &&
			!strings.HasPrefix(relPath, "internal/rawlog/webui/") {
			return nil
		}
		io.WriteString(h, relPath)
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		io.Copy(h, f)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// EnsureImage checks whether the Docker image timmy-code:latest exists and is
// up-to-date relative to the project source. If not, it rebuilds the image.
// It returns nil if the image is ready, or an error if the build fails.
func EnsureImage(projectRoot string) error {
	curHash, hashErr := SourceHash(projectRoot)
	needRebuild := true

	if hashErr == nil && ImageExists("timmy-code:latest") {
		hashFile := filepath.Join(projectRoot, ".timmy-code", ".docker-image-hash")
		if stored, readErr := os.ReadFile(hashFile); readErr == nil {
			if string(stored) == curHash {
				needRebuild = false
			}
		}
	}

	if !needRebuild {
		return nil
	}

	fmt.Fprintln(os.Stderr, "Building Docker image timmy-code...")
	buildCmd := exec.Command("docker", "build", "-t", "timmy-code", projectRoot)
	buildCmd.Stdout = os.Stderr
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("docker build failed: %w", err)
	}

	if hashErr == nil {
		hashDir := filepath.Join(projectRoot, ".timmy-code")
		os.MkdirAll(hashDir, 0755)
		os.WriteFile(filepath.Join(hashDir, ".docker-image-hash"), []byte(curHash), 0644)
	}
	return nil
}

// GetFreePort finds a free TCP port and returns it. The port is immediately
// released after discovery (best-effort, subject to races).
func GetFreePort() (int, error) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port, nil
}
