package main

import (
	"strings"
	"testing"
	"time"
)

// Phone-supplied strings cannot add or shift fields, tags or lines in line protocol.
func TestLinesEscapeClientStrings(t *testing.T) {
	f := &Forwarder{Measurement: "healthkit"}
	v := 62.0
	for _, source := range []string{`Watch\`, "Watch\nhealthkit evil=1 1", "Watch\t\x00", `a\,unit=x`} {
		s := &Sample{Kind: "quantity", Type: "HKQuantityTypeIdentifierHeartRate", Unit: "count/min", Value: &v,
			start: time.Unix(1700000000, 0)}
		s.Source.Name = source
		got := strings.TrimSuffix(string(f.Lines([]*Sample{s})), "\n")
		if strings.ContainsAny(got, "\n\r\t\x00") {
			t.Errorf("source %q: control character in %q", source, got)
		}
		// Unescaped, the line splits into exactly measurement+tags, fields, timestamp.
		if parts := splitUnescaped(got, ' '); len(parts) != 3 || parts[1] != "heart_rate=62" || parts[2] != "1700000000000000000" {
			t.Errorf("source %q: line %q splits into %q", source, got, parts)
		}
		if tags := splitUnescaped(splitUnescaped(got, ' ')[0], ','); len(tags) != 3 {
			t.Errorf("source %q: tags of %q: %q", source, got, tags)
		}
	}
	// Record value names become field names.
	recs := []*RecordIn{{Kind: "activity_summary", Values: []byte(`{"x\\":{"value":1,"unit":"kcal"}}`), start: time.Unix(1, 0)}}
	if got := string(f.RecordLines(recs)); len(splitUnescaped(strings.TrimSpace(got), ' ')) != 3 {
		t.Errorf("record line %q", got)
	}
}

// splitUnescaped splits s at sep not preceded by an escaping backslash, the way line-protocol
// parsers read it.
func splitUnescaped(s string, sep byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case sep:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
