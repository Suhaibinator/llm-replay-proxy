// Package jsontree decodes JSON into generic trees that keep every number's
// exact lexeme, and reads those numbers back.
package jsontree

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// Lenient accepts what v1 encoding/json accepted from providers: duplicate
// object names (the last wins) and invalid UTF-8 (read as U+FFFD).
var Lenient = []jsontext.Options{jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true)}

// Decode parses exactly one JSON value into map[string]any, []any, string,
// bool, nil and, for numbers, a jsontext.Value holding the exact lexeme. By
// default it rejects invalid UTF-8, unpaired surrogate escapes and duplicate
// object names; pass Lenient to accept them.
func Decode(b []byte, opts ...jsontext.Options) (any, error) {
	decoder := jsontext.NewDecoder(bytes.NewReader(b), opts...)
	value, err := decodeValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.ReadToken(); err == nil {
		return nil, errors.New("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return nil, err
	}
	return value, nil
}

// Object decodes b and requires a JSON object.
func Object(b []byte, opts ...jsontext.Options) (map[string]any, error) {
	v, err := Decode(b, opts...)
	if err != nil {
		return nil, err
	}
	o, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("JSON value is not an object")
	}
	return o, nil
}

func decodeValue(decoder *jsontext.Decoder) (any, error) {
	switch decoder.PeekKind() {
	case '{':
		if _, err := decoder.ReadToken(); err != nil {
			return nil, err
		}
		object := map[string]any{}
		for decoder.PeekKind() != '}' {
			token, err := decoder.ReadToken()
			if err != nil {
				return nil, err
			}
			// A token is only valid until the next read.
			name := token.String()
			value, err := decodeValue(decoder)
			if err != nil {
				return nil, err
			}
			object[name] = value
		}
		if _, err := decoder.ReadToken(); err != nil {
			return nil, err
		}
		return object, nil
	case '[':
		if _, err := decoder.ReadToken(); err != nil {
			return nil, err
		}
		// A non-nil slice keeps an empty array distinct from null when the
		// tree is encoded again.
		array := []any{}
		for decoder.PeekKind() != ']' {
			value, err := decodeValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if _, err := decoder.ReadToken(); err != nil {
			return nil, err
		}
		return array, nil
	case '0':
		raw, err := decoder.ReadValue()
		if err != nil {
			return nil, err
		}
		return raw.Clone(), nil
	}
	token, err := decoder.ReadToken()
	if err != nil {
		return nil, err
	}
	switch token.Kind() {
	case '"':
		return token.String(), nil
	case 't', 'f':
		return token.Bool(), nil
	case 'n':
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected token %s", token.Kind())
}

// Number returns v's lexeme when v is a decoded JSON number.
func Number(v any) (string, bool) {
	n, ok := v.(jsontext.Value)
	if !ok || n.Kind() != '0' {
		return "", false
	}
	return string(n), true
}

// Int64 returns v as an int64 when it is a JSON number written as an integer
// that fits.
func Int64(v any) (int64, bool) {
	s, ok := Number(v)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// Float64 returns v as a float64 when it is a JSON number in range.
func Float64(v any) (float64, bool) {
	s, ok := Number(v)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

// Stable marshals with sorted map keys and invalid UTF-8 written as U+FFFD,
// the output v1 encoding/json produced, minus its HTML escaping.
var Stable = []json.Options{json.Deterministic(true), jsontext.AllowInvalidUTF8(true)}

// Marshal encodes v with Stable options.
func Marshal(v any) ([]byte, error) { return json.Marshal(v, Stable...) }
