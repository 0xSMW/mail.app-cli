//go:build darwin && cgo

package appleevents

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestNativeDescriptorRoundTrip(t *testing.T) {
	if !Available() {
		t.Fatal("native bridge unavailable")
	}
	values := []Value{
		Null(), Bool(false), Bool(true), Int(0), Int(-9223372036854775808), Int(9223372036854775807),
		String("Hello café — ไทย 日本語 🚀\x00end"), {Kind: "real", Real: 0.125},
		Enum("all "), TypeCode("prop"), File("/tmp/café file.txt"), File("/tmp/literal %20#? space café ไทย.txt"),
		Date(time.Date(2026, 10, 13, 5, 6, 7, 0, time.FixedZone("offset", 7*60*60))),
		List(String("first"), Bool(false), Int(0), List(String("nested"))),
		Record(map[string]Value{"abcd": String("ไทย"), "zero": Int(0), "bool": Bool(false), "list": List(Int(2))}),
	}
	for _, value := range values {
		got, err := RoundTrip(value)
		if err != nil {
			t.Fatalf("%s: %v", value.Kind, err)
		}
		if !reflect.DeepEqual(got, value) {
			t.Errorf("%s: got %#v want %#v", value.Kind, got, value)
		}
	}
}

func TestObjectReferencesRemainOpaque(t *testing.T) {
	object := Property("vers", Null())
	got, err := RoundTrip(object)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "opaque" || got.Code != "obj " {
		t.Fatalf("object decoded as %#v", got)
	}
	if data, err := base64.StdEncoding.DecodeString(got.Data); err != nil || len(data) == 0 {
		t.Fatalf("opaque bytes missing: %v", err)
	}
	again, err := RoundTrip(got)
	if err != nil || !reflect.DeepEqual(again, got) {
		t.Fatalf("opaque descriptor was not preserved: %#v %v", again, err)
	}
}

func TestMalformedFlattenedDescriptors(t *testing.T) {
	for _, bytes := range [][]byte{{0}, []byte("not an Apple Event"), make([]byte, 64)} {
		_, err := RoundTrip(Value{Kind: "opaque", Data: base64.StdEncoding.EncodeToString(bytes)})
		if err == nil {
			t.Fatal("malformed flattened descriptor accepted")
		}
	}
}

