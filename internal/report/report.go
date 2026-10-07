// Package report formats findings as text, JSON, or GitHub Actions
// workflow commands.
package report

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/quietpelican42/phonylint/internal/rules"
)

// Write renders findings in format ("text", "json", or "github") to w.
func Write(w io.Writer, findings []rules.Finding, format string) error {
	switch format {
	case "json":
		return writeJSON(w, findings)
	case "github":
		return writeGitHub(w, findings)
	case "text", "":
		return writeText(w, findings)
	default:
		return fmt.Errorf("unknown format %q (want text, json, or github)", format)
	}
}

func writeText(w io.Writer, findings []rules.Finding) error {
	if len(findings) == 0 {
		_, err := fmt.Fprintln(w, "phonylint: no findings")
		return err
	}
	for _, f := range findings {
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		if loc == "" {
			loc = "-"
		}
		if _, err := fmt.Fprintf(w, "%s: %s: [%s] %s\n", loc, f.Severity, f.Rule, f.Message); err != nil {
			return err
		}
	}
	counts := map[string]int{}
	for _, f := range findings {
		counts[f.Severity]++
	}
	_, err := fmt.Fprintf(w, "\n%d finding(s): %d error, %d warning, %d info\n",
		len(findings), counts[rules.SeverityError], counts[rules.SeverityWarning], counts[rules.SeverityInfo])
	return err
}

type jsonFinding struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Target   string `json:"target"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Message  string `json:"message"`
}

func writeJSON(w io.Writer, findings []rules.Finding) error {
	out := make([]jsonFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, jsonFinding{
			Rule: f.Rule, Severity: f.Severity, Target: f.Target,
			File: f.File, Line: f.Line, Message: f.Message,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func writeGitHub(w io.Writer, findings []rules.Finding) error {
	for _, f := range findings {
		cmd := "notice"
		switch f.Severity {
		case rules.SeverityError:
			cmd = "error"
		case rules.SeverityWarning:
			cmd = "warning"
		}
		params := "title=" + f.Rule
		if f.File != "" {
			params += ",file=" + f.File
		}
		if f.Line > 0 {
			params += fmt.Sprintf(",line=%d", f.Line)
		}
		if _, err := fmt.Fprintf(w, "::%s %s::%s\n", cmd, params, f.Message); err != nil {
			return err
		}
	}
	return nil
}
