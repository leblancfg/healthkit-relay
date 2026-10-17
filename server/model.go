package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Batch is the request body of POST /v1/samples.
type Batch struct {
	// Device describes the uploading phone. Free-form, archived as sent.
	Device json.RawMessage `json:"device,omitempty"`
	// Samples are new or updated HealthKit samples.
	Samples []json.RawMessage `json:"samples"`
	// Deletions are samples HealthKit reported as deleted.
	Deletions []json.RawMessage `json:"deletions"`
}

// Sample holds the fields the server reads from a sample. The full JSON object is archived
// verbatim, so fields added by newer clients are kept even if this struct ignores them.
type Sample struct {
	UUID       string   `json:"uuid"`
	Type       string   `json:"type"`
	Kind       string   `json:"kind"`
	Start      string   `json:"start"`
	End        string   `json:"end"`
	Value      *float64 `json:"value,omitempty"`
	Unit       string   `json:"unit,omitempty"`
	ValueLabel string   `json:"value_label,omitempty"`
	Source     struct {
		Name     string `json:"name,omitempty"`
		BundleID string `json:"bundle_id,omitempty"`
	} `json:"source"`
	Workout *struct {
		ActivityType    int      `json:"activity_type"`
		ActivityName    string   `json:"activity_name,omitempty"`
		DurationSeconds float64  `json:"duration_s"`
		EnergyKcal      *float64 `json:"energy_kcal,omitempty"`
		DistanceMeters  *float64 `json:"distance_m,omitempty"`
	} `json:"workout,omitempty"`

	start, end time.Time
}

// Deletion is a HealthKit deleted-object notice.
type Deletion struct {
	UUID string `json:"uuid"`
	Type string `json:"type,omitempty"`
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func normalizeUUID(s string) (string, error) {
	if !uuidRE.MatchString(s) {
		return "", fmt.Errorf("invalid uuid %q", s)
	}
	return strings.ToUpper(s), nil
}

func parseSample(raw json.RawMessage) (*Sample, error) {
	var s Sample
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	var err error
	if s.UUID, err = normalizeUUID(s.UUID); err != nil {
		return nil, err
	}
	if s.Type == "" {
		return nil, fmt.Errorf("sample %s: missing type", s.UUID)
	}
	if s.start, err = time.Parse(time.RFC3339Nano, s.Start); err != nil {
		return nil, fmt.Errorf("sample %s: bad start: %v", s.UUID, err)
	}
	if s.end, err = time.Parse(time.RFC3339Nano, s.End); err != nil {
		return nil, fmt.Errorf("sample %s: bad end: %v", s.UUID, err)
	}
	if s.end.Before(s.start) {
		return nil, fmt.Errorf("sample %s: end before start", s.UUID)
	}
	return &s, nil
}

func parseDeletion(raw json.RawMessage) (*Deletion, error) {
	var d Deletion
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	var err error
	if d.UUID, err = normalizeUUID(d.UUID); err != nil {
		return nil, err
	}
	return &d, nil
}
