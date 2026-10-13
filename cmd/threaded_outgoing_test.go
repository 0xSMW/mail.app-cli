package cmd

import (
	"reflect"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/config"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

func TestThreadedOutgoingCLIRejectsInvalidSelectionBeforeAutomation(t *testing.T) {
	for _, path := range [][]string{{"send"}, {"drafts", "create"}} {
		for _, tc := range []struct {
			name          string
			flags         []string
			mailbox, body bool
			want          string
		}{
			{"both sources", []string{"--reply-to", "42", "--forward-of", "43"}, true, true, "mutually exclusive"},
			{"reply all alone", []string{"--reply-all"}, true, true, "requires --reply-to"},
			{"reply all with forward", []string{"--reply-all", "--forward-of", "42"}, true, true, "requires --reply-to"},
			{"empty source", []string{"--reply-to", ""}, true, true, "positive message ID"},
			{"invalid source", []string{"--forward-of", "bad"}, true, true, "positive message ID"},
			{"negative source", []string{"--reply-to", "-42"}, true, true, "positive message ID"},
			{"implicit mailbox", []string{"--reply-to", "42"}, false, true, "explicit --mailbox"},
			{"implicit body", []string{"--reply-to", "42"}, true, false, "--body or --body-file"},
		} {
			t.Run(strings.Join(path, " ")+"/"+tc.name, func(t *testing.T) {
				args := append(append([]string{}, path...), "--backend", "native", "--account", "Personal", "--to", "recipient@example.com", "--subject", "Edited")
				if tc.mailbox {
					args = append(args, "--mailbox", "INBOX")
				}
				if tc.body {
					args = append(args, "--body", "Edited complete body")
				}
				args = append(args, tc.flags...)
				code, stdout, stderr := run(t, args...)
				if code != clierr.ExitCode(clierr.CodeUsage) || !strings.Contains(stderr, tc.want) {
					t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
				}
			})
		}
	}
}

func TestThreadedOutgoingCLIRejectsLegacyBackend(t *testing.T) {
	for _, path := range [][]string{{"send"}, {"drafts", "create"}} {
		args := append(append([]string{}, path...), "--backend", "legacy", "--account", "Personal", "--mailbox", "INBOX", "--reply-to", "42", "--to", "recipient@example.com", "--subject", "Edited", "--body", "Edited complete body")
		code, stdout, stderr := run(t, args...)
		if code != clierr.ExitCode(clierr.CodeUsage) || !strings.Contains(stderr, "require the native backend") {
			t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
		}
	}
}

func TestThreadedOutgoingSelectionKeepsOriginalAndEditedFields(t *testing.T) {
	previousResolved, previousClient := resolved, mailClient
	t.Cleanup(func() { resolved, mailClient = previousResolved, previousClient })
	resolved = config.Resolved{Mailbox: config.Value{Value: "Projects/INBOX", Source: config.SourceFlag}}
	mailClient = mail.NewClient()
	if err := mailClient.SetBackend(mail.BackendNative); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []mail.ReplyMode{mail.ReplyToSender, mail.ReplyToAll, mail.ForwardMessage} {
		command := &cobra.Command{Use: "compose"}
		var flags threadedOutgoingFlags
		flags.register(command)
		command.Flags().String("mailbox", "", "original mailbox")
		command.Flags().String("body", "", "complete body")
		command.Flags().String("body-file", "", "complete body file")
		args := []string{"--mailbox", "Projects/INBOX", "--body", "Edited"}
		if mode == mail.ForwardMessage {
			args = append(args, "--forward-of", "42")
		} else {
			args = append(args, "--reply-to", "42")
		}
		if mode == mail.ReplyToAll {
			args = append(args, "--reply-all")
		}
		if err := command.ParseFlags(args); err != nil {
			t.Fatal(err)
		}
		selection, err := flags.selection(command)
		if err != nil {
			t.Fatal(err)
		}
		selection.Account = "Personal"
		outgoing := mail.OutgoingInput{Account: "Personal", Sender: "alias@example.com", Subject: "Edited subject", Body: "Edited complete body", To: []string{"edited@example.com"}, Cc: []string{"copy@example.com"}, Bcc: []string{"hidden@example.com"}, Attachments: []string{"/tmp/example.txt"}}
		input := selection.input(outgoing)
		if input.OriginalAccount != "Personal" || input.OriginalMailbox != "Projects/INBOX" || input.OriginalID != "42" || input.Mode != mode || !reflect.DeepEqual(input.OutgoingInput, outgoing) {
			t.Fatalf("identity or edited fields changed: %+v", input)
		}
		if notices := threadedPreviewNotices(selection, true); len(notices) != 2 || !strings.Contains(notices[1], "Original message lookup") {
			t.Fatalf("preview overstates validation: %v", notices)
		}
	}
}

func TestThreadedOutgoingFlagsOnlyOnSendAndDraftCreate(t *testing.T) {
	for _, command := range []*cobra.Command{draftsListCmd, draftsShowCmd, draftsUpdateCmd, draftsSendCmd, draftsDeleteCmd} {
		for _, flag := range []string{"reply-to", "reply-all", "forward-of"} {
			if command.Flags().Lookup(flag) != nil {
				t.Fatalf("%s unexpectedly accepts --%s", command.CommandPath(), flag)
			}
		}
	}
}
