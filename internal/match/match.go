// Package match builds stable keys for inference requests.
package match

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

const canonicalVersion = 2

// API names the inference API a route speaks. It is the only part of the
// request outside the body that matching uses: the upstream provider, its URL
// and its headers are deliberately left out, so a recording replays whichever
// provider a request selects.
func API(route string) (string, error) {
	switch route {
	case "/v1/chat/completions":
		return "chat_completions", nil
	case "/v1/responses":
		return "responses", nil
	case "/v1/messages":
		return "messages", nil
	default:
		return "", fmt.Errorf("match: unsupported route %q", route)
	}
}

// Key returns the SHA-256 key and the exact, versioned input hashed to produce it.
// Exclusions affect only the copy used for matching; body is never modified.
func Key(route string, body []byte, exclusions []string) (string, []byte, error) {
	api, err := API(route)
	if err != nil {
		return "", nil, err
	}
	if err := ValidateExclusions(exclusions); err != nil {
		return "", nil, err
	}

	value, err := decode(body)
	if err != nil {
		return "", nil, fmt.Errorf("match: invalid JSON body: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return "", nil, errors.New("match: request body must be a JSON object")
	}
	value = applyExclusions(value, exclusions)
	canonicalBody, err := json.Marshal(value)
	if err != nil {
		return "", nil, fmt.Errorf("match: canonicalize body: %w", err)
	}

	// A struct fixes the envelope's field order. RawMessage embeds the already
	// canonical request rather than quoting it.
	input, err := json.Marshal(struct {
		Version int             `json:"version"`
		API     string          `json:"api"`
		Body    json.RawMessage `json:"body"`
	}{canonicalVersion, api, canonicalBody})
	if err != nil {
		return "", nil, fmt.Errorf("match: encode matching input: %w", err)
	}
	sum := sha256.Sum256(input)
	return hex.EncodeToString(sum[:]), input, nil
}

// ValidateExclusions verifies RFC 6901 JSON Pointer syntax. The document root
// and top-level stream flag cannot be excluded because matching must always
// distinguish streaming and non-streaming requests.
func ValidateExclusions(exclusions []string) error {
	seen := make(map[string]struct{}, len(exclusions))
	for _, pointer := range exclusions {
		if !utf8.ValidString(pointer) {
			return errors.New("match: exclusion contains invalid UTF-8")
		}
		if pointer == "" {
			return errors.New("match: excluding the document root is not allowed")
		}
		if !strings.HasPrefix(pointer, "/") {
			return fmt.Errorf("match: invalid JSON Pointer %q: must start with '/'", pointer)
		}
		for i := 0; i < len(pointer); i++ {
			if pointer[i] != '~' {
				continue
			}
			if i+1 >= len(pointer) || (pointer[i+1] != '0' && pointer[i+1] != '1') {
				return fmt.Errorf("match: invalid JSON Pointer %q: '~' must be followed by 0 or 1", pointer)
			}
			i++
		}
		if pointer == "/stream" {
			return errors.New("match: /stream cannot be excluded")
		}
		if _, ok := seen[pointer]; ok {
			return fmt.Errorf("match: duplicate exclusion %q", pointer)
		}
		seen[pointer] = struct{}{}
	}
	return nil
}

// StateReferences returns provider-owned state identifiers referenced by a
// request. These fields are the stateful inputs supported by the Responses and
// Messages APIs. Results are unique and retain their request-field order.
func StateReferences(body []byte) ([]string, error) {
	value, err := decode(body)
	if err != nil {
		return nil, fmt.Errorf("match: invalid JSON body: %w", err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("match: request body must be a JSON object")
	}

	var refs []string
	seen := map[string]struct{}{}
	add := func(id string) {
		if id == "" {
			return
		}
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			refs = append(refs, id)
		}
	}
	if id, ok := object["previous_response_id"].(string); ok {
		add(id)
	}
	for _, field := range []string{"conversation", "container"} {
		switch state := object[field].(type) {
		case string:
			add(state)
		case map[string]any:
			if id, ok := state["id"].(string); ok {
				add(id)
			}
		}
	}
	// Responses may refer directly to a provider-owned output item. Only the
	// discriminated item_reference shape is stateful; message/tool content IDs
	// and arbitrary strings are deliberately ignored.
	if input, ok := object["input"].([]any); ok {
		for _, value := range input {
			item, ok := value.(map[string]any)
			if !ok || item["type"] != "item_reference" {
				continue
			}
			if id, ok := item["id"].(string); ok {
				add(id)
			}
		}
	}
	return refs, nil
}

func decode(body []byte) (any, error) {
	if !utf8.Valid(body) {
		return nil, errors.New("invalid UTF-8")
	}
	if err := validateSurrogates(body); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	value, err := decodeValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err == nil {
		return nil, errors.New("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return nil, err
	}
	return value, nil
}

func decodeValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return token, nil
	}
	switch delim {
	case '{':
		object := map[string]any{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("object key is not a string")
			}
			if _, duplicate := object[key]; duplicate {
				return nil, fmt.Errorf("duplicate object key %q", key)
			}
			value, err := decodeValue(decoder)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
			if err != nil {
				return nil, err
			}
			return nil, errors.New("unterminated object")
		}
		return object, nil
	case '[':
		// A non-nil slice keeps an empty array distinct from null when the
		// canonical body is re-encoded.
		array := []any{}
		for decoder.More() {
			value, err := decodeValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
			if err != nil {
				return nil, err
			}
			return nil, errors.New("unterminated array")
		}
		return array, nil
	default:
		return nil, fmt.Errorf("unexpected delimiter %q", delim)
	}
}

