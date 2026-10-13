// Package appleevents sends typed Apple Events without compiling or executing scripts.
package appleevents

import (
	"context"
	"fmt"
	"time"
)

// Value is a portable description of an Apple Event descriptor. Opaque replies
// retain a flattened descriptor so returned object references can be reused.
type Value struct {
	Kind    string           `json:"kind"`
	Code    string           `json:"code,omitempty"`
	Text    string           `json:"text,omitempty"`
	Number  int64            `json:"number,omitempty"`
	Real    float64          `json:"real,omitempty"`
	Boolean bool             `json:"boolean,omitempty"`
	Items   []Value          `json:"items,omitempty"`
	Fields  map[string]Value `json:"fields,omitempty"`
	Data    string           `json:"data,omitempty"`
}

type Event struct {
	Class   string           `json:"class"`
	ID      string           `json:"id"`
	Params  map[string]Value `json:"params,omitempty"`
	Timeout time.Duration    `json:"-"`
}

type Stats struct {
	Events    int64   `json:"events"`
	ElapsedMS float64 `json:"elapsedMs"`
	Attempted bool    `json:"attempted"`
}

type Error struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	Attempted bool   `json:"attempted"`
	Cause     error  `json:"-"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("Mail Apple Event error %d: %s", e.Code, e.Message)
}
func (e *Error) Unwrap() error { return e.Cause }

func Null() Value                          { return Value{Kind: "null"} }
func String(s string) Value                { return Value{Kind: "string", Text: s} }
func Int(n int64) Value                    { return Value{Kind: "int", Number: n} }
func Bool(b bool) Value                    { return Value{Kind: "bool", Boolean: b} }
func Enum(code string) Value               { return Value{Kind: "enum", Code: code} }
func TypeCode(code string) Value           { return Value{Kind: "type", Code: code} }
func File(path string) Value               { return Value{Kind: "file", Text: path} }
func Date(t time.Time) Value               { return Value{Kind: "date", Text: t.UTC().Format(time.RFC3339Nano)} }
func List(items ...Value) Value            { return Value{Kind: "list", Items: items} }
func Record(fields map[string]Value) Value { return Value{Kind: "record", Fields: fields} }
func Object(class, form string, key, container Value) Value {
	return Value{Kind: "object", Fields: map[string]Value{"want": TypeCode(class), "form": Enum(form), "seld": key, "from": container}}
}
func Property(code string, container Value) Value {
	return Object("prop", "prop", TypeCode(code), container)
}
func ByID(class string, id Value, container Value) Value { return Object(class, "ID  ", id, container) }
func ByName(class, name string, container Value) Value {
	return Object(class, "name", String(name), container)
}
func Index(class string, index int, container Value) Value {
	return Object(class, "indx", Int(int64(index)), container)
}
func All(class string, container Value) Value {
	return Object(class, "indx", Ordinal("all "), container)
}

func (v Value) StringValue() string { return v.Text }
func (v Value) IntValue() int64     { return v.Number }
func (v Value) BoolValue() bool     { return v.Boolean }

// Sender allows operation tests to supply a descriptor-level fake transport.
type Sender func(context.Context, Event) (Value, Stats, error)

// Insertion describes a native insertion location relative to an object.
func Insertion(object Value, position string) Value {
	return Value{Kind: "insertion", Fields: map[string]Value{"kobj": object, "kpos": Enum(position)}}
}

// Comparison describes a public Apple Event comparison record (cmpd).
func Comparison(operator string, left, right Value) Value {
	return Value{Kind: "comparison", Fields: map[string]Value{"relo": Enum(operator), "obj1": left, "obj2": right}}
}

// Logical combines comparison tests in a public logical record (logi).
func Logical(operator string, terms ...Value) Value {
	return Value{Kind: "logical", Fields: map[string]Value{"logc": Enum(operator), "term": List(terms...)}}
}

// Examined refers to the object currently being evaluated by a test selector.
func Examined() Value { return Value{Kind: "examined"} }

// ByTest selects objects whose comparison or logical test evaluates to true.
func ByTest(class string, test, container Value) Value {
	return Object(class, "test", test, container)
}

// Ordinal describes an absolute collection position such as all or first.
func Ordinal(code string) Value { return Value{Kind: "ordinal", Code: code} }

// Range selects a contiguous inclusive range of one-based collection indexes.
// Its endpoints resolve against the current range container, as Mail requires.
func Range(class string, start, end int, container Value) Value {
	return Object(class, "rang", Value{Kind: "range", Fields: map[string]Value{"star": Index(class, start, CurrentContainer()), "stop": Index(class, end, CurrentContainer())}}, container)
}

// CurrentContainer refers to the collection currently resolving a relative selector.
func CurrentContainer() Value { return Value{Kind: "current-container"} }
