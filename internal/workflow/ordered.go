package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// OrderedMap is a JSON object that remembers the key order from the source file.
//
// Go maps iterate in randomized order, but two fields are order-sensitive all the
// way to the UI: `variants` becomes the image-mode dropdown, and `variantLabels`
// is rendered as a joined string. Decoding them into a plain map would make the
// canvas menu shuffle between requests, so ordering is preserved explicitly.
type OrderedMap[T any] struct {
	Keys   []string
	Values map[string]T
}

// UnmarshalJSON decodes the object while recording key order.
func (m *OrderedMap[T]) UnmarshalJSON(data []byte) error {
	m.Values = map[string]T{}
	m.Keys = nil

	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("expected a JSON object")
	}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("expected a JSON object key")
		}
		var value T
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		m.Keys = append(m.Keys, key)
		m.Values[key] = value
	}
	// Consume the closing brace.
	if _, err := decoder.Token(); err != nil {
		return err
	}
	return nil
}

// MarshalJSON writes the object back in the recorded key order.
func (m OrderedMap[T]) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for i, key := range m.Keys {
		if i > 0 {
			buffer.WriteByte(',')
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		buffer.Write(encodedKey)
		buffer.WriteByte(':')
		encodedValue, err := json.Marshal(m.Values[key])
		if err != nil {
			return nil, err
		}
		buffer.Write(encodedValue)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

// Get returns the value for key.
func (m OrderedMap[T]) Get(key string) (T, bool) {
	if m.Values == nil {
		var zero T
		return zero, false
	}
	value, ok := m.Values[key]
	return value, ok
}

// Len returns the number of entries.
func (m OrderedMap[T]) Len() int { return len(m.Keys) }

// IsZero reports whether the object was absent or empty.
func (m OrderedMap[T]) IsZero() bool { return len(m.Keys) == 0 }
