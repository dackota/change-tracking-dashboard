package filter_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/filter"
)

// TestClassSet_Allows covers the whole predicate contract in one table: an
// empty set is a no-op (allows everything, including values outside any
// vocabulary), a populated set allows exactly its members, and the predicate
// is total — it answers for any string, including "" and values it has never
// seen, rather than panicking.
func TestClassSet_Allows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		set   filter.ClassSet
		value string
		want  bool
	}{
		{"empty set allows a known value", filter.NewClassSet(), "major", true},
		{"empty set allows an unknown value", filter.NewClassSet(), "not-a-tier", true},
		{"empty set allows the empty value", filter.NewClassSet(), "", true},
		{"single member allows itself", filter.NewClassSet("major"), "major", true},
		{"single member rejects a non-member", filter.NewClassSet("major"), "minor", false},
		{"single member rejects the empty value", filter.NewClassSet("major"), "", false},
		{"multiple members allow each member", filter.NewClassSet("major", "downgrade"), "downgrade", true},
		{"multiple members reject a non-member", filter.NewClassSet("major", "downgrade"), "patch", false},
		{"duplicate members collapse", filter.NewClassSet("patch", "patch"), "patch", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.set.Allows(tt.value); got != tt.want {
				t.Errorf("Allows(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

// TestParseClassSet verifies parsing request-style repeated values against a
// closed vocabulary: recognized values OR together into an allow-set, no
// values yields the no-op set, and an unrecognized value is rejected rather
// than silently dropped. The rejection is what stops `impact=majr` from
// quietly returning an unfiltered feed that a consumer reads as "everything
// is major".
func TestParseClassSet(t *testing.T) {
	t.Parallel()

	vocab := map[string]struct{}{"major": {}, "minor": {}, "patch": {}}

	tests := []struct {
		name    string
		values  []string
		want    []string
		wantErr bool
	}{
		{name: "no values yields the no-op set", values: nil, want: []string{}},
		{name: "empty slice yields the no-op set", values: []string{}, want: []string{}},
		{name: "single known value", values: []string{"major"}, want: []string{"major"}},
		{name: "repeated values OR together", values: []string{"major", "patch"}, want: []string{"major", "patch"}},
		{name: "duplicates collapse", values: []string{"minor", "minor"}, want: []string{"minor"}},
		{name: "unknown value is rejected", values: []string{"majr"}, wantErr: true},
		{name: "one unknown among known is rejected", values: []string{"major", "majr"}, wantErr: true},
		{name: "empty value is rejected", values: []string{""}, wantErr: true},
		{name: "case mismatch is rejected", values: []string{"Major"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := filter.ParseClassSet(tt.values, vocab)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseClassSet(%v) = %v, nil; want an error", tt.values, got.Values())
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseClassSet(%v): unexpected error: %v", tt.values, err)
			}
			if !reflect.DeepEqual(got.Values(), tt.want) {
				t.Errorf("ParseClassSet(%v).Values() = %v, want %v", tt.values, got.Values(), tt.want)
			}
		})
	}
}

// TestParseClassSet_ErrorEchoesNoCallerInput verifies the rejection error
// never contains the offending value. The web layer logs this error, and an
// error carrying raw caller input is a log-injection vector; the endpoint's
// 400 body is generic for the same reason.
func TestParseClassSet_ErrorEchoesNoCallerInput(t *testing.T) {
	t.Parallel()

	const hostile = "majr<script>alert(1)</script>"

	_, err := filter.ParseClassSet([]string{hostile}, map[string]struct{}{"major": {}})
	if err == nil {
		t.Fatal("ParseClassSet: got nil error for an unknown value, want an error")
	}
	if strings.Contains(err.Error(), hostile) || strings.Contains(err.Error(), "majr") {
		t.Errorf("error echoes caller input: %q", err.Error())
	}
}

// TestClassSet_ZeroValueAllowsEverything verifies the zero ClassSet behaves
// exactly like NewClassSet() — a no-op predicate. Callers that never set a
// class filter get the zero value, so it must never reject.
func TestClassSet_ZeroValueAllowsEverything(t *testing.T) {
	t.Parallel()

	var s filter.ClassSet
	for _, v := range []string{"", "major", "anything"} {
		if !s.Allows(v) {
			t.Errorf("zero ClassSet.Allows(%q) = false, want true", v)
		}
	}
}
