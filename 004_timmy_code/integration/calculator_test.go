package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCalculatorEndToEnd runs timmy-code in --execute mode
// with a calculator prompt and verifies it produces a working Go program.
func TestCalculatorEndToEnd(t *testing.T) {
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		t.Skip("DEEPSEEK_API_KEY not set — skipping integration test")
	}

	// Find repo root (where go.mod lives)
	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// Walk up to find go.mod
	for {
		if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(repoRoot)
		if parent == repoRoot {
			t.Fatal("cannot find go.mod — run from repo root")
		}
		repoRoot = parent
	}

	// Create temp work directory inside repo for the test project
	workDir, err := os.MkdirTemp(repoRoot, ".timmy-integration-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(workDir)

	// Write TIMMY.md
	if err := os.WriteFile(filepath.Join(workDir, "TIMMY.md"), []byte("# Integration test\n"), 0644); err != nil {
		t.Fatalf("failed to write TIMMY.md: %v", err)
	}

	prompt := `Создай консольный калькулятор на Go в файле calc.go.
Требования:
- При запуске выводит приглашение: "Введите выражение (например 2 + 3): "
- Поддерживает числа: положительные, отрицательные, целые, дробные (через точку, например 3.14)
- Поддерживает операции: + (сложение), - (вычитание), * (умножение), / (деление)
- После нажатия Enter выводит результат: "Результат: <число>"
- При делении на ноль: выводит "Ошибка: деление на ноль"
- При некорректном вводе: выводит "Ошибка: неверный формат"
- Пустая строка или "exit" — выход из программы
- Используй только стандартную библиотеку Go
- Файл должен называться calc.go в текущей директории`

	// Build timmy-code binary
	binaryPath := filepath.Join(workDir, "timmy-code")
	buildCmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/timmy-code")
	buildCmd.Dir = repoRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build timmy-code: %v\n%s", err, out)
	}

	// Run timmy-code in --execute mode (5 min timeout)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	runCmd := exec.CommandContext(ctx, binaryPath, "--execute", prompt)
	runCmd.Dir = workDir
	runCmd.Env = append(os.Environ(), "DEEPSEEK_API_KEY="+apiKey)

	t.Logf("Running timmy-code --execute in %s...", workDir)
	output, err := runCmd.CombinedOutput()
	t.Logf("timmy-code output:\n%s", string(output))

	if err != nil {
		if ctx.Err() != nil {
			t.Fatalf("timmy-code timed out after 300s")
		}
		t.Fatalf("timmy-code failed: %v\nOutput:\n%s", err, string(output))
	}

	// Verify: calc.go should exist in workDir
	calcPath := filepath.Join(workDir, "calc.go")
	if _, err := os.Stat(calcPath); os.IsNotExist(err) {
		// Search recursively
		var found []string
		filepath.Walk(workDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.Name() == "calc.go" {
				found = append(found, path)
			}
			return nil
		})
		if len(found) == 0 {
			t.Fatalf("calc.go was not created anywhere under %s", workDir)
		}
		calcPath = found[0]
	}

	// Read calc.go
	calcContent, err := os.ReadFile(calcPath)
	if err != nil {
		t.Fatalf("failed to read calc.go: %v", err)
	}
	if len(calcContent) < 50 {
		t.Fatalf("calc.go is too short (%d bytes)", len(calcContent))
	}
	t.Logf("calc.go: %d bytes at %s", len(calcContent), calcPath)

	// Build calc.go
	calcBinary := filepath.Join(workDir, "calc")
	buildCalc := exec.Command("go", "build", "-o", calcBinary, calcPath)
	buildCalc.Dir = workDir
	if out, err := buildCalc.CombinedOutput(); err != nil {
		t.Fatalf("calc.go does not compile: %v\n%s\nContent:\n%s", err, out, string(calcContent))
	}
	t.Log("calc.go compiles successfully")

	// Test calc with sample expressions
	tests := []struct {
		name     string
		input    string
		contains string
	}{
		{"addition", "2 + 3\n", "5"},
		{"subtraction", "10 - 7\n", "3"},
		{"multiplication", "4 * 5\n", "20"},
		{"division", "15 / 3\n", "5"},
		{"negative", "-5 + 8\n", "3"},
		{"float", "3.14 + 2.86\n", "6"},
		{"div zero", "5 / 0\n", "деление на ноль"},
		{"exit", "exit\n", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(calcBinary)
			cmd.Dir = workDir
			cmd.Stdin = strings.NewReader(tc.input + "exit\n")
			out, _ := cmd.CombinedOutput()
			output := string(out)
			if tc.contains != "" && !strings.Contains(strings.ToLower(output), strings.ToLower(tc.contains)) {
				t.Errorf("%s: input=%q, expected output containing %q, got %q",
					tc.name, tc.input, tc.contains, output)
			} else {
				t.Logf("%s: OK (%q in output)", tc.name, tc.contains)
			}
		})
	}
}
