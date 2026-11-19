package wireproxy

import "testing"

func TestParseUploadLimit(t *testing.T) {
	ok := map[string]UploadLimitConfig{
		"":        {Mode: "off"},
		"auto":    {Mode: "auto"},
		"AUTO:12": {Mode: "auto", Start: 12e6},
		"off":     {Mode: "off"},
		"2.5":     {Mode: "fixed", Start: 2.5e6},
	}
	for in, want := range ok {
		got, err := ParseUploadLimit(in)
		if err != nil || got != want {
			t.Errorf("%q: got %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"0", "-1", "fast", "auto:", "auto:x", "inf"} {
		if _, err := ParseUploadLimit(in); err == nil {
			t.Errorf("%q: want an error", in)
		}
	}
}
