package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/dmitriimaksimovdevelop/melisai/internal/model"
	"github.com/dmitriimaksimovdevelop/melisai/internal/share"
)

var (
	shareBaseURL string
	shareAPIURL  string
	shareOffline bool
	shareTimeout time.Duration
)

var shareCmd = &cobra.Command{
	Use:   "share <report.json>",
	Short: "Encode a report summary into a shareable URL",
	Long: `Reads a melisai JSON report and prints a URL that embeds the
diagnostic summary (health score, anomalies, USE metrics, recommendations).

By default the payload is uploaded to the melisai.dev short-link backend
and a URL like https://melisai.dev/r/Xa9bC3kp is printed. If the backend
is unreachable (no network, airgapped server, 5xx, timeout) the command
falls back to a self-contained fragment URL of the form
https://melisai.dev/r#<base64url-gzip> — these are longer but require
no server.

Use "-" to read the report from stdin.

Privacy note: every variant of the URL includes hostname, kernel version,
and recommendation evidence text. Treat the URL with the same care as
the report file itself.

Examples:
  melisai collect --profile quick -o report.json
  melisai share report.json                       # short link, with fallback
  melisai share --offline report.json             # force fragment URL
  melisai share --api-url=http://lab/api/r -      # custom backend, stdin`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		report, err := loadReportForShare(args[0])
		if err != nil {
			return err
		}
		payload := share.BuildPayload(report)

		if !shareOffline {
			url, uploadErr := share.Upload(cmd.Context(), payload, shareAPIURL, shareTimeout)
			if uploadErr == nil {
				fmt.Println(url)
				return nil
			}
			// Fall through to fragment with a warning. The CLI must
			// still succeed so users on airgapped boxes can ship the
			// long URL via whatever transport they have.
			fmt.Fprintf(os.Stderr,
				"warning: short-link upload failed (%v); falling back to self-contained URL\n",
				uploadErr)
		}

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
		"Viewer base URL used for fragment fallback (must be http or https with a host)")
	shareCmd.Flags().StringVar(&shareAPIURL, "api-url", share.DefaultAPIURL,
		"Backend endpoint for short-link upload")
	shareCmd.Flags().BoolVar(&shareOffline, "offline", false,
		"Skip the backend upload and emit a self-contained fragment URL")
	shareCmd.Flags().DurationVar(&shareTimeout, "timeout", share.DefaultUploadTimeout,
		"Timeout for the upload attempt before falling back to fragment URL")
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
