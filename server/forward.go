package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Forwarder posts numeric samples as InfluxDB line protocol, e.g. to VictoriaMetrics'
// /influx/write or InfluxDB's /api/v2/write. Each line is
//
//	healthkit,unit=count/min,source=Watch heart_rate=62 1700000000000000000
//
// which VictoriaMetrics stores as the series healthkit_heart_rate{unit,source}.
type Forwarder struct {
	URL         string
	Token       string // optional bearer token
	Measurement string
	Client      *http.Client
}

// Lines renders the samples that carry a number. Samples without one are skipped.
func (f *Forwarder) Lines(samples []*Sample) []byte {
	var b bytes.Buffer
	for _, s := range samples {
		ts := s.start.UnixNano()
		tags := map[string]string{"source": s.Source.Name}
		switch {
		case s.Workout != nil:
			w := s.Workout
			name := w.ActivityName
			if name == "" {
				name = strconv.Itoa(w.ActivityType)
			}
			tags = map[string]string{"activity": name, "source": s.Source.Name}
			f.line(&b, tags, "workout_duration_seconds", w.DurationSeconds, ts)
			if w.EnergyKcal != nil {
				f.line(&b, tags, "workout_energy_kcal", *w.EnergyKcal, ts)
			}
			if w.DistanceMeters != nil {
				f.line(&b, tags, "workout_distance_meters", *w.DistanceMeters, ts)
			}
		case s.Kind == "category":
			// Category samples (sleep stages, mindful minutes...) become their duration,
			// tagged with the category value.
			v := s.ValueLabel
			if v == "" && s.Value != nil {
				v = strconv.FormatFloat(*s.Value, 'f', -1, 64)
			}
			tags["value"] = v
			f.line(&b, tags, metricName(s.Type)+"_seconds", s.end.Sub(s.start).Seconds(), ts)
		case s.Value != nil:
			tags["unit"] = s.Unit
			f.line(&b, tags, metricName(s.Type), *s.Value, ts)
		}
	}
	return b.Bytes()
}

func (f *Forwarder) line(b *bytes.Buffer, tags map[string]string, field string, v float64, ts int64) {
	b.WriteString(escape(f.Measurement, ", "))
	for _, k := range []string{"activity", "source", "unit", "value"} {
		if tags[k] != "" {
			fmt.Fprintf(b, ",%s=%s", k, escape(tags[k], ",= "))
		}
	}
	fmt.Fprintf(b, " %s=%s %d\n", escape(field, ",= "), strconv.FormatFloat(v, 'g', -1, 64), ts)
}

// Send posts the lines. It is a no-op when there is nothing to send.
func (f *Forwarder) Send(ctx context.Context, lines []byte) error {
	if len(lines) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.URL, bytes.NewReader(lines))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if f.Token != "" {
		req.Header.Set("Authorization", "Bearer "+f.Token)
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("forward: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// metricName turns a HealthKit identifier into a snake_case name:
// HKQuantityTypeIdentifierHeartRateVariabilitySDNN -> heart_rate_variability_sdnn.
func metricName(id string) string {
	for _, p := range []string{"HKQuantityTypeIdentifier", "HKCategoryTypeIdentifier", "HKDataTypeIdentifier", "HK"} {
		if strings.HasPrefix(id, p) && len(id) > len(p) {
			id = id[len(p):]
			break
		}
	}
	r := []rune(id)
	var b strings.Builder
	for i, c := range r {
		if unicode.IsUpper(c) && i > 0 {
			prev := r[i-1]
			nextLower := i+1 < len(r) && unicode.IsLower(r[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				b.WriteByte('_')
			}
		}
		if unicode.IsLetter(c) || unicode.IsDigit(c) {
			b.WriteRune(unicode.ToLower(c))
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func escape(s, chars string) string {
	var b strings.Builder
	for _, c := range s {
		if c == '\n' || c == '\r' {
			c = ' '
		}
		if strings.ContainsRune(chars, c) {
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	return b.String()
}
