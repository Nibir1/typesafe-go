package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	typesafe "github.com/nibir1/typesafe-go"
	tsmcp "github.com/nibir1/typesafe-go/integrations/mcp"
)

func newServer(t *testing.T) *tsmcp.Server {
	t.Helper()
	client, err := typesafe.NewClient(
		typesafe.WithAPIKey("sk-test"),
		typesafe.WithBaseURL("http://127.0.0.1:1"),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return tsmcp.NewServer(client, tsmcp.Options{})
}

func TestLoadPoliciesFromDisk(t *testing.T) {
	srv := newServer(t)

	n, err := loadPolicies(srv, filepath.Join("testdata", "policies"))
	if err != nil {
		t.Fatalf("loadPolicies: %v", err)
	}
	if n != 1 {
		t.Errorf("loaded %d files, want 1", n)
	}
	got := srv.Policies()
	if len(got) != 1 || got[0] != "moderation" {
		t.Errorf("Policies() = %v, want [moderation]", got)
	}
}

// A policy file naming a question it does not define must fail at load, where
// an operator sees it, rather than mid-conversation where an agent cannot act
// on it.
func TestLoadRejectsAPolicyMissingAQuestion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{
		"policy": {"weights": {"is_spam": 1, "absent": 1}, "normalize": true},
		"questions": {"is_spam": {"type": "noul", "instructions": "Spam?"}}
	}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := loadPolicies(newServer(t), dir); err == nil {
		t.Fatal("expected an error")
	} else if !strings.Contains(err.Error(), "absent") {
		t.Errorf("the error should name the missing question, got: %v", err)
	}
}

func TestLoadRejectsMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := loadPolicies(newServer(t), dir); err == nil {
		t.Error("expected an error for malformed JSON")
	}
}

func TestLoadRejectsAnEmptyDirectory(t *testing.T) {
	if _, err := loadPolicies(newServer(t), t.TempDir()); err == nil {
		t.Error("expected an error when there is nothing to load")
	}
}

// The policy name defaults to the file name, so a file can omit it.
func TestPolicyNameDefaultsToTheFileName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "triage.json"), []byte(`{
		"policy": {"weights": {"urgent": 1}, "normalize": true, "review_above": 0.5},
		"questions": {"urgent": {"type": "noul", "instructions": "Urgent?"}}
	}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	srv := newServer(t)
	if _, err := loadPolicies(srv, dir); err != nil {
		t.Fatalf("loadPolicies: %v", err)
	}
	if got := srv.Policies(); len(got) != 1 || got[0] != "triage" {
		t.Errorf("Policies() = %v, want [triage]", got)
	}
}

func TestVersionExitsCleanly(t *testing.T) {
	if code := run([]string{"-version"}); code != exitOK {
		t.Errorf("exit code = %d, want %d", code, exitOK)
	}
}

// stdout carries the MCP protocol, so a missing key must be reported on stderr
// with a distinct exit code rather than by writing to the stream.
func TestMissingKeyExitsWithItsOwnCode(t *testing.T) {
	t.Setenv(typesafe.EnvAPIKey, "")
	if code := run(nil); code != exitNoKey {
		t.Errorf("exit code = %d, want %d", code, exitNoKey)
	}
}
