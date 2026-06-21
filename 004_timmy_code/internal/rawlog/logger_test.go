package rawlog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLogger(t *testing.T) {
	dir, err := os.MkdirTemp("", "rawlog-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	logger := NewLogger(dir)

	// --- StartSession ---
	session := logger.StartSession("test-session")
	if session == nil {
		t.Fatal("expected non-nil session")
	}
	sessionDir := filepath.Join(dir, "sessions", "test-session")
	if _, err := os.Stat(sessionDir); os.IsNotExist(err) {
		t.Error("session directory was not created")
	}

	// --- NewMessage (first → msg-01) ---
	msg := session.NewMessage()
	if msg == nil {
		t.Fatal("expected non-nil message")
	}
	msgDir := filepath.Join(sessionDir, "msg-01")
	if _, err := os.Stat(msgDir); os.IsNotExist(err) {
		t.Error("message directory was not created")
	}

	// --- NewRound (first → round-1) ---
	round := msg.NewRound()
	if round == nil {
		t.Fatal("expected non-nil round")
	}
	roundDir := filepath.Join(msgDir, "round-1")
	if _, err := os.Stat(roundDir); os.IsNotExist(err) {
		t.Error("round directory was not created")
	}
	if round.responseFile == nil {
		t.Error("response file should be open after NewRound")
	}

	// --- LogRequest ---
	reqPayload := []byte(`{"message":"Hello"}`)
	if err := round.LogRequest(reqPayload); err != nil {
		t.Fatalf("LogRequest failed: %v", err)
	}
	reqPath := filepath.Join(roundDir, "request.json")
	data, err := os.ReadFile(reqPath)
	if err != nil {
		t.Fatalf("cannot read request.json: %v", err)
	}
	expected := "{\n  \"message\": \"Hello\"\n}"
	if string(data) != expected {
		t.Errorf("request.json mismatch:\n got: %s\nwant: %s", string(data), expected)
	}

	// --- LogResponseLine ---
	if err := round.LogResponseLine([]byte(`{"response":"Hi"}`)); err != nil {
		t.Fatalf("LogResponseLine failed: %v", err)
	}
	if err := round.LogResponseLine([]byte(`{"response":"How are you?"}`)); err != nil {
		t.Fatalf("LogResponseLine failed: %v", err)
	}
	if err := round.CloseResponse(); err != nil {
		t.Fatalf("CloseResponse failed: %v", err)
	}
	respPath := filepath.Join(roundDir, "response.jsonl")
	data, err = os.ReadFile(respPath)
	if err != nil {
		t.Fatalf("cannot read response.jsonl: %v", err)
	}
	expectedResp := "{\"response\":\"Hi\"}\n{\"response\":\"How are you?\"}\n"
	if string(data) != expectedResp {
		t.Errorf("response.jsonl mismatch:\n got: %q\nwant: %q", string(data), expectedResp)
	}

	// --- CreateSubAgent ---
	sub := round.CreateSubAgent("agent1")
	if sub == nil {
		t.Fatal("expected non-nil sub agent")
	}
	subDir := filepath.Join(roundDir, "sub-agent-agent1")
	if _, err := os.Stat(subDir); os.IsNotExist(err) {
		t.Error("sub-agent directory was not created")
	}

	// --- Recursive sub-agent usage ---
	subSession := sub.StartSession("sub-session")
	if subSession == nil {
		t.Fatal("expected non-nil sub session")
	}
	subSessionDir := filepath.Join(subDir, "sessions", "sub-session")
	if _, err := os.Stat(subSessionDir); os.IsNotExist(err) {
		t.Error("sub-agent session directory was not created")
	}

	// --- Auto-increment: second message ---
	msg2 := session.NewMessage()
	if msg2 == nil {
		t.Fatal("expected non-nil second message")
	}
	msg2Dir := filepath.Join(sessionDir, "msg-02")
	if _, err := os.Stat(msg2Dir); os.IsNotExist(err) {
		t.Error("second message directory not created")
	}

	// --- Auto-increment: second round (resets per message) ---
	round2 := msg2.NewRound()
	if round2 == nil {
		t.Fatal("expected non-nil second round")
	}
	round2Dir := filepath.Join(msg2Dir, "round-1")
	if _, err := os.Stat(round2Dir); os.IsNotExist(err) {
		t.Error("second round directory not created")
	}
}
