package main

import (
	"sort"
	"strings"
)

// The type catalog: every HealthKit sample type the app can sync, with what the query API
// needs to aggregate it. catalog_gen.go is generated from the app's type list
// (ios/HealthKitRelay/Generated/SDKHealthTypes.swift, itself generated from the iOS SDK):
// run `go generate` after that file changes; TestCatalogGenerated fails while they differ.

//go:generate go test -run TestCatalogGenerated -update-catalog .

// CatalogType is one HealthKit sample type.
type CatalogType struct {
	Identifier  string `json:"identifier"`
	Kind        string `json:"kind"`
	DisplayName string `json:"display_name"`
	// Unit is Apple's canonical unit (quantities). Samples carry the unit they were sent in,
	// which is what the API returns.
	Unit string `json:"unit,omitempty"`
	// Aggregation (quantities) is HKQuantityAggregationStyle: cumulative values are summed
	// over an interval, discrete ones averaged.
	Aggregation string `json:"aggregation,omitempty"`
	// ValueEnum (categories) names the enum of the sample values; Values are its cases.
	ValueEnum string          `json:"value_enum,omitempty"`
	Values    []CategoryValue `json:"values,omitempty"`
}

// CategoryValue is one case of a category type's value enum.
type CategoryValue struct {
	Value int    `json:"value"`
	Label string `json:"label"`
}

// Aggregation styles (HKQuantityAggregationStyle).
const (
	AggCumulative          = "cumulative"
	AggDiscreteArithmetic  = "discreteArithmetic"
	AggDiscreteTemporal    = "discreteTemporallyWeighted"
	AggDiscreteEquivalentL = "discreteEquivalentContinuousLevel"
)

// Kind is a family of sample types; it decides what an aggregate is.
type Kind struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Aggregates  bool   `json:"aggregates"` // has /aggregates
}

// Kinds are the sample kinds the app sends ("kind" of each sample), in catalog order.
var Kinds = []Kind{
	{"quantity", "A number with a unit, e.g. steps (12027 count on a day) or heart rate (67 count/min at 09:04). Aggregates: sum for cumulative types, average/min/max for discrete ones.", true},
	{"category", "A labelled interval, e.g. sleep stage asleepDeep from 01:10 to 01:40, or stand hour stood from 14:00 to 15:00. Aggregates: count and total duration per value.", true},
	{"workout", "A workout with its activity type, duration, energy and distance.", false},
	{"workout_route", "The GPS route of a workout; its points are in the archive's route_points.", false},
	{"heartbeat_series", "Beat-to-beat timings recorded with an HRV reading.", false},
	{"electrocardiogram", "An ECG recording with its classification; voltages are in the archive's ecg_voltages.", false},
	{"audiogram", "A hearing test with its sensitivity points.", false},
	{"correlation", "A group of samples recorded together (blood pressure: systolic and diastolic; food).", false},
	{"clinical_record", "A Health Records (FHIR) resource from a healthcare provider.", false},
	{"cda_document", "A clinical document (CDA).", false},
	{"vision_prescription", "A glasses or contact lens prescription.", false},
	{"state_of_mind", "A logged emotion or daily mood, with valence and labels.", false},
	{"scored_assessment", "A GAD-7 or PHQ-9 questionnaire with its score.", false},
	{"medication_dose_event", "A logged medication dose.", false},
}

var catalogByID = func() map[string]*CatalogType {
	m := make(map[string]*CatalogType, len(catalogTypes))
	for i := range catalogTypes {
		m[catalogTypes[i].Identifier] = &catalogTypes[i]
	}
	return m
}()

// lookupType returns the catalog entry for an identifier; ok is false for anything else.
func lookupType(id string) (*CatalogType, bool) {
	t, ok := catalogByID[id]
	return t, ok
}

// suggestTypes returns up to three catalog identifiers close to id, best first, for an
// unknown type: HKQuantityTypeIdentifierSteps gives HKQuantityTypeIdentifierStepCount.
// Names are compared without case, by the part after "Identifier" (StepCount), so a wrong
// prefix or a missing one still finds the type; matches far behind the best are left out.
func suggestTypes(id string) []string {
	type match struct {
		id    string
		score float64
	}
	q := typeCore(id)
	var ms []match
	for _, t := range catalogTypes {
		score := jaroWinkler(q, typeCore(t.Identifier))
		if len(q) >= 4 && strings.Contains(typeCore(t.Identifier), q) {
			score = max(score, 0.85) // HeartRate: RestingHeartRate, HeartRateRecoveryOneMinute...
		}
		if score >= 0.8 {
			ms = append(ms, match{t.Identifier, score})
		}
	}
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].score > ms[j].score })
	var out []string
	for _, m := range ms {
		if len(out) == 3 || m.score < ms[0].score-0.1 {
			break
		}
		out = append(out, m.id)
	}
	return out
}

// typeCore is the lower-case name after "Identifier": stepcount for
// HKQuantityTypeIdentifierStepCount, or the whole name when nothing follows it.
func typeCore(id string) string {
	s := strings.ToLower(id)
	if i := strings.LastIndex(s, "identifier"); i >= 0 && i+len("identifier") < len(s) {
		return s[i+len("identifier"):]
	}
	return s
}

// jaroWinkler is the Jaro-Winkler similarity of a and b (ASCII), from 0 (nothing in common)
// to 1 (equal); a shared prefix counts extra.
func jaroWinkler(a, b string) float64 {
	if a == b {
		return 1
	}
	if a == "" || b == "" {
		return 0
	}
	window := max(len(a), len(b))/2 - 1
	am, bm := make([]bool, len(a)), make([]bool, len(b))
	m := 0
	for i := range len(a) {
		for j := max(0, i-window); j < min(len(b), i+window+1); j++ {
			if !bm[j] && a[i] == b[j] {
				am[i], bm[j] = true, true
				m++
				break
			}
		}
	}
	if m == 0 {
		return 0
	}
	t, j := 0, 0
	for i := range len(a) {
		if !am[i] {
			continue
		}
		for !bm[j] {
			j++
		}
		if a[i] != b[j] {
			t++
		}
		j++
	}
	fm := float64(m)
	jaro := (fm/float64(len(a)) + fm/float64(len(b)) + (fm-float64(t)/2)/fm) / 3
	prefix := 0
	for prefix < min(4, len(a), len(b)) && a[prefix] == b[prefix] {
		prefix++
	}
	return jaro + float64(prefix)*0.1*(1-jaro)
}

func kindNames() []string {
	out := make([]string, len(Kinds))
	for i, k := range Kinds {
		out[i] = k.Name
	}
	return out
}

func (t *CatalogType) label(v int) string {
	for _, c := range t.Values {
		if c.Value == v {
			return c.Label
		}
	}
	return ""
}
