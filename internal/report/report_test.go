package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/__GH_OWNER__/phonylint/internal/rules"
)

func sample() []rules.Finding {
	return []rules.Finding{
		{Rule: rules.RulePhonyShadowedByPath, Severity: rules.SeverityError, Target: "docs", File: "Makefile", Line: 5, Message: "boom"},
		{Rule: rules.RuleLikelyPhony, Severity: rules.SeverityWarning, Target: "clean", File: "Makefile", Line: 9, Message: "hmm"},
	}
}

func TestWriteText(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, sample(), "text"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "Makefile:5") || !strings.Contains(out, "phony-shadowed-by-path") {
		t.Errorf("text output missing expected content: %s", out)
	}
	if !strings.Contains(out, "2 finding(s)") {
		t.Errorf("text output missing summary: %s", out)
	}
}

func TestWriteText_Empty(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, nil, "text"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no findings") {
		t.Errorf("expected 'no findings', got: %s", buf.String())
	}
}

func TestWriteJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, sample(), "json"); err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if len(out) != 2 {
		t.Fatalf("got %d findings, want 2", len(out))
	}
}

func TestWriteGitHub(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, sample(), "github"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "::error ") {
		t.Errorf("expected ::error workflow command, got: %s", out)
	}
	if !strings.Contains(out, "::warning ") {
		t.Errorf("expected ::warning workflow command, got: %s", out)
	}
}

func TestWriteUnknownFormat(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, sample(), "yaml"); err == nil {
		t.Error("expected an error for an unknown format")
	}
}
