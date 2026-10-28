package cmd

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/config"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
)

func TestConfigShowPlainAndJSONPreserveValuesAndSources(t *testing.T) {
	resolved := config.Resolved{
		Account:     config.Value{Value: "Work", Source: config.SourceFlag},
		Mailbox:     config.Value{Value: "Archive", Source: config.SourceEnv},
		Output:      config.Value{Value: "auto", Source: config.SourceDefault},
		SendEnabled: config.Value{Value: "true", Source: config.SourceDefault},
		Path:        "/fixture/config.json",
	}
	for _, format := range []output.Format{output.FormatPlain, output.FormatJSON} {
		t.Run(string(format), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			a := newApplication()
			a.resolved = resolved
			var err error
			a.writer, err = output.New(format, &stdout, &stderr, false, "", "config show", mail.SchemaVersion)
			if err != nil {
				t.Fatal(err)
			}
			a.buildConfigCommands()
			show, _, err := a.configCmd.Find([]string{"show"})
			if err != nil {
				t.Fatal(err)
			}
			if err := show.RunE(show, nil); err != nil {
				t.Fatal(err)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr=%q", stderr.String())
			}
			if format == output.FormatPlain {
				want := "KEY VALUE SOURCE account Work flag mailbox Archive env output auto default send.enabled true default file: /fixture/config.json"
				if got := strings.Join(strings.Fields(stdout.String()), " "); got != want {
					t.Fatalf("plain config=%q, want %q", got, want)
				}
				return
			}
			var envelope struct {
				Data config.Resolved `json:"data"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(envelope.Data, resolved) {
				t.Fatalf("JSON config=%+v, want %+v", envelope.Data, resolved)
			}
		})
	}
}