// Explicitly opt in to the single read-only Mail application version request.
// Normal unit tests do not dispatch Apple Events or read any Mail data.
func TestReadOnlyMailVersion(t *testing.T) {
	if os.Getenv("MAIL_AE_VERSION_PROBE") != "1" {
		t.Skip("read-only integration probe disabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	version, stats, err := Send(ctx, Event{Class: "core", ID: "getd", Params: map[string]Value{"----": Property("vers", Null())}})
	if err != nil {
		t.Fatalf("version probe failed: stats=%+v err=%v", stats, err)
	}
	if version.Kind != "string" || version.Text == "" || !stats.Attempted || stats.Events != 1 {
		t.Fatalf("unexpected application-version result: kind=%s stats=%+v", version.Kind, stats)
	}
	t.Logf("Mail version %s; native events=%d", version.Text, stats.Events)
}

func TestUnknownDescriptorsAreLossless(t *testing.T) {
	// AEFlattenDesc fixture: type 'TEST' with binary bytes ff 00 01.
	original := Value{Kind: "opaque", Code: "TEST", Data: "ZGxlMgAAAABURVNUAAAAA/8AAQ=="}
	got, err := RoundTrip(original)
	if err != nil || !reflect.DeepEqual(got, original) {
		t.Fatalf("unknown native type changed: %#v %v", got, err)
	}
}

func TestNativeRichTextWrapper(t *testing.T) {
	// AEFlattenDesc fixture: typeAEText ('tTXT') record whose keyAEText
	// contains Unicode text. Its native bytes must survive text extraction.
	original := Value{Kind: "opaque", Code: "tTXT", Data: "ZGxlMgAAAAB0VFhUAAAAKgAAAAEAAAAAa3R4dHV0eHQAAAAWAGMAYQBmAOkAIA5EDhcOIgAg2D3egA=="}
	got, err := RoundTrip(original)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "string" || got.Text != "café ไทย 🚀" || got.Code != "tTXT" || got.Data != original.Data {
		t.Fatalf("rich text wrapper changed: %#v", got)
	}
	again, err := RoundTrip(got)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("rich text roundtrip lost wrapper: %#v %v", again, err)
	}
}

func TestNativeSubtickDoesNotDispatch(t *testing.T) {
	_, stats, err := Send(context.Background(), Event{Class: "core", ID: "getd", Timeout: time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) || stats.Attempted || stats.Events != 0 {
		t.Fatalf("subtick stats=%+v err=%v", stats, err)
	}
}

func TestInsertionLocationRoundTrip(t *testing.T) {
	location := Insertion(Index("mesg", 1, Null()), "afte")
	if location.Kind != "insertion" || !reflect.DeepEqual(location.Fields["kpos"], Enum("afte")) {
		t.Fatal("unexpected insertion constructor")
	}
	got, err := RoundTrip(location)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "opaque" || got.Code != "insl" || got.Data == "" {
		t.Fatalf("insertion descriptor not preserved: %#v", got)
	}
	again, err := RoundTrip(got)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("insertion descriptor changed: %#v %v", again, err)
	}
}

func TestReplyErrorsPreserveDispatchOwnership(t *testing.T) {
	bridgeErrors := []error{
		errors.New("native Apple Event bridge could not allocate its response"),
		&json.SyntaxError{Offset: 3},
	}
	for _, cause := range bridgeErrors {
		err := replyError(nativeReply{Attempted: true}, cause, nil)
		var native *Error
		if !errors.As(err, &native) || !native.Attempted || native.Cause != cause || !errors.Is(err, cause) {
			t.Fatalf("post-dispatch bridge error lost ownership or cause: %#v", err)
		}
		if got := replyError(nativeReply{}, cause, nil); got != cause {
			t.Fatalf("pre-dispatch bridge error changed: %#v", got)
		}
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		err := replyError(nativeReply{Attempted: true, Value: String("reply")}, nil, cause)
		var native *Error
		if !errors.As(err, &native) || !native.Attempted || native.Cause != cause || !errors.Is(err, cause) {
			t.Fatalf("post-dispatch context error lost ownership or cause: %#v", err)
		}
		if got := replyError(nativeReply{}, nil, cause); got != cause {
			t.Fatalf("pre-dispatch context error changed: %#v", got)
		}
	}
	err := replyError(nativeReply{Failed: true, Attempted: true, Code: -1712, Message: "native timeout"}, nil, nil)
	var native *Error
	if !errors.As(err, &native) || !native.Attempted || native.Code != -1712 || native.Message != "native timeout" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("native timeout lost metadata: %#v", err)
	}
	if err := replyError(nativeReply{Attempted: true, Value: String("reply")}, nil, nil); err != nil {
		t.Fatalf("successful reply: %v", err)
	}
}

func TestNativeTestSelectorDescriptors(t *testing.T) {
	subject := Comparison("cont", Property("subj", Examined()), String("example"))
	unread := Comparison("=   ", Property("isrd", Examined()), Bool(false))
	values := []struct {
		value Value
		code  string
	}{
		{Examined(), "exmn"}, {CurrentContainer(), "ccnt"}, {subject, "cmpd"}, {unread, "cmpd"},
		{Logical("AND ", subject, unread), "logi"},
		{Logical("OR  ", subject, unread), "logi"},
		{Logical("NOT ", unread), "logi"},
		{ByTest("mssg", Logical("AND ", subject, unread), ByName("mbxp", "Inbox", Null())), "obj "},
	}
	for _, tc := range values {
		got, err := RoundTrip(tc.value)
		if err != nil {
			t.Fatalf("%s descriptor: %v", tc.code, err)
		}
		if got.Kind != "opaque" || got.Code != tc.code || got.Data == "" {
			t.Fatalf("%s descriptor changed: %#v", tc.code, got)
		}
		again, err := RoundTrip(got)
		if err != nil || !reflect.DeepEqual(again, got) {
			t.Fatalf("%s descriptor bytes changed: %#v %v", tc.code, again, err)
		}
	}
}

