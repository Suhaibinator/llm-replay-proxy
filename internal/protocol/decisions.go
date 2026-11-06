package protocol

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/local/llm-replay-proxy/internal/jsontree"
	"github.com/local/llm-replay-proxy/internal/model"
)

// The decisions API (OpenRouter's /api/alpha/decisions, the TypeSafe
// SystemOne wire format) asks a decision model typed questions about a state
// and gets one answer per question back. It is a single JSON request and
// response: there is no stream, no conversation and no assistant text.
//
// Request:  {"model", "state", "questions": {id: {"type", "instructions", ...}}}
// Response: {"id", "model", "provider", "answers": {id: {"type", ...}},
//            "usage": {"input_tokens", "output_tokens", "cost"}}
//
// Validation checks the envelope only. Answer shapes depend on the question
// type (noul, choice, …) and the endpoint is still alpha, so the caller's
// client owns checking them.

// errDecisionsStream rejects a stream on the decisions route; the API has none.
var errDecisionsStream = errors.New("decisions responses are not streamed")

// noDecisionText is why a decisions recording has no plain-text editor.
const noDecisionText = "Plain-text editing is unavailable because a decision response has typed answers, not text. Use the advanced editor."

func inspectDecisions(stream bool, rev model.Revision) (string, string, error) {
	if stream {
		return "", "", errDecisionsStream
	}
	o, err := bodyObject(rev.Body)
	if err != nil {
		return "", "", err
	}
	if o["error"] != nil {
		return "", "", errors.New("upstream error response")
	}
	if _, err := requiredString(o, "model"); err != nil {
		return "", "", err
	}
	answers, ok := asMap(o["answers"])
	if !ok || len(answers) == 0 {
		return "", "", errors.New("decision response has no answers")
	}
	for _, id := range slices.Sorted(maps.Keys(answers)) {
		answer, ok := asMap(answers[id])
		if !ok {
			return "", "", fmt.Errorf("answer %q must be an object", id)
		}
		if _, err := requiredString(answer, "type"); err != nil {
			return "", "", fmt.Errorf("answer %q: %w", id, err)
		}
	}
	usage, ok := asMap(o["usage"])
	if !ok {
		return "", "", errors.New("decision response has no usage")
	}
	for _, field := range []string{"input_tokens", "output_tokens"} {
		if n, ok := jsontree.Int64(usage[field]); !ok || n < 0 {
			return "", "", fmt.Errorf("usage.%s must be a nonnegative integer", field)
		}
	}
	return "", noDecisionText, nil
}

// ValidateForRequest checks a response against the request that produced it,
// beyond what Validate can see from the response alone. Only decisions need
// it: a decision must answer every question it was asked, each with the
// question's own type, or the client rejects it. Recording such a response
// would make every replay fail the same way, so it is never published.
func ValidateForRequest(route string, request []byte, revision model.Revision) error {
	if route != DecisionsRoute {
		return nil
	}
	req, err := jsontree.Object(request)
	if err != nil {
		return fmt.Errorf("decision request: %w", err)
	}
	questions, ok := asMap(req["questions"])
	if !ok {
		return errors.New("decision request has no questions object")
	}
	o, err := bodyObject(revision.Body)
	if err != nil {
		return err
	}
	answers, _ := asMap(o["answers"])
	for _, id := range slices.Sorted(maps.Keys(questions)) {
		answer, ok := asMap(answers[id])
		if !ok {
			return fmt.Errorf("question %q has no answer", id)
		}
		question, _ := asMap(questions[id])
		if want, _ := asString(question["type"]); want != "" && answer["type"] != want {
			return fmt.Errorf("answer %q has type %v, want %q", id, answer["type"], want)
		}
	}
	return nil
}

// Streams reports whether route has a streamed form at all.
func Streams(route string) bool { return route != DecisionsRoute }

// summarizeDecisionsRequest describes a decisions request: its model, how many
// questions it asks (as Items), the images in its state, and the question IDs
// as the preview. Decisions are independent calls, so there is no thread.
func summarizeDecisionsRequest(s *model.RequestSummary, req map[string]any) {
	questions, _ := asMap(req["questions"])
	s.Items = len(questions)
	s.Images = images(req["state"])
	s.Preview = preview(strings.Join(slices.Sorted(maps.Keys(questions)), ", "))
}

func summarizeDecisionsBody(s *model.ResponseSummary, o map[string]any) {
	s.Model, _ = asString(o["model"])
	usage, ok := asMap(o["usage"])
	if !ok {
		return
	}
	s.Usage = model.TokenUsage{Input: count(usage["input_tokens"]), Output: count(usage["output_tokens"])}
	s.Cost = cost(usage["cost"])
}
