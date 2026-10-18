package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

var updateCatalog = flag.Bool("update-catalog", false, "rewrite catalog_gen.go from the app's SDKHealthTypes.swift")

const sdkTypesSwift = "../ios/HealthKitRelay/Generated/SDKHealthTypes.swift"

// Families of identifier constants: the sample kind they make, or "" for types that are not
// samples (characteristics are records, not queryable as samples).
var catalogFamilies = map[string]string{
	"HKQuantityTypeIdentifier":         "quantity",
	"HKCategoryTypeIdentifier":         "category",
	"HKCorrelationTypeIdentifier":      "correlation",
	"HKClinicalTypeIdentifier":         "clinical_record",
	"HKDocumentTypeIdentifier":         "cda_document",
	"HKScoredAssessmentTypeIdentifier": "scored_assessment",
	"HKCharacteristicTypeIdentifier":   "",
}

// Types built by a factory method: their identifier (type.identifier on iOS, as the archive
// has it) and kind; a nil entry is not a sample type. A factory missing here fails the test.
var catalogFactories = map[string]*CatalogType{
	"workoutType":            {Identifier: "HKWorkoutTypeIdentifier", Kind: "workout", DisplayName: "Workout"},
	"workoutRouteType":       {Identifier: "HKWorkoutRouteTypeIdentifier", Kind: "workout_route", DisplayName: "Workout Route"},
	"heartbeatSeriesType":    {Identifier: "HKDataTypeIdentifierHeartbeatSeries", Kind: "heartbeat_series", DisplayName: "Heartbeat Series"},
	"electrocardiogramType":  {Identifier: "HKDataTypeIdentifierElectrocardiogram", Kind: "electrocardiogram", DisplayName: "Electrocardiogram"},
	"audiogramSampleType":    {Identifier: "HKDataTypeIdentifierAudiogram", Kind: "audiogram", DisplayName: "Audiogram"},
	"visionPrescriptionType": {Identifier: "HKVisionPrescriptionTypeIdentifier", Kind: "vision_prescription", DisplayName: "Vision Prescription"},
	// The constant is HKDataTypeIdentifierStateOfMind, its value HKDataTypeStateOfMind.
	"stateOfMindType": {Identifier: "HKDataTypeStateOfMind", Kind: "state_of_mind", DisplayName: "State of Mind"},
	// Assumed equal to the constant's name, like the other HKMedication* identifiers; no
	// archive has one yet to confirm it.
	"medicationDoseEventType":     {Identifier: "HKMedicationDoseEventTypeIdentifierMedicationDoseEvent", Kind: "medication_dose_event", DisplayName: "Medication Dose Event"},
	"activitySummaryType":         nil, // a record (activity_summary)
	"userAnnotatedMedicationType": nil, // a record (medication)
}

var aggregationStyles = map[string]string{
	"Cumulative":                             AggCumulative,
	"Discrete (Arithmetic)":                  AggDiscreteArithmetic,
	"Discrete (Temporally Weighted)":         AggDiscreteTemporal,
	"Discrete (Equivalent Continuous Level)": AggDiscreteEquivalentL,
}

var (
	swiftIdentified = regexp.MustCompile(`^\s*SDKIdentifiedType\(family: "(\w+)", identifier: "(\w+)", ios: "[\d.]+", unit: (nil|"[^"]*"), aggregation: (nil|"[^"]*"), valueEnum: (nil|"\w+")\),$`)
	swiftFactory    = regexp.MustCompile(`^\s*SDKFactoryType\(className: "\w+", selector: "(\w+)", ios: "[\d.]+"\),$`)
	swiftEnum       = regexp.MustCompile(`^\s*"(\w+)": \[(.*)\],$`)
	swiftEnumCase   = regexp.MustCompile(`(-?\d+): "([^"]*)"`)
)

func swiftString(s string) string {
	if s == "nil" {
		return ""
	}
	return strings.Trim(s, `"`)
}

