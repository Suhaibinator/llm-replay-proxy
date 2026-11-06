package protocol

import (
	"strings"
	"testing"

	"github.com/local/llm-replay-proxy/internal/model"
)

// decisionBody is shaped like go-common's SystemOne client test fixture.
const decisionBody = `{"id":"gen-1","model":"pplx-decider-v1.2","provider":"Perplexity","answers":{"urgent":{"type":"noul","noul":0.12},"color":{"type":"choice","choice":"red","confidence":0.97,"probabilities":{"red":0.97,"blue":0.03}}},"usage":{"input_tokens":120,"output_tokens":4,"cost":0.0004}}`

func TestDecisionsValidate(t *testing.T) {
	if err := Validate(DecisionsRoute, false, model.Revision{Status: 200, Body: decisionBody}); err != nil {
		t.Fatalf("decision rejected: %v", err)
	}
	for name, tc := range map[string]struct {
		stream bool
		rev    model.Revision
	}{
		"error body":        {false, model.Revision{Status: 200, Body: `{"error":{"message":"x"},"model":"m","answers":{"q":{"type":"noul"}},"usage":{"input_tokens":1,"output_tokens":1}}`}},
		"no model":          {false, model.Revision{Status: 200, Body: `{"answers":{"q":{"type":"noul"}},"usage":{"input_tokens":1,"output_tokens":1}}`}},
		"no answers":        {false, model.Revision{Status: 200, Body: `{"model":"m","usage":{"input_tokens":1,"output_tokens":1}}`}},
		"empty answers":     {false, model.Revision{Status: 200, Body: `{"model":"m","answers":{},"usage":{"input_tokens":1,"output_tokens":1}}`}},
		"answer not object": {false, model.Revision{Status: 200, Body: `{"model":"m","answers":{"q":0.5},"usage":{"input_tokens":1,"output_tokens":1}}`}},
		"answer no type":    {false, model.Revision{Status: 200, Body: `{"model":"m","answers":{"q":{"noul":0.5}},"usage":{"input_tokens":1,"output_tokens":1}}`}},
		"no usage":          {false, model.Revision{Status: 200, Body: `{"model":"m","answers":{"q":{"type":"noul"}}}`}},
		"usage not counted": {false, model.Revision{Status: 200, Body: `{"model":"m","answers":{"q":{"type":"noul"}},"usage":{"input_tokens":1}}`}},
		"fractional usage":  {false, model.Revision{Status: 200, Body: `{"model":"m","answers":{"q":{"type":"noul"}},"usage":{"input_tokens":1.5,"output_tokens":1}}`}},
		"negative usage":    {false, model.Revision{Status: 200, Body: `{"model":"m","answers":{"q":{"type":"noul"}},"usage":{"input_tokens":1,"output_tokens":-1}}`}},
		"streamed":          {true, model.Revision{Status: 200, Headers: map[string]string{"Content-Type": "text/event-stream"}, Events: []model.Event{{Data: "data: " + decisionBody + "\n\n"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := Validate(DecisionsRoute, tc.stream, tc.rev); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestDecisionsHaveNoPlainTextEditor(t *testing.T) {
	rev := model.Revision{Status: 200, Body: decisionBody}
	if text, reason := Text(DecisionsRoute, false, rev); text != "" || !strings.Contains(reason, "typed answers") {
		t.Fatalf("Text = %q, %q", text, reason)
	}
	if _, err := EditText(DecisionsRoute, false, rev, "x"); err == nil {
		t.Fatal("decision edited as text")
	}
}

func TestDecisionsSummaries(t *testing.T) {
	req := `{"model":"perplexity/pplx-decider-v1.2","state":[{"page":1},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}],"questions":{"presence_2":{"type":"noul"},"page_kind":{"type":"choice"}}}`
	s := SummarizeRequest(DecisionsRoute, []byte(req))
	if s.Model != "perplexity/pplx-decider-v1.2" || s.Items != 2 || s.Images != 1 || s.Preview != "page_kind, presence_2" || s.Thread != "" || s.Bytes != int64(len(req)) {
		t.Fatalf("request summary %+v", s)
	}

	r := SummarizeResponse(DecisionsRoute, false, model.Revision{Status: 200, Body: decisionBody})
	if r.Model != "pplx-decider-v1.2" || usageString(r.Usage) != "in=120 cached=nil out=4 reasoning=nil total=nil" || r.Cost == nil || *r.Cost != 0.0004 {
		t.Fatalf("response summary %+v %s", r, usageString(r.Usage))
	}
}

func TestDecisionsValidateForRequest(t *testing.T) {
	req := []byte(`{"model":"m","state":"s","questions":{"urgent":{"type":"noul"},"color":{"type":"choice","criteria":{"red":"r","blue":"b"}}}}`)
	if err := ValidateForRequest(DecisionsRoute, req, model.Revision{Status: 200, Body: decisionBody}); err != nil {
		t.Fatalf("complete answers rejected: %v", err)
	}
	for name, body := range map[string]string{
		"missing answer": `{"model":"m","answers":{"urgent":{"type":"noul","noul":0.1}},"usage":{"input_tokens":1,"output_tokens":1}}`,
		"wrong type":     `{"model":"m","answers":{"urgent":{"type":"noul","noul":0.1},"color":{"type":"noul","noul":0.2}},"usage":{"input_tokens":1,"output_tokens":1}}`,
	} {
		if err := ValidateForRequest(DecisionsRoute, req, model.Revision{Status: 200, Body: body}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Other routes have no request-dependent rules.
	if err := ValidateForRequest(ChatRoute, []byte(`{`), model.Revision{}); err != nil {
		t.Fatalf("chat: %v", err)
	}
}