// encoding/json replaces lone UTF-16 surrogate escapes with U+FFFD. Rejecting
// them prevents distinct wire inputs from silently acquiring the same key.
func validateSurrogates(body []byte) error {
	for i := 0; i < len(body); i++ {
		if body[i] != '"' {
			continue
		}
		for i++; i < len(body) && body[i] != '"'; i++ {
			if body[i] != '\\' {
				continue
			}
			i++
			if i >= len(body) {
				return errors.New("unterminated string escape")
			}
			if body[i] != 'u' {
				continue
			}
			code, next, err := unicodeEscape(body, i)
			if err != nil {
				return err
			}
			i = next
			if code >= 0xdc00 && code <= 0xdfff {
				return errors.New("unpaired low surrogate")
			}
			if code >= 0xd800 && code <= 0xdbff {
				if i+2 >= len(body) || body[i+1] != '\\' || body[i+2] != 'u' {
					return errors.New("unpaired high surrogate")
				}
				low, lowNext, err := unicodeEscape(body, i+2)
				if err != nil || low < 0xdc00 || low > 0xdfff {
					return errors.New("unpaired high surrogate")
				}
				i = lowNext
			}
		}
	}
	return nil
}

func unicodeEscape(body []byte, u int) (uint64, int, error) {
	if u+4 >= len(body) {
		return 0, u, errors.New("short Unicode escape")
	}
	code, err := strconv.ParseUint(string(body[u+1:u+5]), 16, 16)
	if err != nil {
		return 0, u, errors.New("invalid Unicode escape")
	}
	return code, u + 4, nil
}

// Excluded reports whether the JSON Pointer path names a value that one of the
// exclusions removes before matching: the excluded value itself or anything
// inside it. Paths and exclusions use the same RFC 6901 escaping.
func Excluded(path string, exclusions []string) bool {
	for _, pointer := range exclusions {
		if path == pointer || strings.HasPrefix(path, pointer+"/") {
			return true
		}
	}
	return false
}

func parsePointer(pointer string) []string {
	parts := strings.Split(pointer[1:], "/")
	for i := range parts {
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(parts[i], "~1", "/"), "~0", "~")
	}
	return parts
}

type exclusionNode struct {
	remove   bool
	children map[string]*exclusionNode
}

func applyExclusions(value any, pointers []string) any {
	root := &exclusionNode{children: map[string]*exclusionNode{}}
	for _, pointer := range pointers {
		node := root
		for _, token := range parsePointer(pointer) {
			if node.children[token] == nil {
				node.children[token] = &exclusionNode{children: map[string]*exclusionNode{}}
			}
			node = node.children[token]
		}
		node.remove = true
	}
	return exclude(value, root)
}

func exclude(value any, rules *exclusionNode) any {
	switch node := value.(type) {
	case map[string]any:
		for token, rule := range rules.children {
			child, ok := node[token]
			if !ok {
				continue
			}
			if rule.remove {
				delete(node, token)
			} else {
				node[token] = exclude(child, rule)
			}
		}
	case []any:
		remove := map[int]struct{}{}
		for token, rule := range rules.children {
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(node) || strconv.Itoa(index) != token {
				continue
			}
			if rule.remove {
				remove[index] = struct{}{}
			} else {
				node[index] = exclude(node[index], rule)
			}
		}
		if len(remove) > 0 {
			kept := node[:0]
			for index, item := range node {
				if _, drop := remove[index]; !drop {
					kept = append(kept, item)
				}
			}
			return kept
		}
	}
	return value
}