// displayName turns StepCount into "Step Count", HeartRateVariabilitySDNN into "Heart Rate
// Variability SDNN", VO2Max into "VO2 Max".
func displayName(s string) string {
	r := []rune(s)
	var b strings.Builder
	for i, c := range r {
		if i > 0 && unicode.IsUpper(c) {
			prev := r[i-1]
			nextLower := i+1 < len(r) && unicode.IsLower(r[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				b.WriteByte(' ')
			}
		}
		b.WriteRune(c)
	}
	return b.String()
}

// parseSDKTypes reads the app's generated type list into catalog entries.
func parseSDKTypes(src string) ([]CatalogType, error) {
	enums := map[string][]CategoryValue{}
	var types []CatalogType
	var errs []error
	seenFactory := map[string]bool{}
	for _, line := range strings.Split(src, "\n") {
		if m := swiftIdentified.FindStringSubmatch(line); m != nil {
			family, id := m[1], m[2]
			kind, ok := catalogFamilies[family]
			if !ok {
				errs = append(errs, fmt.Errorf("identifier family %s is new: add it to catalogFamilies", family))
				continue
			}
			if kind == "" {
				continue
			}
			t := CatalogType{Identifier: id, Kind: kind, DisplayName: displayName(strings.TrimPrefix(id, family)),
				Unit: swiftString(m[3]), ValueEnum: swiftString(m[5])}
			if agg := swiftString(m[4]); agg != "" {
				if t.Aggregation, ok = aggregationStyles[agg]; !ok {
					errs = append(errs, fmt.Errorf("%s: aggregation style %q is new: add it to aggregationStyles", id, agg))
				}
			}
			if kind == "quantity" && t.Aggregation == "" {
				errs = append(errs, fmt.Errorf("%s: a quantity type without aggregation style", id))
			}
			types = append(types, t)
		} else if m := swiftFactory.FindStringSubmatch(line); m != nil {
			seenFactory[m[1]] = true
			t, ok := catalogFactories[m[1]]
			if !ok {
				errs = append(errs, fmt.Errorf("factory %s is new: add it to catalogFactories", m[1]))
			} else if t != nil {
				types = append(types, *t)
			}
		} else if m := swiftEnum.FindStringSubmatch(line); m != nil {
			for _, c := range swiftEnumCase.FindAllStringSubmatch(m[2], -1) {
				v, _ := strconv.Atoi(c[1])
				enums[m[1]] = append(enums[m[1]], CategoryValue{Value: v, Label: c[2]})
			}
		} else if strings.Contains(line, "SDKIdentifiedType(") || strings.Contains(line, "SDKFactoryType(") {
			errs = append(errs, fmt.Errorf("line not understood: %s", strings.TrimSpace(line)))
		}
	}
	for f := range catalogFactories {
		if !seenFactory[f] {
			errs = append(errs, fmt.Errorf("factory %s is gone from the SDK list: remove it from catalogFactories", f))
		}
	}
	seen := map[string]bool{}
	for i := range types {
		t := &types[i]
		if seen[t.Identifier] {
			errs = append(errs, fmt.Errorf("%s listed twice", t.Identifier))
		}
		seen[t.Identifier] = true
		if t.ValueEnum != "" {
			if t.Values = enums[t.ValueEnum]; t.Values == nil {
				errs = append(errs, fmt.Errorf("%s: no cases for enum %s", t.Identifier, t.ValueEnum))
			}
			sort.Slice(t.Values, func(a, b int) bool { return t.Values[a].Value < t.Values[b].Value })
		}
	}
	if len(types) < 100 {
		errs = append(errs, fmt.Errorf("only %d types found: has the Swift file's format changed?", len(types)))
	}
	return types, errors.Join(errs...)
}

func renderCatalog(types []CatalogType) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("// Code generated by TestCatalogGenerated from ios/HealthKitRelay/Generated/SDKHealthTypes.swift; DO NOT EDIT.\n\n")
	b.WriteString("package main\n\nvar catalogTypes = []CatalogType{\n")
	for _, t := range types {
		fmt.Fprintf(&b, "\t{Identifier: %q, Kind: %q, DisplayName: %q", t.Identifier, t.Kind, t.DisplayName)
		if t.Unit != "" {
			fmt.Fprintf(&b, ", Unit: %q", t.Unit)
		}
		if t.Aggregation != "" {
			fmt.Fprintf(&b, ", Aggregation: %q", t.Aggregation)
		}
		if t.ValueEnum != "" {
			fmt.Fprintf(&b, ", ValueEnum: %q, Values: []CategoryValue{", t.ValueEnum)
			for i, v := range t.Values {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "{%d, %q}", v.Value, v.Label)
			}
			b.WriteString("}")
		}
		b.WriteString("},\n")
	}
	b.WriteString("}\n")
	return format.Source(b.Bytes())
}

// The catalog matches the app's type list. Skipped where the app's sources are not around
// (the Nix build only has server/).
func TestCatalogGenerated(t *testing.T) {
	src, err := os.ReadFile(sdkTypesSwift)
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("no " + sdkTypesSwift)
	} else if err != nil {
		t.Fatal(err)
	}
	types, err := parseSDKTypes(string(src))
	if err != nil {
		t.Fatal(err)
	}
	want, err := renderCatalog(types)
	if err != nil {
		t.Fatal(err)
	}
	if *updateCatalog {
		if err := os.WriteFile("catalog_gen.go", want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, _ := os.ReadFile("catalog_gen.go")
	if !bytes.Equal(got, want) {
		t.Fatal("catalog_gen.go is out of date with " + sdkTypesSwift + ": run go generate in server/")
	}
}

func TestCatalog(t *testing.T) {
	for in, want := range map[string]string{
		"StepCount": "Step Count", "HeartRateVariabilitySDNN": "Heart Rate Variability SDNN", "VO2Max": "VO2 Max",
		"UVExposure": "UV Exposure", "DietaryVitaminB6": "Dietary Vitamin B6",
	} {
		if got := displayName(in); got != want {
			t.Errorf("displayName(%s) = %q, want %q", in, got, want)
		}
	}
	steps, ok := lookupType("HKQuantityTypeIdentifierStepCount")
	if !ok || steps.Kind != "quantity" || steps.Aggregation != AggCumulative || steps.Unit != "count" {
		t.Errorf("steps: %+v", steps)
	}
	hr, _ := lookupType("HKQuantityTypeIdentifierHeartRate")
	if hr == nil || hr.Aggregation == AggCumulative {
		t.Errorf("heart rate: %+v", hr)
	}
	sleep, _ := lookupType("HKCategoryTypeIdentifierSleepAnalysis")
	if sleep == nil || sleep.label(4) != "asleepDeep" {
		t.Errorf("sleep: %+v", sleep)
	}
	if _, ok := lookupType("HKCharacteristicTypeIdentifierBloodType"); ok {
		t.Error("characteristics are not sample types")
	}
	if _, ok := lookupType("HKQuantityTypeIdentifierStepCount' OR 1=1 --"); ok {
		t.Error("lookup must be exact")
	}
	kinds := map[string]bool{}
	for _, k := range Kinds {
		kinds[k.Name] = true
	}
	for _, ct := range catalogTypes {
		if !kinds[ct.Kind] {
			t.Errorf("%s: kind %s is not in Kinds", ct.Identifier, ct.Kind)
		}
		if !typeRE.MatchString(ct.Identifier) {
			t.Errorf("%s: not a valid type identifier", ct.Identifier)
		}
	}
}
