package appleevents

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestInvalidDescriptorValues(t *testing.T) {
	values := []Value{
		{Kind: "unknown"}, {Kind: "insertion", Fields: map[string]Value{}},
		Insertion(Null(), "bad"),
		{Kind: "insertion", Fields: map[string]Value{"kobj": Null(), "kpos": String("afte")}}, Enum("bad"), TypeCode("tooolong"), Enum("a\x00bc"), Enum("éab"),
		Record(map[string]Value{"bad": Int(0)}), List(Enum("bad")), File("relative"),
		File("/tmp/nul\x00path"), {Kind: "file", Text: "/tmp/file", Code: "bad "}, {Kind: "file", Text: "/tmp/file", Data: "%%%"}, {Kind: "string", Text: string([]byte{0xff})},
		{Kind: "date", Text: "yesterday"}, {Kind: "real", Real: math.Inf(1)},
		{Kind: "opaque", Data: "%%%"}, {Kind: "object", Fields: map[string]Value{}},
		{Kind: "object", Fields: map[string]Value{"want": String("prop"), "form": Enum("prop"), "seld": TypeCode("vers"), "from": Null()}},
	}
	for i, value := range values {
		if _, err := RoundTrip(value); err == nil {
			t.Errorf("value %d: expected validation error", i)
		}
	}
}

func TestInvalidEventDoesNotDispatch(t *testing.T) {
	events := []Event{
		{Class: "bad", ID: "getd"}, {Class: "core", ID: "bad"},
		{Class: "core", ID: "getd", Params: map[string]Value{"bad": Null()}},
		{Class: "core", ID: "getd", Params: map[string]Value{"----": Enum("bad")}},
	}
	for _, event := range events {
		_, stats, err := Send(context.Background(), event)
		if err == nil || stats.Attempted || stats.Events != 0 {
			t.Fatalf("invalid event dispatched: stats=%+v err=%v", stats, err)
		}
	}
}

func TestTimeoutBudgets(t *testing.T) {
	ticks, err := timeoutTicks(context.Background(), 0)
	if err != nil || ticks != 1800 {
		t.Fatalf("default ticks=%d err=%v", ticks, err)
	}
	ticks, err = timeoutTicks(context.Background(), 2*time.Second)
	if err != nil || ticks != 120 {
		t.Fatalf("requested ticks=%d err=%v", ticks, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	ticks, err = timeoutTicks(ctx, 30*time.Second)
	if err != nil || ticks < 1 || ticks > 15 {
		t.Fatalf("deadline ticks=%d err=%v", ticks, err)
	}
	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if _, err := timeoutTicks(canceled, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
	if _, err := timeoutTicks(context.Background(), time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("subtick: %v", err)
	}
}

func TestCanceledEventDoesNotDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, stats, err := Send(ctx, Event{Class: "core", ID: "getd"})
	if !errors.Is(err, context.Canceled) || stats.Attempted || stats.Events != 0 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}

func TestErrorUnwrap(t *testing.T) {
	err := &Error{Code: -1712, Message: "timed out", Attempted: true, Cause: context.DeadlineExceeded}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("timeout lost context classification")
	}
	var native *Error
	if !errors.As(err, &native) || native.Code != -1712 || !native.Attempted {
		t.Fatal("timeout lost native metadata")
	}
}

func TestComparisonAndLogicalValidation(t *testing.T) {
	for _, operator := range []string{"=   ", "<   ", "<=  ", ">   ", ">=  ", "cont", "bgwt", "ends"} {
		if err := validateValue(Comparison(operator, Property("subj", Examined()), String("test")), 0); err != nil {
			t.Errorf("public comparison %q: %v", operator, err)
		}
	}
	comparison := Comparison("=   ", Int(0), Int(0))
	invalid := []Value{
		ByTest("mssg", String("invalid"), Null()),
		Comparison("bad", Int(0), Int(0)), Comparison("nope", Int(0), Int(0)),
		{Kind: "comparison", Fields: map[string]Value{"relo": Enum("=   "), "obj1": Int(0)}},
		{Kind: "comparison", Fields: map[string]Value{"relo": String("=   "), "obj1": Int(0), "obj2": Int(0)}},
		Logical("AND "), Logical("OR  ", String("invalid")), Logical("XOR ", comparison),
		Logical("NOT ", comparison, comparison),
		{Kind: "logical", Fields: map[string]Value{"logc": Enum("AND "), "term": comparison}},
		{Kind: "logical", Fields: map[string]Value{"logc": String("AND "), "term": List(comparison)}},
	}
	for _, value := range invalid {
		if err := validateValue(value, 0); err == nil {
			t.Errorf("invalid test descriptor accepted: %#v", value)
		}
	}
	selected := ByTest("mssg", comparison, Null())
	if selected.Fields["form"].Code != "test" || !reflect.DeepEqual(selected.Fields["seld"], comparison) {
		t.Fatal("ByTest did not preserve test selector")
	}
}

func TestAllUsesAbsoluteOrdinal(t *testing.T) {
	all := All("mact", Null())
	if all.Fields["form"].Code != "indx" || !reflect.DeepEqual(all.Fields["seld"], Ordinal("all ")) {
		t.Fatal("All must use absolute ordinal, not an enumeration")
	}
	for _, invalid := range []Value{Ordinal("bad"), Ordinal("nope")} {
		if err := validateValue(invalid, 0); err == nil {
			t.Fatalf("invalid ordinal accepted: %#v", invalid)
		}
	}
}

func TestRangeValidation(t *testing.T) {
	valid := Range("mssg", 1, 2, Null())
	if err := validateValue(valid, 0); err != nil {
		t.Fatal(err)
	}
	if valid.Fields["form"].Code != "rang" || valid.Fields["seld"].Kind != "range" {
		t.Fatal("unexpected range selector constructor")
	}
	for _, bound := range []string{"star", "stop"} {
		if !reflect.DeepEqual(valid.Fields["seld"].Fields[bound].Fields["from"], CurrentContainer()) {
			t.Fatal("range bounds must resolve against current container")
		}
	}

	for _, value := range []Value{
		Range("mssg", 0, 2, Null()), Range("mssg", 2, 1, Null()),
		{Kind: "range", Fields: map[string]Value{"star": Index("mssg", 1, Null())}},
		{Kind: "range", Fields: map[string]Value{"star": String("invalid"), "stop": Index("mssg", 2, Null())}},
		Object("mssg", "rang", String("invalid"), Null()),
	} {
		if err := validateValue(value, 0); err == nil {
			t.Fatalf("invalid range accepted: %#v", value)
		}
	}
}
