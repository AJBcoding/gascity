package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/workclose"
)

func TestBeadsAuditShippedCommandIsRegistered(t *testing.T) {
	cmd := newBeadsCmd(&bytes.Buffer{}, &bytes.Buffer{})
	child, _, err := cmd.Find([]string{"audit-shipped"})
	if err != nil || child == cmd || child.Name() != "audit-shipped" {
		t.Fatalf("audit-shipped command missing: child=%v err=%v", child, err)
	}
}

func TestRenderShippedAuditProvidesOnlyExplicitRemediationChoices(t *testing.T) {
	report := workclose.AuditReport{Complete: true, Groups: []workclose.AuditGroup{{
		StoreRef: "city:test", Findings: []workclose.AuditFinding{{BeadID: "ga-bad", Status: "closed", Violations: []string{"missing landing stamp"}}},
	}}}
	var out bytes.Buffer
	if code := renderShippedAudit(report, "text", &out, &out); code == 0 {
		t.Fatal("invalid shipped record returned success")
	}
	got := out.String()
	for _, want := range []string{"city:test", "ga-bad", "gc landing stamp <gcl-event-id>", "reclassify the outcome"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "automatically") {
		t.Fatalf("audit implied a rewrite: %s", got)
	}
}
