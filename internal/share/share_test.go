package share

import (
	"strings"
	"testing"

	"github.com/dmitriimaksimovdevelop/melisai/internal/model"
)

func TestRoundTrip(t *testing.T) {
	r := sampleReport()
	p := BuildPayload(r)

	encoded, err := Encode(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	got, err := Decode(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got.V != PayloadVersion {
		t.Errorf("version = %d, want %d", got.V, PayloadVersion)
	}
	if got.Meta.Hostname != r.Metadata.Hostname {
		t.Errorf("hostname = %q, want %q", got.Meta.Hostname, r.Metadata.Hostname)
	}
	if got.Summary.HealthScore != r.Summary.HealthScore {
		t.Errorf("health = %d, want %d", got.Summary.HealthScore, r.Summary.HealthScore)
	}
	if len(got.Summary.Anomalies) != len(r.Summary.Anomalies) {
		t.Errorf("anomalies count = %d, want %d",
			len(got.Summary.Anomalies), len(r.Summary.Anomalies))
	}
}

func TestBuildURL(t *testing.T) {
	tests := []struct {
		name       string
		base       string
		wantPrefix string
		wantErr    bool
	}{
		{"default", "", "https://melisai.dev/r#", false},
		{"custom_https", "https://example.com/x", "https://example.com/x#", false},
		{"custom_http", "http://localhost:8080/r", "http://localhost:8080/r#", false},
		{"colon_only", ":not a url", "", true},
		{"javascript_scheme", "javascript:alert(1)", "", true},
		{"data_scheme", "data:text/html,abc", "", true},
		{"ftp_scheme", "ftp://example.com/", "", true},
		{"scheme_only", "https://", "", true},
		{"bare_word", "not a url", "", true},
	}

	p := BuildPayload(sampleReport())
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			url, err := BuildURL(p, tc.base)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr=%v", err, tc.wantErr)
			}
			if !tc.wantErr && !strings.HasPrefix(url, tc.wantPrefix) {
				t.Errorf("url = %q, want prefix %q", url, tc.wantPrefix)
			}
		})
	}
}

// TestEncodedSize guards against a regression that would push the URL over
// what Slack and most browsers will accept. A typical summary should stay
// well under 50 KB encoded; we set the budget at 80 KB to leave headroom
// for richer recommendations without surprising the maintainer with a
// silent breach. If this trips, either trim the payload or move the
// receiver to a backend store.
func TestEncodedSize(t *testing.T) {
	p := BuildPayload(sampleReport())
	encoded, err := Encode(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	const budget = 80 * 1024
	if len(encoded) > budget {
		t.Errorf("encoded size = %d bytes, exceeds budget %d", len(encoded), budget)
	}
	t.Logf("encoded payload: %d bytes (15 anomalies, 10 recs, 4 USE resources)", len(encoded))
}

func TestDecodeAcceptsFragmentPrefix(t *testing.T) {
	p := BuildPayload(sampleReport())
	encoded, err := Encode(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Decode("#" + encoded); err != nil {
		t.Errorf("decode with # prefix: %v", err)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	cases := []string{"", "!!!not-base64!!!", "YWJjZA"}
	for _, s := range cases {
		if _, err := Decode(s); err == nil {
			t.Errorf("Decode(%q) succeeded, want error", s)
		}
	}
}

func TestEmptyAnomaliesRoundTrip(t *testing.T) {
	r := &model.Report{
		Metadata: model.Metadata{
			Hostname:      "host1",
			KernelVersion: "6.8.0",
			Profile:       "quick",
			Version:       "0.4.1",
		},
		Summary: model.Summary{
			HealthScore: 100,
			Anomalies:   nil,
			Resources:   map[string]model.USEMetric{},
		},
	}
	p := BuildPayload(r)
	encoded, err := Encode(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := Decode(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Summary.HealthScore != 100 {
		t.Errorf("health = %d, want 100", got.Summary.HealthScore)
	}
}

// sampleReport builds a realistic-size report fixture. Sized to roughly
// match what `melisai collect --profile standard` produces on a busy host:
// ~15 anomalies, 5 USE resources, ~10 recommendations with multi-command
// fixes. Used to assert encoded payload size stays in budget.
func sampleReport() *model.Report {
	anomalies := make([]model.Anomaly, 15)
	for i := range anomalies {
		anomalies[i] = model.Anomaly{
			Severity:  "high",
			Category:  "cpu",
			Metric:    "throttling",
			Message:   "container CPU throttled — quota too tight for the workload",
			Value:     "24.5%",
			Threshold: "5%",
		}
	}

	recs := make([]model.Recommendation, 10)
	for i := range recs {
		recs[i] = model.Recommendation{
			Priority:       1,
			Category:       "network",
			Type:           "sysctl",
			Title:          "Raise net.core.rmem_max for high-BDP links",
			Commands:       []string{"sysctl -w net.core.rmem_max=134217728"},
			Persistent:     []string{"echo 'net.core.rmem_max=134217728' >> /etc/sysctl.d/99-melisai.conf"},
			ExpectedImpact: "Reduces TCP RcvQDrop under bursty inbound traffic",
			Evidence:       "TCPRcvQDrop=1.2k/s observed across 3 interfaces over 30s window",
			Source:         "melisai/anomaly:net.rcvq_drop",
		}
	}

	return &model.Report{
		Metadata: model.Metadata{
			Tool:          "melisai",
			Version:       "0.4.1",
			SchemaVersion: "1.1.0",
			Hostname:      "prod-rtb-01",
			Timestamp:     "2026-05-25T10:00:00Z",
			Duration:      "30s",
			Profile:       "standard",
			KernelVersion: "6.8.0-90-generic",
			Arch:          "amd64",
			CPUs:          32,
			MemoryGB:      128,
			ContainerEnv:  "kubernetes",
			CgroupVersion: 2,
		},
		Summary: model.Summary{
			HealthScore: 68,
			Anomalies:   anomalies,
			Resources: map[string]model.USEMetric{
				"cpu":     {Utilization: 78.5, Saturation: 12.0, Errors: 0},
				"memory":  {Utilization: 65.0, Saturation: 0.0, Errors: 0},
				"disk":    {Utilization: 45.0, Saturation: 3.0, Errors: 2},
				"network": {Utilization: 0.0, Saturation: 850.0, Errors: 14},
			},
			Recommendations: recs,
		},
	}
}
