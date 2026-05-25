package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/dmitriimaksimovdevelop/melisai/internal/model"
	"github.com/dmitriimaksimovdevelop/melisai/internal/share"
)

var (
	shareBaseURL string
)

var shareCmd = &cobra.Command{
	Use:   "share <report.json>",
	Short: "Encode a report summary into a shareable URL",
	Long: `Reads a melisai JSON report and prints a URL that embeds the
diagnostic summary (health score, anomalies, USE metrics, recommendations)
inside the URL fragment. No data is uploaded anywhere — the receiving
page at melisai.dev/r decodes and renders the payload entirely
client-side.

Use "-" to read the report from stdin.

Privacy note: the payload includes hostname, kernel version, and the
recommendation evidence text. Treat the resulting URL with the same
care as the report file itself.

Example:
  melisai collect --profile quick -o report.json
  melisai share report.json
  # → https://melisai.dev/r#H4sIAAAAAAAA...`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		report, err := loadReportForShare(args[0])
		if err != nil {
			return err
		}

		payload := share.BuildPayload(report)
		url, err := share.BuildURL(payload, shareBaseURL)
		if err != nil {
			return fmt.Errorf("build url: %w", err)
		}

		fmt.Println(url)
		return nil
	},
}

func init() {
	shareCmd.Flags().StringVar(&shareBaseURL, "base-url", share.DefaultBaseURL,
		"Receiver base URL (must be http or https with a host)")
}

// loadReportForShare reads a Report from a file path or stdin (when path is "-").
func loadReportForShare(path string) (*model.Report, error) {
	var src io.Reader
	if path == "-" {
		src = os.Stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", path, err)
		}
		defer f.Close()
		src = f
	}

	var report model.Report
	if err := json.NewDecoder(src).Decode(&report); err != nil {
		return nil, fmt.Errorf("parse report: %w", err)
	}
	return &report, nil
}
