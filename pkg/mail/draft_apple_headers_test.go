package mail

import (
	"strings"
	"testing"
)

const applePlainDraftHeaders = "X-Apple-Mail-Signature: \r\nX-Apple-Base-Url: x-msg://42/\r\nX-Apple-Windows-Friendly: 1\r\nX-Apple-Mail-Remote-Attachments: YES\r\n"

func TestDraftAppleCompositionHeadersPreservePlainContent(t *testing.T) {
	for _, headers := range []string{
		"X-Apple-Mail-Signature: \r\n",
		"X-Apple-Mail-Signature: D0D51344-9BAF-44F0-93C2-682C2A6AE70C\r\n",
		"X-Apple-Base-Url: x-msg://42/\r\n",
		"X-Apple-Windows-Friendly: 1\r\n",
		"X-Apple-Windows-Friendly: 0\r\n",
		"X-Apple-Mail-Remote-Attachments: YES\r\n",
		"X-Apple-Mail-Remote-Attachments: NO\r\n",
		applePlainDraftHeaders,
	} {
		t.Run(strings.TrimSpace(headers), func(t *testing.T) {
			snapshot := simpleDraftSnapshot(t)
			snapshot.Source = headers + snapshot.Source
			body, attachments, err := reconstructibleDraft(snapshot)
			if err != nil || body != snapshot.Message.Content || len(attachments) != 0 {
				t.Fatalf("body=%q attachments=%v err=%v", body, attachments, err)
			}
		})
	}
}

func TestDraftAppleCompositionHeadersRejectUnknownValues(t *testing.T) {
	for _, header := range []string{
		"X-Apple-Mail-Signature: unknown\r\n",
		"X-Apple-Mail-Signature: <html>signature</html>\r\n",
		"X-Apple-Base-Url: https://example.test/\r\n",
		"X-Apple-Base-Url: x-msg://42/relative\r\n",
		"X-Apple-Base-Url: x-msg://../\r\n",
		"X-Apple-Windows-Friendly: yes\r\n",
		"X-Apple-Mail-Remote-Attachments: MAYBE\r\n",
		"X-Apple-Unknown: YES\r\n",
		"X-Apple-Mail-Signature: \r\nX-Apple-Mail-Signature: \r\n",
		"X-Apple-Base-Url: x-msg://42/\r\nX-Apple-Base-Url: x-msg://42/\r\n",
		"X-Apple-Windows-Friendly: 1\r\nX-Apple-Windows-Friendly: 1\r\n",
		"X-Apple-Mail-Remote-Attachments: YES\r\nX-Apple-Mail-Remote-Attachments: YES\r\n",
	} {
		t.Run(strings.TrimSpace(header), func(t *testing.T) {
			snapshot := simpleDraftSnapshot(t)
			snapshot.Source = header + snapshot.Source
			if _, _, err := reconstructibleDraft(snapshot); err == nil {
				t.Fatal("accepted unknown or duplicate composition metadata")
			}
		})
	}
}

func TestDraftAppleCompositionHeadersDoNotBypassContentChecks(t *testing.T) {
	for _, header := range []string{"In-Reply-To: <thread@example.test>\r\n", "References: <thread@example.test>\r\n", "Content-ID: <inline>\r\n", "DKIM-Signature: signature\r\n"} {
		snapshot := simpleDraftSnapshot(t)
		snapshot.Source = applePlainDraftHeaders + header + snapshot.Source
		if _, _, err := reconstructibleDraft(snapshot); err == nil {
			t.Fatalf("accepted semantic header %q", header)
		}
	}
	for _, kind := range []string{"text/html", "multipart/alternative; boundary=x", "multipart/related; boundary=x", "multipart/signed; boundary=x", "application/pkcs7-mime"} {
		snapshot := simpleDraftSnapshot(t)
		snapshot.Source = applePlainDraftHeaders + strings.Replace(snapshot.Source, "text/plain; charset=utf-8", kind, 1)
		if _, _, err := reconstructibleDraft(snapshot); err == nil {
			t.Fatalf("accepted unsafe MIME %q", kind)
		}
	}
	snapshot := simpleDraftSnapshot(t)
	snapshot.Source = applePlainDraftHeaders + snapshot.Source
	snapshot.Message.Content = "different body"
	if _, _, err := reconstructibleDraft(snapshot); err == nil {
		t.Fatal("accepted mismatched body with composition metadata")
	}
}

func TestDraftAppleCompositionHeadersRequireSelfContainedAttachments(t *testing.T) {
	for _, tc := range []struct {
		name, part string
		wantOK     bool
	}{
		{"file", "Content-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=sample.bin\r\nContent-Transfer-Encoding: base64\r\n\r\nAAECAw==", true},
		{"inline", "Content-Type: image/png\r\nContent-Disposition: inline; filename=sample.png\r\nContent-ID: <image>\r\n\r\nimage", false},
		{"remote", "Content-Type: message/external-body; access-type=URL; URL=\"https://example.test/file\"\r\nContent-Disposition: attachment; filename=sample.bin\r\n\r\n", false},
		{"signed", "Content-Type: application/pkcs7-mime\r\nContent-Disposition: attachment; filename=smime.p7m\r\n\r\nsignature", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := simpleDraftSnapshot(t)
			snapshot.Message.Content = "body"
			snapshot.Source = applePlainDraftHeaders + "From: alias@example.test\r\nTo: to@example.test\r\nCc: cc@example.test\r\nSubject: Draft\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nbody\r\n--x\r\n" + tc.part + "\r\n--x--\r\n"
			body, attachments, err := reconstructibleDraft(snapshot)
			if tc.wantOK {
				if err != nil || body != "body" || len(attachments) != 1 || string(attachments[0].data) != "\x00\x01\x02\x03" {
					t.Fatalf("body=%q attachments=%v err=%v", body, attachments, err)
				}
			} else if err == nil {
				t.Fatal("accepted unsupported attachment despite compose metadata")
			}
		})
	}
}
