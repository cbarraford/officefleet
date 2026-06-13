package repo

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func marshalJSONField(field string, value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", field, err)
	}
	return data, nil
}

func unmarshalJSONField(field string, data []byte, dest any) error {
	if isJSONNull(data) {
		return nil
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("decode %s: %w", field, err)
	}
	return nil
}

func isJSONNull(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}
