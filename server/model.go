package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// WireVersion is the newest wire format this server understands. Version 1 (no "version"
// field, no "records") is what app 0.1 sends; it is accepted unchanged.
const WireVersion = 2

// Batch is the request body of POST /v1/samples.
type Batch struct {
	// Version of the wire format; 0 means 1.
	Version int `json:"version,omitempty"`
	// Device describes the uploading phone. Free-form, archived as sent.
	Device json.RawMessage `json:"device,omitempty"`
	// Samples are new or updated HealthKit samples, keyed by UUID.
	Samples []json.RawMessage `json:"samples"`
	// Records (v2) are things without a sample UUID, keyed by (kind, key): series chunks,
	// file chunks, daily activity summaries, characteristics, medications...
	Records []json.RawMessage `json:"records,omitempty"`
	// Deletions are samples HealthKit reported as deleted.
	Deletions []json.RawMessage `json:"deletions"`
	// Trigger (app 0.6.1) says what started the sync that sent the batch, see Triggers.
	// Logged and counted, not archived.
	Trigger string `json:"trigger,omitempty"`
	// AppState (app 0.6.1) is the app's state when the sync started: "active",
	// "inactive" or "background". Logged only.
	AppState string `json:"app_state,omitempty"`
}

// Triggers are the values of Batch.Trigger the server counts. "unknown" counts batches
// without one (apps before 0.6.1) or with a value not listed here, which keeps the metric's
// label set fixed.
var Triggers = []string{"foreground", "observer", "refresh", "unlock", "retry", "unknown"}

func (b *Batch) trigger() string {
	for _, t := range Triggers {
		if b.Trigger == t {
			return t
		}
	}
	return "unknown"
}

func (b *Batch) appState() string {
	switch b.AppState {
	case "active", "inactive", "background":
		return b.AppState
	}
	return "unknown"
}

func (b *Batch) version() int {
	if b.Version < 1 {
		return 1
	}
	return b.Version
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

// RecordIn holds the fields the server reads from a record; it is archived verbatim.
type RecordIn struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
	// Activity summaries: the day's start and its values, forwarded as metrics. Other kinds
	// put anything under "values" (characteristics: a date string, enum objects), so values
	// are read as {value, unit} only where they have that shape, never rejected; see quantities.
	Start  string          `json:"start,omitempty"`
	Values json.RawMessage `json:"values,omitempty"`
	// Series and blob chunks: the sample they belong to, and (series) the points.
	SampleUUID string            `json:"sample_uuid,omitempty"`
	SampleType string            `json:"sample_type,omitempty"`
	Series     string            `json:"series,omitempty"`
	Points     []json.RawMessage `json:"points,omitempty"`

	digest string
	start  time.Time
}

// Quantity is a {"value": 512, "unit": "kcal"} record value.
type Quantity struct {
	Value *float64 `json:"value"`
	Unit  string   `json:"unit"`
}

// quantities returns the values shaped like a Quantity with a number, by name.
func (r *RecordIn) quantities() map[string]Quantity {
	var values map[string]json.RawMessage
	if json.Unmarshal(r.Values, &values) != nil {
		return nil
	}
	out := make(map[string]Quantity, len(values))
	for name, raw := range values {
		var q Quantity
		if json.Unmarshal(raw, &q) == nil && q.Value != nil {
			out[name] = q
		}
	}
	return out
}

var kindRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func parseRecord(raw json.RawMessage) (*RecordIn, error) {
	var r RecordIn
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if !kindRE.MatchString(r.Kind) {
		return nil, fmt.Errorf("invalid kind %q", r.Kind)
	}
	if r.Key == "" || len(r.Key) > 512 {
		return nil, fmt.Errorf("record %s: key must be 1 to 512 bytes", r.Kind)
	}
	if r.Start != "" {
		t, err := time.Parse(time.RFC3339Nano, r.Start)
		if err != nil {
			return nil, fmt.Errorf("record %s/%s: bad start: %v", r.Kind, r.Key, err)
		}
		r.start = t
	}
	// What a chunk says about its sample is used to file it, never a reason to reject it.
	if u, err := normalizeUUID(r.SampleUUID); err == nil {
		r.SampleUUID = u
	} else {
		r.SampleUUID = ""
	}
	if !typeRE.MatchString(r.SampleType) {
		r.SampleType = ""
	}
	if !kindRE.MatchString(r.Series) {
		r.Series = ""
	}
	if r.start.IsZero() && len(r.Points) > 0 {
		// Route and quantity-series points carry their own time: the first one dates the chunk.
		var p struct {
			T     any    `json:"t"`
			Start string `json:"start"`
		}
		json.Unmarshal(r.Points[0], &p)
		ts, _ := p.T.(string)
		if t, err := time.Parse(time.RFC3339Nano, cmpOr(ts, p.Start)); err == nil {
			r.start = t
		}
	}
	// Records are de-duplicated by content: a changed summary is a new record, an unchanged
	// one is a duplicate. Clients encode deterministically (sorted keys).
	sum := sha256.Sum256(raw)
	r.digest = hex.EncodeToString(sum[:16])
	return &r, nil
}

// Deletion is a HealthKit deleted-object notice.
type Deletion struct {
	UUID string `json:"uuid"`
	Type string `json:"type,omitempty"`
}

// typeRE is what a HealthKit type identifier looks like; sample types name directories in
// the archive, so nothing else is accepted.
var typeRE = regexp.MustCompile(`^HK[A-Za-z0-9]{1,120}$`)

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
	if !typeRE.MatchString(s.Type) {
		return nil, fmt.Errorf("sample %s: invalid type %q", s.UUID, s.Type)
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
	if !typeRE.MatchString(d.Type) {
		d.Type = "" // optional, and only a hint of where the sample is: not a reason to reject
	}
	return &d, nil
}

// Items: what the archive stores, built the same way from uploads and from the JSON Lines
// archive of earlier versions (migrate.go).

func sampleItem(s *Sample, raw json.RawMessage, wire int) Item {
	return Item{Op: "sample", UUID: s.UUID, Wire: wire, Type: s.Type, Kind: s.Kind, Start: s.start, Data: raw}
}

func recordItem(r *RecordIn, raw json.RawMessage) Item {
	return Item{Op: "record", UUID: r.SampleUUID, Wire: 2, Type: r.Kind, Key: r.Key, Digest: r.digest,
		Series: r.Series, SeriesType: r.SampleType, Start: r.start, Data: raw}
}

func deletionItem(d *Deletion, raw json.RawMessage) Item {
	return Item{Op: "delete", UUID: d.UUID, Type: d.Type, Data: raw}
}
