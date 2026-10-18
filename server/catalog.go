package main

import "sort"

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
	{"quantity", "A number with a unit (steps, heart rate, body mass...). Aggregates: sum for cumulative types, average/min/max for discrete ones.", true},
	{"category", "An enumerated value over a time interval (sleep stages, stand hours, symptoms...). Aggregates: count and total duration per value.", true},
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

// catalogIDs lists every identifier, sorted, for the spec's enum.
func catalogIDs() []string {
	out := make([]string, 0, len(catalogTypes))
	for _, t := range catalogTypes {
		out = append(out, t.Identifier)
	}
	sort.Strings(out)
	return out
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
