package appleevents

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"time"
	"unicode/utf8"
)

var ErrUnavailable = errors.New("native Apple Events require macOS with cgo")

func validateCode(code string) error {
	if len(code) != 4 {
		return fmt.Errorf("Apple Event code %q must contain exactly four ASCII bytes", code)
	}
	for _, b := range []byte(code) {
		if b < 0x20 || b > 0x7e {
			return fmt.Errorf("Apple Event code %q must contain four printable ASCII bytes", code)
		}
	}
	return nil
}

func validateValue(v Value, depth int) error {
	if depth > 128 {
		return errors.New("Apple Event descriptor nesting exceeds 128")
	}
	if v.Code != "" {
		if err := validateCode(v.Code); err != nil {
			return err
		}
	}
	switch v.Kind {
	case "null", "int", "bool", "examined", "current-container":
	case "string":
		if v.Data != "" {
			if _, err := base64.StdEncoding.DecodeString(v.Data); err != nil {
				return errors.New("Apple Event text wrapper contains invalid flattened base64 data")
			}
		}
		if !utf8.ValidString(v.Text) {
			return errors.New("Apple Event text is not valid UTF-8")
		}
	case "real":
		if math.IsNaN(v.Real) || math.IsInf(v.Real, 0) {
			return errors.New("Apple Event real must be finite")
		}
	case "ordinal":
		if err := validateCode(v.Code); err != nil {
			return err
		}
		switch v.Code {
		case "all ", "firs", "last", "midd", "any ":
		default:
			return errors.New("Apple Event ordinal requires all, first, last, middle, or any")
		}
	case "enum", "type":
		return validateCode(v.Code)
	case "file":
		if v.Code != "" && v.Code != "furl" && v.Code != "alis" && v.Code != "fsrf" {
			return errors.New("Apple Event file code requires furl, alis, or fsrf")
		}
		if v.Data != "" {
			if _, err := base64.StdEncoding.DecodeString(v.Data); err != nil {
				return errors.New("Apple Event file wrapper contains invalid flattened base64 data")
			}
		}
		if !filepath.IsAbs(v.Text) || !utf8.ValidString(v.Text) {
			return errors.New("Apple Event file requires an absolute UTF-8 path")
		}
		for _, b := range []byte(v.Text) {
			if b == 0 {
				return errors.New("Apple Event file path contains NUL")
			}
		}
	case "date":
		if _, err := time.Parse(time.RFC3339Nano, v.Text); err != nil {
			return fmt.Errorf("Apple Event date requires RFC3339: %w", err)
		}
	case "list":
		for _, item := range v.Items {
			if err := validateValue(item, depth+1); err != nil {
				return err
			}
		}
	case "record", "object", "insertion", "comparison", "logical", "range":
		for key, item := range v.Fields {
			if err := validateCode(key); err != nil {
				return err
			}
			if err := validateValue(item, depth+1); err != nil {
				return err
			}
		}
		if v.Kind == "object" {
			for _, key := range []string{"want", "form", "seld", "from"} {
				if _, ok := v.Fields[key]; !ok {
					return fmt.Errorf("Apple Event object missing %s", key)
				}
			}
			if v.Fields["want"].Kind != "type" || v.Fields["form"].Kind != "enum" {
				return errors.New("Apple Event object requires type want and enum form")
			}
			if v.Fields["form"].Code == "rang" && v.Fields["seld"].Kind != "range" && v.Fields["seld"].Kind != "opaque" {
				return errors.New("Apple Event range selector requires a range descriptor")
			}
			if v.Fields["form"].Code == "test" {
				test := v.Fields["seld"]
				if test.Kind != "comparison" && test.Kind != "logical" && test.Kind != "opaque" {
					return errors.New("Apple Event test selector requires a comparison or logical descriptor")
				}
			}
		}
		if v.Kind == "range" {
			for _, key := range []string{"star", "stop"} {
				bound, ok := v.Fields[key]
				if !ok || (bound.Kind != "object" && bound.Kind != "opaque") {
					return fmt.Errorf("Apple Event range requires object %s", key)
				}
				if bound.Kind == "object" && bound.Fields["form"].Code == "indx" && bound.Fields["seld"].Kind == "int" && bound.Fields["seld"].Number < 1 {
					return errors.New("Apple Event range indexes must be positive")
				}
			}
			start, stop := v.Fields["star"], v.Fields["stop"]
			if start.Kind == "object" && stop.Kind == "object" && start.Fields["seld"].Kind == "int" && stop.Fields["seld"].Kind == "int" && start.Fields["seld"].Number > stop.Fields["seld"].Number {
				return errors.New("Apple Event range start must not exceed stop")
			}
		}
		if v.Kind == "comparison" {
			if v.Fields["relo"].Kind != "enum" {
				return errors.New("Apple Event comparison requires enum relo")
			}
			switch v.Fields["relo"].Code {
			case "=   ", "<   ", "<=  ", ">   ", ">=  ", "cont", "bgwt", "ends":
			default:
				return errors.New("Apple Event comparison requires a public comparison operator")
			}
			for _, key := range []string{"obj1", "obj2"} {
				if _, ok := v.Fields[key]; !ok {
					return fmt.Errorf("Apple Event comparison missing %s", key)
				}
			}
		}
		if v.Kind == "logical" {
			if v.Fields["logc"].Kind != "enum" {
				return errors.New("Apple Event logical test requires enum logc")
			}
			terms := v.Fields["term"]
			if terms.Kind != "list" || len(terms.Items) == 0 {
				return errors.New("Apple Event logical test requires a nonempty term list")
			}
			switch v.Fields["logc"].Code {
			case "AND ", "OR  ":
			case "NOT ":
				if len(terms.Items) != 1 {
					return errors.New("Apple Event NOT requires exactly one term")
				}
			default:
				return errors.New("Apple Event logical test requires AND, OR, or NOT operator")
			}
			for _, term := range terms.Items {
				if term.Kind != "comparison" && term.Kind != "logical" && term.Kind != "opaque" {
					return errors.New("Apple Event logical terms require comparison or logical descriptors")
				}
			}
		}
		if v.Kind == "insertion" {
			if _, ok := v.Fields["kobj"]; !ok {
				return errors.New("Apple Event insertion missing kobj")
			}
			if v.Fields["kpos"].Kind != "enum" {
				return errors.New("Apple Event insertion requires enum kpos")
			}
		}
	case "opaque":
		if v.Code != "" {
			if err := validateCode(v.Code); err != nil {
				return err
			}
		}
		data, err := base64.StdEncoding.DecodeString(v.Data)
		if err != nil || len(data) == 0 {
			return errors.New("Apple Event opaque data requires nonempty base64 flattened descriptor")
		}
	default:
		return fmt.Errorf("unsupported Apple Event descriptor kind %q", v.Kind)
	}
	return nil
}

func validateEvent(e Event) error {
	if err := validateCode(e.Class); err != nil {
		return err
	}
	if err := validateCode(e.ID); err != nil {
		return err
	}
	for key, value := range e.Params {
		if err := validateCode(key); err != nil {
			return err
		}
		if err := validateValue(value, 0); err != nil {
			return err
		}
	}
	return nil
}

// Apple Events measure their timeout in 1/60-second ticks. Round down so
// the requested native wait never exceeds the caller's remaining budget.
func timeoutTicks(ctx context.Context, requested time.Duration) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	budget := requested
	if budget <= 0 {
		budget = 30 * time.Second
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < budget {
			budget = remaining
		}
	}
	ticks := int64(budget/time.Second)*60 + int64((budget%time.Second)*60/time.Second)
	if ticks < 1 {
		return 0, context.DeadlineExceeded
	}
	if ticks > math.MaxInt32 {
		ticks = math.MaxInt32
	}
	return ticks, nil
}
