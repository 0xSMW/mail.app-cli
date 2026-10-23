package mail

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestMessageMarshalJSONLinkInventory(t *testing.T) {
	link := MessageLink{URL: "https://example.test/"}
	for _, tt := range []struct {
		name    string
		message Message
		want    string
	}{
		{"complete nil links", Message{LinkStatus: &LinkExtractionStatus{Complete: true}}, "[]"},
		{"complete empty links", Message{LinkStatus: &LinkExtractionStatus{Complete: true}, Links: []MessageLink{}}, "[]"},
		{"incomplete nil links", Message{LinkStatus: &LinkExtractionStatus{}}, "[]"},
		{"incomplete empty links", Message{LinkStatus: &LinkExtractionStatus{}, Links: []MessageLink{}}, "[]"},
		{"populated links", Message{LinkStatus: &LinkExtractionStatus{Complete: true}, Links: []MessageLink{link}}, "populated"},
		{"uninspected metadata", Message{}, ""},
		{"uninspected empty links", Message{Links: []MessageLink{}}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := json.Marshal(tt.message)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			want := tt.want
			if want == "populated" {
				data, err := json.Marshal([]MessageLink{link})
				if err != nil {
					t.Fatal(err)
				}
				want = string(data)
			}
			if got := string(fields["links"]); got != want {
				t.Errorf("links = %q, want %q", got, want)
			}
			for _, field := range []string{"toRecipients", "ccRecipients", "bccRecipients"} {
				if got := string(fields[field]); got != "[]" {
					t.Errorf("%s = %q, want []", field, got)
				}
			}
			var decoded Message
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decoded.LinkStatus, tt.message.LinkStatus) {
				t.Errorf("link status changed: got %+v, want %+v", decoded.LinkStatus, tt.message.LinkStatus)
			}
			if len(tt.message.Links) > 0 && !reflect.DeepEqual(decoded.Links, tt.message.Links) {
				t.Errorf("links changed: got %+v, want %+v", decoded.Links, tt.message.Links)
			}
		})
	}
}
