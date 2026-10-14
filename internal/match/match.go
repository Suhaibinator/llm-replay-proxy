// Package match builds stable keys for inference requests.
package match

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/local/llm-replay-proxy/internal/jsontree"
)

const canonicalVersion = 4

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
	value = normalize(api, applyExclusions(value, exclusions))
	canonicalBody, err := encode(value)
	if err != nil {
		return "", nil, fmt.Errorf("match: canonicalize body: %w", err)
	}

	// A struct fixes the envelope's field order. jsontext.Value embeds the
	// already canonical request rather than quoting it.
	input, err := json.Marshal(struct {
		Version int            `json:"version"`
		API     string         `json:"api"`
		Body    jsontext.Value `json:"body"`
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

// decode parses a request strictly: invalid UTF-8, unpaired surrogate escapes
// and duplicate object names are rejected, so distinct wire inputs cannot
// silently share a key. Numbers keep their exact lexeme.
func decode(body []byte) (any, error) { return jsontree.Decode(body) }

// canonicalOptions format JSON as RFC 8785 (JCS) does, sorted object names,
// minimal string escapes and no whitespace, except that numbers keep their
// exact lexeme instead of being rounded through float64.
var canonicalOptions = []jsontext.Options{jsontext.CanonicalizeRawInts(false), jsontext.CanonicalizeRawFloats(false)}

// encode writes a decoded tree as canonical JSON.
func encode(value any) (jsontext.Value, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	out := jsontext.Value(b)
	if err := out.Canonicalize(canonicalOptions...); err != nil {
		return nil, err
	}
	return out, nil
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
