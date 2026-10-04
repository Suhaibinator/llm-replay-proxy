package match

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestKeyCanonicalAndSensitiveInputs(t *testing.T) {
	bodyA := []byte(`{"model":"m","n":9007199254740993123456789,"stream":true,"messages":[{"content":"hi"}]}`)
	bodyB := []byte(" { \"messages\" : [ { \"content\" : \"hi\" } ], \"stream\":true, \"n\":9007199254740993123456789, \"model\":\"m\" } ")
	keyA, canonicalA, err := Key("/v1/chat/completions", "provider-a", bodyA, nil)
	if err != nil {
		t.Fatal(err)
	}
	keyB, canonicalB, err := Key("/v1/chat/completions", "provider-a", bodyB, nil)
	if err != nil {
		t.Fatal(err)
	}
	if keyA != keyB || string(canonicalA) != string(canonicalB) {
		t.Fatalf("equivalent JSON differs:\n%s\n%s", canonicalA, canonicalB)
	}
	if !strings.Contains(string(canonicalA), "9007199254740993123456789") {
		t.Fatalf("numeric precision lost: %s", canonicalA)
	}
	for _, change := range []struct{ route, identity, body string }{
		{"/v1/responses", "provider-a", string(bodyA)},
		{"/v1/chat/completions", "provider-b", string(bodyA)},
		{"/v1/chat/completions", "provider-a", strings.Replace(string(bodyA), `"stream":true`, `"stream":false`, 1)},
	} {
		got, _, err := Key(change.route, change.identity, []byte(change.body), nil)
		if err != nil {
			t.Fatal(err)
		}
		if got == keyA {
			t.Errorf("input change did not change key: %+v", change)
		}
	}
	var envelope struct {
		Version int            `json:"version"`
		Body    map[string]any `json:"body"`
	}
	if err := json.Unmarshal(canonicalA, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Version != canonicalVersion {
		t.Errorf("version = %d", envelope.Version)
	}
}

func TestCanonicalNumbersPreserveExactLexeme(t *testing.T) {
	a, _, err := Key("/v1/responses", "p", []byte(`{"n":1}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := Key("/v1/responses", "p", []byte(`{"n":1.0}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("distinct numeric inputs collapsed")
	}
	_, canonical, err := Key("/v1/responses", "p", []byte(`{"tiny":123456789012345678901234567890e-999999999999999999999}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(canonical), `123456789012345678901234567890e-999999999999999999999`) {
		t.Fatalf("precision lost: %s", canonical)
	}
}

func TestKeyExclusions(t *testing.T) {
	a := []byte(`{"metadata":{"trace/id":"one","til~de":1},"items":[{"id":1},{"id":2}],"stream":false}`)
	b := []byte(`{"metadata":{"trace/id":"two","til~de":9},"items":[{"id":99},{"id":2}],"stream":false}`)
	exclusions := []string{"/metadata/trace~1id", "/metadata/til~0de", "/items/0/id"}
	ka, ca, err := Key("/v1/responses", "p", a, exclusions)
	if err != nil {
		t.Fatal(err)
	}
	kb, cb, err := Key("/v1/responses", "p", b, exclusions)
	if err != nil {
		t.Fatal(err)
	}
	if ka != kb || string(ca) != string(cb) {
		t.Fatalf("exclusions did not match:\n%s\n%s", ca, cb)
	}

	// A missing path is explicitly a no-op.
	kc, _, err := Key("/v1/responses", "p", a, append(exclusions, "/missing/path"))
	if err != nil {
		t.Fatal(err)
	}
	if kc != ka {
		t.Error("missing exclusion changed key")
	}
}

func TestArrayElementExclusion(t *testing.T) {
	_, canonical, err := Key("/v1/messages", "p", []byte(`{"stream":false,"a":[0,1,2,3]}`), []string{"/a/1", "/a/2"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(canonical), `"a":[0,3]`) {
		t.Fatalf("array items not removed: %s", canonical)
	}
	_, reverse, err := Key("/v1/messages", "p", []byte(`{"stream":false,"a":[0,1,2,3]}`), []string{"/a/2", "/a/1"})
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != string(reverse) {
		t.Fatalf("exclusion order changed result:\n%s\n%s", canonical, reverse)
	}
}

func TestEmptyArrayIsDistinctFromNull(t *testing.T) {
	empty, emptyInput, err := Key("/v1/responses", "p", []byte(`{"tools":[]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	null, _, err := Key("/v1/responses", "p", []byte(`{"tools":null}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty == null {
		t.Fatal("empty array and null share a key")
	}
	if !strings.Contains(string(emptyInput), `"tools":[]`) {
		t.Fatalf("empty array not preserved: %s", emptyInput)
	}
	_, nestedInput, err := Key("/v1/responses", "p", []byte(`{"a":[[],{"b":[]}]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(nestedInput), `"a":[[],{"b":[]}]`) {
		t.Fatalf("nested empty arrays not preserved: %s", nestedInput)
	}
	// Removing every element by exclusion and sending an empty array produce
	// the same matching input.
	excluded, excludedInput, err := Key("/v1/responses", "p", []byte(`{"tools":["x"]}`), []string{"/tools/0"})
	if err != nil {
		t.Fatal(err)
	}
	alreadyEmpty, _, err := Key("/v1/responses", "p", []byte(`{"tools":[]}`), []string{"/tools/0"})
	if err != nil {
		t.Fatal(err)
	}
	if excluded != alreadyEmpty || !strings.Contains(string(excludedInput), `"tools":[]`) {
		t.Fatalf("exclusion-emptied array differs from empty array: %s", excludedInput)
	}
}

func TestValidateExclusions(t *testing.T) {
	valid := []string{"/metadata/request_id", "/a~1b/~0key", "/array/0"}
	if err := ValidateExclusions(valid); err != nil {
		t.Fatal(err)
	}
	for _, paths := range [][]string{{""}, {"stream"}, {"/~2"}, {"/x~"}, {"/stream"}, {"/x", "/x"}} {
		if err := ValidateExclusions(paths); err == nil {
			t.Errorf("ValidateExclusions(%q) succeeded", paths)
		}
	}
}

func TestInvalidJSON(t *testing.T) {
	for _, body := range []string{"", "{", "{} {}", `[]`, `{"x":1,"x":2}`, `{"x":"\ud800"}`, `{"x":"\udc00"}`} {
		if _, _, err := Key("/v1/responses", "p", []byte(body), nil); err == nil {
			t.Errorf("Key accepted %q", body)
		}
	}
	if _, _, err := Key("", "p", []byte(`{}`), nil); err == nil {
		t.Error("empty route accepted")
	}
}

func TestStateReferences(t *testing.T) {
	body := []byte(`{"previous_response_id":"resp_1","conversation":{"id":"conv_1"},"container":"container_1","input":[{"type":"item_reference","id":"item_1"},{"type":"message","id":"not_state","content":"item_2"},"plain"]}`)
	got, err := StateReferences(body)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"resp_1", "conv_1", "container_1", "item_1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %q want %q", got, want)
	}

	got, err = StateReferences([]byte(`{"previous_response_id":"same","conversation":"same","container":{"id":"other"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "same,other" {
		t.Fatalf("dedupe/order: %q", got)
	}
	if _, err := StateReferences([]byte(`[]`)); err == nil {
		t.Error("array body accepted")
	}
	if _, err := StateReferences([]byte(`wat`)); err == nil {
		t.Error("invalid JSON accepted")
	}
}
