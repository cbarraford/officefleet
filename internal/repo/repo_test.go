package repo

import "testing"

func TestUnmarshalJSONB(t *testing.T) {
	// Empty column (SQL NULL) is a no-op, not an error.
	var m map[string]any
	if err := unmarshalJSONB(nil, &m); err != nil {
		t.Errorf("nil column should be a no-op, got %v", err)
	}
	if err := unmarshalJSONB([]byte{}, &m); err != nil {
		t.Errorf("empty column should be a no-op, got %v", err)
	}

	// Valid JSON decodes.
	if err := unmarshalJSONB([]byte(`{"k":"v"}`), &m); err != nil {
		t.Fatalf("valid JSON should decode, got %v", err)
	}
	if m["k"] != "v" {
		t.Errorf("decoded value = %v, want v", m["k"])
	}

	// A non-empty malformed blob is a real error, not silently dropped (issue #12).
	if err := unmarshalJSONB([]byte(`{not json`), &m); err == nil {
		t.Error("malformed JSON must return an error")
	}

	// JSON null into a pointer leaves it nil (the backend-absent case).
	type ref struct{ Name string }
	var p *ref
	if err := unmarshalJSONB([]byte(`null`), &p); err != nil {
		t.Errorf("null should decode without error, got %v", err)
	}
	if p != nil {
		t.Errorf("null must leave the pointer nil, got %+v", p)
	}
}