func TestAbsoluteOrdinalDescriptor(t *testing.T) {
	for _, code := range []string{"all ", "firs", "last", "midd", "any "} {
		got, err := RoundTrip(Ordinal(code))
		if err != nil {
			t.Fatalf("ordinal %q: %v", code, err)
		}
		if got.Kind != "opaque" || got.Code != "abso" {
			t.Fatalf("ordinal descriptor has wrong native type: %#v", got)
		}
		again, err := RoundTrip(got)
		if err != nil || !reflect.DeepEqual(again, got) {
			t.Fatalf("ordinal bytes changed: %#v %v", again, err)
		}
	}
}

func TestNativeAliasAndFSRefFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reference-fixture.txt")
	if err := os.WriteFile(path, []byte("native descriptor test fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"alis", "fsrf"} {
		file := File(path)
		file.Code = code
		got, err := RoundTrip(file)
		if err != nil {
			t.Fatalf("%s file reference: %v", code, err)
		}
		// /var and /private/var refer to the same temp directory on macOS.
		normalized, err := filepath.EvalSymlinks(got.Text)
		if err != nil {
			t.Fatalf("%s resolved file path: %v", code, err)
		}
		expected, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		if got.Kind != "file" || got.Code != code || got.Data == "" || normalized != expected {
			t.Fatalf("%s file reference lost its type, data, or path", code)
		}
		again, err := RoundTrip(got)
		if err != nil || !reflect.DeepEqual(again, got) {
			t.Fatalf("%s file reference changed on reuse", code)
		}
	}
}

func TestUnresolvableFileReferencesRemainOpaque(t *testing.T) {
	fixture, err := base64.StdEncoding.DecodeString("ZGxlMgAAAABURVNUAAAAA/8AAQ==")
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"alis", "fsrf"} {
		copy(fixture[8:12], code)
		original := Value{Kind: "opaque", Code: code, Data: base64.StdEncoding.EncodeToString(fixture)}
		got, err := RoundTrip(original)
		if err != nil || !reflect.DeepEqual(got, original) {
			t.Fatalf("unresolvable %s descriptor was not preserved", code)
		}
	}
}

func TestNativeRangeDescriptor(t *testing.T) {
	rangeRef := Range("mssg", 1, 2, ByName("mbxp", "INBOX", ByName("mact", "fixture", Null())))
	for _, tc := range []struct {
		value Value
		code  string
	}{{rangeRef, "obj "}, {rangeRef.Fields["seld"], "rang"}} {
		got, err := RoundTrip(tc.value)
		if err != nil {
			t.Fatalf("range descriptor: %v", err)
		}
		if got.Kind != "opaque" || got.Code != tc.code || got.Data == "" {
			t.Fatalf("range native type changed: kind=%s code=%s", got.Kind, got.Code)
		}
		again, err := RoundTrip(got)
		if err != nil || !reflect.DeepEqual(got, again) {
			t.Fatal("range native bytes changed on reuse")
		}
	}
}

func TestNativeBatchDateRoundTrip(t *testing.T) {
	dates := make([]Value, 128)
	epoch := time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range dates {
		dates[i] = Date(epoch.Add(time.Duration(i) * 31 * time.Hour))
	}
	input := List(dates...)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			got, err := RoundTrip(input)
			if err == nil && !reflect.DeepEqual(got, input) {
				err = errors.New("batch dates changed their UTC formatting")
			}
			errs <- err
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}
