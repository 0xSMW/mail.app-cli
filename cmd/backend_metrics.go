package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/0xSMW/mail.app-cli/v3/internal/mailindex"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"io"
	"os"
	"path/filepath"
)

// Optional local measurement output deliberately excludes message and account data.
// Exclusive creation keeps a benchmark invocation from replacing existing files.
func (a *application) writeBackendMetrics(stderr io.Writer) {
	path := os.Getenv("MAIL_APP_CLI_METRICS_FILE")
	if path == "" {
		return
	}
	if !filepath.IsAbs(path) {
		fmt.Fprintln(stderr, "backend metrics: path must be absolute")
		return
	}
	data := struct {
		Backend    string              `json:"backend"`
		Automation mail.BackendStats   `json:"automation"`
		Index      mailindex.Stats     `json:"index"`
		Local      mail.LocalReadStats `json:"local"`
	}{Automation: mail.BackendStatistics(), Index: mailindex.Statistics(), Local: mail.LocalReadStatistics()}
	if a.mailClient != nil {
		data.Backend = a.mailClient.Backend()
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fmt.Fprintf(stderr, "backend metrics: %v\n", err)
		return
	}
	err = json.NewEncoder(file).Encode(data)
	closeErr := file.Close()
	if err != nil {
		fmt.Fprintf(stderr, "backend metrics: %v\n", err)
	} else if closeErr != nil {
		fmt.Fprintf(stderr, "backend metrics: %v\n", closeErr)
	}
}
