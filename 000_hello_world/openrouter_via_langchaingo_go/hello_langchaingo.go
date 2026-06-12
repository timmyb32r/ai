package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/openai"
)

// readAPIKey reads the OpenRouter API key from ~/.openrouter.
func readAPIKey() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".openrouter")

	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		return "", fmt.Errorf("файл %s пуст — не найден API-ключ", path)
	}
	return key, nil
}

// ask sends prompt to OpenRouter and returns the answer.
//
// langchaingo doesn't know about OpenRouter directly. So we take the
// OpenAI-compatible client and override the base URL with the OpenRouter
// endpoint — the model name has no "openrouter/" prefix, the provider is
// chosen by the base URL, not by the model string.
func ask(ctx context.Context, prompt, apiKey, model string) (string, error) {
	llm, err := openai.New(
		openai.WithToken(apiKey),
		openai.WithBaseURL("https://openrouter.ai/api/v1"),
		openai.WithModel(model),
	)
	if err != nil {
		return "", err
	}
	return llms.GenerateFromSinglePrompt(ctx, llm, prompt)
}

func main() {
	apiKey, err := readAPIKey()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	answer, err := ask(context.Background(), "hello", apiKey, "google/gemma-4-31b-it:free")
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(answer) // "Hello! How can I help you today?"
}
