// Package protocol validates recorded inference responses and implements the
// deliberately narrow, plain-text editor used by the control panel.
package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/local/llm-replay-proxy/internal/model"
)

const (
	ChatRoute      = "/v1/chat/completions"
	ResponsesRoute = "/v1/responses"
	MessagesRoute  = "/v1/messages"
)

// Validate rejects failed, malformed, and incomplete recordings. For SSE it
// also checks that terminal output agrees with the ordered text deltas.
func Validate(route string, streaming bool, revision model.Revision) error {
	if revision.Status < 200 || revision.Status >= 300 {
		return fmt.Errorf("response status %d is not successful", revision.Status)
	}
	if revision.Status == 204 || revision.Status == 205 {
		return fmt.Errorf("response status %d cannot carry an inference response", revision.Status)
	}
	if streaming {
		for key, value := range revision.Headers {
			if strings.EqualFold(key, "Content-Type") && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "text/event-stream") {
				return fmt.Errorf("streaming response content type %q is not text/event-stream", value)
			}
		}
		if revision.Body != "" {
			return errors.New("streaming revision must not contain a response body")
		}
		if len(revision.Events) == 0 {
			return errors.New("streaming revision has no events")
		}
		var last int64 = -1
		for i, event := range revision.Events {
			if event.OffsetMS < 0 || event.OffsetMS > int64((1<<63-1)/time.Millisecond) || event.OffsetMS < last {
				return fmt.Errorf("event %d has an invalid offset", i)
			}
			last = event.OffsetMS
			if _, err := parseEventFrame(i, event.Data); err != nil {
				return fmt.Errorf("event %d: %w", i, err)
			}
		}
	} else {
		if len(revision.Events) != 0 {
			return errors.New("non-streaming revision must not contain events")
		}
		if strings.TrimSpace(revision.Body) == "" {
			return errors.New("non-streaming revision has an empty body")
		}
	}
	_, _, err := inspect(route, streaming, revision, false)
	if err != nil {
		return err
	}
	if err := validateStructure(route, streaming, revision); err != nil {
		return err
	}
	if route == ChatRoute && streaming {
		return validateChatTools(revision)
	}
	return nil
}

// Text returns editable assistant text, or a human-readable reason why the
// recording requires the advanced event editor. Invalid recordings also have
// no editable text.
func Text(route string, streaming bool, revision model.Revision) (string, string) {
	if err := Validate(route, streaming, revision); err != nil {
		return "", "Recording is invalid: " + err.Error()
	}
	text, ordinary, err := inspect(route, streaming, revision, true)
	if err != nil {
		return "", "Recording is invalid: " + err.Error()
	}
	if ordinary != "" {
		return "", ordinary
	}
	return text, ""
}

// EditText creates an unpersisted edited revision. It preserves event order,
// timing, protocol identities, headers, and status. Callers assign database
// identity and timestamps when publishing the new immutable revision.
func EditText(route string, streaming bool, revision model.Revision, text string) (model.Revision, error) {
	if _, reason := Text(route, streaming, revision); reason != "" {
		return model.Revision{}, errors.New(reason)
	}
	edited := cloneRevision(revision)
	var err error
	switch route {
	case ChatRoute:
		err = editChat(&edited, streaming, text)
	case ResponsesRoute:
		err = editResponses(&edited, streaming, text)
	case MessagesRoute:
		err = editMessages(&edited, streaming, text)
	default:
		err = fmt.Errorf("unsupported API route %q", route)
	}
	if err != nil {
		return model.Revision{}, err
	}
	edited.ID, edited.RecordingID, edited.CreatedAt = 0, revision.RecordingID, ""
	edited.Source = "edit"
	if err := Validate(route, streaming, edited); err != nil {
		return model.Revision{}, fmt.Errorf("edited recording is invalid: %w", err)
	}
	return edited, nil
}

type frame struct{ event, data string }

// utf8BOM may prefix the first frame of an event stream; the SSE specification
// strips it before parsing.
const utf8BOM = "\ufeff"

// splitField splits an SSE line into its field name and value. A line without
// a colon names a field whose value is empty, and one leading space after the
// colon is not part of the value.
func splitField(line string) (string, string) {
	name, value, found := strings.Cut(line, ":")
	if !found {
		return line, ""
	}
	return name, strings.TrimPrefix(value, " ")
}

// parseEventFrame parses the frame at position index of a recorded stream.
func parseEventFrame(index int, raw string) (frame, error) {
	if index == 0 {
		raw = strings.TrimPrefix(raw, utf8BOM)
	}
	return parseFrame(raw)
}

func parseFrame(raw string) (frame, error) {
	if !(strings.HasSuffix(raw, "\n\n") || strings.HasSuffix(raw, "\r\n\r\n")) {
		return frame{}, errors.New("SSE frame is missing its blank-line delimiter")
	}
	normal := strings.ReplaceAll(raw, "\r\n", "\n")
	payload := strings.TrimSuffix(normal, "\n\n")
	if strings.Contains(payload, "\n\n") {
		return frame{}, errors.New("event contains more than one SSE frame")
	}
	lines := strings.Split(payload, "\n")
	f := frame{}
	var data []string
	sawEvent := false
	for _, line := range lines {
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		name, value := splitField(line)
		switch name {
		case "event":
			if sawEvent {
				return frame{}, errors.New("SSE frame has multiple event fields")
			}
			sawEvent = true
			f.event = strings.TrimSpace(value)
		case "data":
			data = append(data, value)
		case "id", "retry":
		default:
			return frame{}, fmt.Errorf("malformed SSE field %q", line)
		}
	}
	if len(data) == 0 {
		return frame{}, nil
	} // comment-only keepalive
	f.data = strings.Join(data, "\n")
	return f, nil
}

func replaceFrameData(raw string, value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	bom := ""
	if strings.HasPrefix(raw, utf8BOM) {
		bom, raw = utf8BOM, strings.TrimPrefix(raw, utf8BOM)
	}
	newline := "\n"
	if strings.Contains(raw, "\r\n") {
		newline = "\r\n"
	}
	lines := strings.Split(strings.TrimSuffix(strings.ReplaceAll(raw, "\r\n", "\n"), "\n\n"), "\n")
	out := make([]string, 0, len(lines))
	replaced := false
	for _, line := range lines {
		if name, _ := splitField(line); name == "data" {
			if !replaced {
				out = append(out, "data: "+string(b))
				replaced = true
			}
			continue
		}
		out = append(out, line)
	}
	if !replaced {
		return "", errors.New("SSE frame has no data field")
	}
	return bom + strings.Join(out, newline) + newline + newline, nil
}

func decodeObject(s string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if v == nil {
		return nil, errors.New("JSON value must be an object")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("JSON has trailing data")
	}
	return v, nil
}

func bodyObject(body string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(body))
	dec.UseNumber()
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if v == nil {
		return nil, errors.New("JSON value must be an object")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("JSON has trailing data")
	}
	return v, nil
}

func eventObjects(rev model.Revision) ([]frame, []map[string]any, error) {
	frames := make([]frame, len(rev.Events))
	objects := make([]map[string]any, len(rev.Events))
	for i, event := range rev.Events {
		f, err := parseEventFrame(i, event.Data)
		if err != nil {
			return nil, nil, fmt.Errorf("event %d: %w", i, err)
		}
		frames[i] = f
		if f.data == "" {
			continue
		}
		if f.data == "[DONE]" {
			continue
		}
		obj, err := decodeObject(f.data)
		if err != nil {
			return nil, nil, fmt.Errorf("event %d: %w", i, err)
		}
		objects[i] = obj
		if f.event != "" {
			if typ, ok := asString(obj["type"]); ok && typ != f.event {
				return nil, nil, fmt.Errorf("event %d name %q does not match data type %q", i, f.event, typ)
			}
		}
	}
	return frames, objects, nil
}

func inspect(route string, streaming bool, rev model.Revision, classify bool) (string, string, error) {
	switch route {
	case ChatRoute:
		return inspectChat(streaming, rev, classify)
	case ResponsesRoute:
		return inspectResponses(streaming, rev, classify)
	case MessagesRoute:
		return inspectMessages(streaming, rev, classify)
	default:
		return "", "", fmt.Errorf("unsupported API route %q", route)
	}
}

func asSlice(v any) ([]any, bool)        { x, ok := v.([]any); return x, ok }
func asMap(v any) (map[string]any, bool) { x, ok := v.(map[string]any); return x, ok }
func asString(v any) (string, bool)      { x, ok := v.(string); return x, ok }
func nonempty(v any) bool                { return v != nil && fmt.Sprint(v) != "" && fmt.Sprint(v) != "<nil>" }
func unsupported(kind string) string {
	return "Plain-text editing is unavailable because this recording contains " + kind + ". Use the advanced event editor."
}

// annotatedText describes metadata whose character offsets or tokens are tied
// to the recorded text, so replacing the text would leave them stale.
const annotatedText = "annotations, citations, or logprobs tied to the text"

// hasEntries reports whether a JSON value carries any information: null,
// empty strings, arrays and objects (recursively) do not.
func hasEntries(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		for _, e := range x {
			if hasEntries(e) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func inspectChat(stream bool, rev model.Revision, classify bool) (string, string, error) {
	if !stream {
		o, err := bodyObject(rev.Body)
		if err != nil {
			return "", "", err
		}
		if o["error"] != nil {
			return "", "", errors.New("upstream error response")
		}
		choices, ok := asSlice(o["choices"])
		if !ok || len(choices) == 0 {
			return "", "", errors.New("chat response has no choices")
		}
		if len(choices) != 1 && classify {
			return "", unsupported("multiple choices"), nil
		}
		for i, raw := range choices {
			choice, ok := asMap(raw)
			if !ok {
				return "", "", fmt.Errorf("chat choice %d is malformed", i)
			}
			if !nonempty(choice["finish_reason"]) {
				return "", "", fmt.Errorf("chat choice %d is incomplete", i)
			}
			message, ok := asMap(choice["message"])
			if !ok {
				return "", "", fmt.Errorf("chat choice %d message is missing", i)
			}
			if calls, ok := asSlice(message["tool_calls"]); ok {
				for _, callRaw := range calls {
					call, _ := asMap(callRaw)
					if call["type"] != "function" {
						continue
					}
					fn, _ := asMap(call["function"])
					args, ok := asString(fn["arguments"])
					if !ok || validArguments(args) != nil {
						return "", "", fmt.Errorf("chat choice %d has malformed tool arguments", i)
					}
				}
			}
		}
		c, ok := asMap(choices[0])
		if !ok {
			return "", "", errors.New("chat choice is malformed")
		}
		if !nonempty(c["finish_reason"]) {
			return "", "", errors.New("chat response is incomplete")
		}
		m, ok := asMap(c["message"])
		if !ok {
			return "", "", errors.New("chat message is missing")
		}
		if classify && (m["tool_calls"] != nil || m["function_call"] != nil || m["reasoning"] != nil || m["reasoning_content"] != nil || m["reasoning_details"] != nil) {
			return "", unsupported("tool calls or reasoning"), nil
		}
		if classify && m["refusal"] != nil {
			return "", unsupported("a refusal"), nil
		}
		if classify && m["audio"] != nil {
			return "", unsupported("audio output"), nil
		}
		if classify && (hasEntries(m["annotations"]) || hasEntries(c["logprobs"])) {
			return "", unsupported(annotatedText), nil
		}
		// A response truncated by the token limit may carry no content at all.
		if m["content"] == nil && c["finish_reason"] == "length" {
			return "", "", nil
		}
		s, ok := asString(m["content"])
		if !ok {
			if classify {
				return "", unsupported("non-text or multimodal content"), nil
			}
			if m["tool_calls"] != nil || m["function_call"] != nil || m["refusal"] != nil || m["audio"] != nil {
				return "", "", nil
			}
			if _, ok := asSlice(m["content"]); ok {
				return "", "", nil
			}
			return "", "", errors.New("chat content is not text")
		}
		return s, "", nil
	}
	frames, objs, err := eventObjects(rev)
	if err != nil {
		return "", "", err
	}
	var b strings.Builder
	done := false
	seenChoices, finishedChoices := map[string]bool{}, map[string]bool{}
	for i, o := range objs {
		if frames[i].data == "[DONE]" {
			if done {
				return "", "", errors.New("chat stream has multiple [DONE] events")
			}
			done = true
			continue
		}
		if o == nil {
			if done {
				return "", "", errors.New("SSE event follows [DONE]")
			}
			continue
		}
		if done {
			return "", "", errors.New("SSE event follows [DONE]")
		}
		if o["error"] != nil {
			return "", "", fmt.Errorf("event %d is an upstream error", i)
		}
		choices, ok := asSlice(o["choices"])
		if !ok {
			return "", "", fmt.Errorf("event %d has no choices", i)
		}
		if classify && len(choices) > 1 {
			return "", unsupported("multiple choices"), nil
		}
		for _, raw := range choices {
			c, ok := asMap(raw)
			if !ok {
				return "", "", fmt.Errorf("event %d has malformed choice", i)
			}
			index, ok := c["index"]
			if !ok {
				return "", "", fmt.Errorf("event %d choice has no index", i)
			}
			key := fmt.Sprint(index)
			seenChoices[key] = true
			if nonempty(c["finish_reason"]) {
				finishedChoices[key] = true
			}
			d, _ := asMap(c["delta"])
			if classify && d["refusal"] != nil {
				return "", unsupported("a refusal"), nil
			}
			if classify && (d["tool_calls"] != nil || d["function_call"] != nil || d["reasoning"] != nil || d["reasoning_content"] != nil || d["reasoning_details"] != nil) {
				return "", unsupported("tool calls or reasoning"), nil
			}
			if classify && d["audio"] != nil {
				return "", unsupported("audio output"), nil
			}
			if classify && (hasEntries(d["annotations"]) || hasEntries(c["logprobs"])) {
				return "", unsupported(annotatedText), nil
			}
			if content, exists := d["content"]; exists && content != nil {
				s, ok := asString(content)
				if !ok {
					if classify {
						return "", unsupported("non-text or multimodal content"), nil
					}
					return "", "", errors.New("chat delta content is not text")
				}
				b.WriteString(s)
			}
		}
	}
	allFinished := len(seenChoices) > 0
	for key := range seenChoices {
		if !finishedChoices[key] {
			allFinished = false
		}
	}
	if !allFinished || !done {
		return "", "", errors.New("chat stream is incomplete (finish reason and [DONE] are required)")
	}
	if classify && len(seenChoices) > 1 {
		return "", unsupported("multiple choices"), nil
	}
	return b.String(), "", nil
}

func responseOutputText(response map[string]any, classify bool) (string, string, error) {
	if response["status"] != "completed" {
		return "", "", errors.New("response is not completed")
	}
	output, ok := asSlice(response["output"])
	if !ok {
		return "", "", errors.New("response output is missing")
	}
	var b strings.Builder
	textParts := 0
	for _, raw := range output {
		item, ok := asMap(raw)
		if !ok {
			return "", "", errors.New("response output item is malformed")
		}
		if item["type"] != "message" {
			if item["type"] == "function_call" {
				args, ok := asString(item["arguments"])
				if !ok || !json.Valid([]byte(args)) {
					return "", "", errors.New("response function call has malformed arguments")
				}
			}
			if classify {
				return "", unsupported("tool calls or reasoning"), nil
			}
			continue
		}
		content, ok := asSlice(item["content"])
		if !ok {
			return "", "", errors.New("response message content is missing")
		}
		for _, cr := range content {
			c, ok := asMap(cr)
			if !ok {
				return "", "", errors.New("response content item is malformed")
			}
			if c["type"] != "output_text" {
				if classify {
					return "", unsupported("reasoning or multimodal content"), nil
				}
				continue
			}
			textParts++
			if classify && (hasEntries(c["annotations"]) || hasEntries(c["logprobs"])) {
				return "", unsupported(annotatedText), nil
			}
			s, ok := asString(c["text"])
			if !ok {
				return "", "", errors.New("output_text text is missing")
			}
			b.WriteString(s)
		}
	}
	if classify && textParts != 1 {
		return "", unsupported("multiple text parts"), nil
	}
	return b.String(), "", nil
}

func inspectResponses(stream bool, rev model.Revision, classify bool) (string, string, error) {
	if !stream {
		o, e := bodyObject(rev.Body)
		if e != nil {
			return "", "", e
		}
		if o["error"] != nil {
			return "", "", errors.New("upstream error response")
		}
		return responseOutputText(o, classify)
	}
	frames, objs, e := eventObjects(rev)
	if e != nil {
		return "", "", e
	}
	var b strings.Builder
	var outputDoneText strings.Builder
	var partDoneText strings.Builder
	var final string
	completed, annotated := false, false
	textDeltas := 0
	for i, o := range objs {
		if frames[i].data == "[DONE]" {
			continue
		}
		if o == nil {
			continue
		}
		if completed {
			return "", "", errors.New("event follows response.completed")
		}
		typ, _ := asString(o["type"])
		if typ == "error" || typ == "response.failed" || typ == "response.incomplete" {
			return "", "", fmt.Errorf("event %d reports %s", i, typ)
		}
		if part, _ := asMap(o["part"]); strings.HasSuffix(typ, ".annotation.added") || hasEntries(o["logprobs"]) ||
			hasEntries(part["annotations"]) || hasEntries(part["logprobs"]) {
			annotated = true
		}
		switch typ {
		case "response.output_text.delta":
			textDeltas++
			if _, ok := asString(o["item_id"]); !ok {
				return "", "", fmt.Errorf("event %d has no item identity", i)
			}
			if o["output_index"] == nil || o["content_index"] == nil {
				return "", "", fmt.Errorf("event %d has no output/content index", i)
			}
			s, ok := asString(o["delta"])
			if !ok {
				return "", "", fmt.Errorf("event %d has invalid text delta", i)
			}
			b.WriteString(s)
		case "response.output_text.done":
			s, ok := asString(o["text"])
			if !ok {
				return "", "", fmt.Errorf("event %d has invalid completed text", i)
			}
			outputDoneText.WriteString(s)
		case "response.content_part.done":
			part, _ := asMap(o["part"])
			if part["type"] == "output_text" {
				s, ok := asString(part["text"])
				if !ok {
					return "", "", fmt.Errorf("event %d has invalid content part", i)
				}
				partDoneText.WriteString(s)
			}
		case "response.completed":
			r, ok := asMap(o["response"])
			if !ok {
				return "", "", errors.New("completed event has no response")
			}
			var reason string
			final, reason, e = responseOutputText(r, classify)
			if e != nil || reason != "" {
				return "", reason, e
			}
			completed = true
		}
	}
	if !completed {
		return "", "", errors.New("responses stream has no response.completed event")
	}
	if outputDoneText.Len() > 0 && outputDoneText.String() != b.String() {
		return "", "", fmt.Errorf("response text deltas %q do not agree with output done text %q", b.String(), outputDoneText.String())
	}
	if partDoneText.Len() > 0 && partDoneText.String() != b.String() {
		return "", "", fmt.Errorf("response text deltas %q do not agree with content part text %q", b.String(), partDoneText.String())
	}
	if b.String() != final {
		return "", "", fmt.Errorf("response text deltas %q do not agree with final output %q", b.String(), final)
	}
	if classify && annotated {
		return "", unsupported(annotatedText), nil
	}
	if classify && textDeltas == 0 {
		// The plain editor rewrites text deltas; it never inserts events.
		return "", "Plain-text editing is unavailable because this recording streams no output_text.delta event to rewrite. Use the advanced event editor.", nil
	}
	return final, "", nil
}

func messageText(o map[string]any, classify bool) (string, string, error) {
	if o["type"] != "message" {
		return "", "", errors.New("Anthropic response type is not message")
	}
	if !nonempty(o["stop_reason"]) {
		return "", "", errors.New("Anthropic message is incomplete")
	}
	content, ok := asSlice(o["content"])
	if !ok {
		return "", "", errors.New("Anthropic content is missing")
	}
	var b strings.Builder
	textBlocks := 0
	for _, raw := range content {
		c, ok := asMap(raw)
		if !ok {
			return "", "", errors.New("Anthropic content block is malformed")
		}
		if c["type"] != "text" {
			if classify {
				return "", unsupported("tool use, reasoning, or multimodal content"), nil
			}
			continue
		}
		textBlocks++
		if classify && hasEntries(c["citations"]) {
			return "", unsupported(annotatedText), nil
		}
		s, ok := asString(c["text"])
		if !ok {
			return "", "", errors.New("Anthropic text is missing")
		}
		b.WriteString(s)
	}
	if classify && textBlocks != 1 {
		return "", unsupported("multiple text blocks"), nil
	}
	return b.String(), "", nil
}
func inspectMessages(stream bool, rev model.Revision, classify bool) (string, string, error) {
	if !stream {
		o, e := bodyObject(rev.Body)
		if e != nil {
			return "", "", e
		}
		if o["type"] == "error" || o["error"] != nil {
			return "", "", errors.New("upstream error response")
		}
		return messageText(o, classify)
	}
	frames, objs, e := eventObjects(rev)
	if e != nil {
		return "", "", e
	}
	var b strings.Builder
	textBlocks := 0
	started, stopped, stopReason := false, false, false
	open := map[string]bool{}
	for i, o := range objs {
		if o == nil {
			continue
		}
		typ, _ := asString(o["type"])
		if typ == "error" || frames[i].event == "error" {
			return "", "", fmt.Errorf("event %d is an upstream error", i)
		}
		switch typ {
		case "message_start":
			if started || stopped {
				return "", "", fmt.Errorf("event %d has message_start out of order", i)
			}
			started = true
		case "content_block_start":
			if !started || stopped {
				return "", "", fmt.Errorf("event %d has content block out of order", i)
			}
			idx := fmt.Sprint(o["index"])
			block, _ := asMap(o["content_block"])
			if block["type"] != "text" {
				if classify {
					return "", unsupported("tool use, reasoning, or multimodal content"), nil
				}
			} else {
				textBlocks++
				if classify && hasEntries(block["citations"]) {
					return "", unsupported(annotatedText), nil
				}
				s, ok := asString(block["text"])
				if !ok {
					return "", "", errors.New("text block has no text")
				}
				b.WriteString(s)
			}
			open[idx] = true
		case "content_block_delta":
			if stopped {
				return "", "", fmt.Errorf("event %d follows message_stop", i)
			}
			idx := fmt.Sprint(o["index"])
			if !open[idx] {
				return "", "", fmt.Errorf("event %d updates an unopened block", i)
			}
			d, _ := asMap(o["delta"])
			if d["type"] != "text_delta" {
				if classify {
					return "", unsupported("tool use or reasoning deltas"), nil
				}
			} else {
				s, ok := asString(d["text"])
				if !ok {
					return "", "", errors.New("text delta has no text")
				}
				b.WriteString(s)
			}
		case "content_block_stop":
			idx := fmt.Sprint(o["index"])
			if !open[idx] {
				return "", "", fmt.Errorf("event %d closes an unopened block", i)
			}
			delete(open, idx)
		case "message_delta":
			if !started || stopped || len(open) > 0 {
				return "", "", fmt.Errorf("event %d has message_delta out of order", i)
			}
			d, _ := asMap(o["delta"])
			if nonempty(d["stop_reason"]) {
				stopReason = true
			}
		case "message_stop":
			if !started || stopped || !stopReason || len(open) > 0 {
				return "", "", fmt.Errorf("event %d has message_stop out of order", i)
			}
			stopped = true
		case "ping":
			// Anthropic may interleave keepalive pings with message events.
		default:
			return "", "", fmt.Errorf("event %d has unsupported Anthropic event type %q", i, typ)
		}
	}
	if !started || !stopped || !stopReason || len(open) > 0 {
		return "", "", errors.New("Anthropic stream is incomplete")
	}
	if classify && textBlocks != 1 {
		return "", unsupported("multiple text blocks"), nil
	}
	return b.String(), "", nil
}

func cloneRevision(r model.Revision) model.Revision {
	n := r
	n.Headers = map[string]string{}
	for k, v := range r.Headers {
		n.Headers[k] = v
	}
	n.Events = append([]model.Event(nil), r.Events...)
	return n
}
func marshalBody(o map[string]any) (string, error) { b, e := json.Marshal(o); return string(b), e }

func editChat(r *model.Revision, stream bool, text string) error {
	if !stream {
		o, e := bodyObject(r.Body)
		if e != nil {
			return e
		}
		choices, _ := asSlice(o["choices"])
		c, _ := asMap(choices[0])
		m, _ := asMap(c["message"])
		m["content"] = text
		r.Body, e = marshalBody(o)
		return e
	}
	_, objs, e := eventObjects(*r)
	if e != nil {
		return e
	}
	// The replacement goes into the first content delta of the (single) choice,
	// or, when the stream never sent a content key, into its first delta. Only
	// deltas up to and including the finish chunk are candidates.
	var target map[string]any
	var fallback map[string]any
	finished := false
	for _, o := range objs {
		choices, _ := asSlice(o["choices"])
		for _, raw := range choices {
			c, _ := asMap(raw)
			d, ok := asMap(c["delta"])
			if ok && !finished {
				if _, has := d["content"]; has && target == nil {
					target = d
				}
				if fallback == nil {
					fallback = d
				}
			}
			if c["finish_reason"] != nil {
				finished = true
			}
		}
	}
	if target == nil {
		target = fallback
	}
	if target == nil {
		return errors.New("chat stream has no editable text delta")
	}
	for _, o := range objs {
		choices, _ := asSlice(o["choices"])
		for _, raw := range choices {
			c, _ := asMap(raw)
			d, _ := asMap(c["delta"])
			if _, ok := d["content"]; ok {
				d["content"] = ""
			}
		}
	}
	target["content"] = text
	for i, o := range objs {
		if o == nil {
			continue
		}
		r.Events[i].Data, e = replaceFrameData(r.Events[i].Data, o)
		if e != nil {
			return e
		}
	}
	return nil
}
func editResponses(r *model.Revision, stream bool, text string) error {
	if !stream {
		o, e := bodyObject(r.Body)
		if e != nil {
			return e
		}
		setResponseText(o, text)
		r.Body, e = marshalBody(o)
		return e
	}
	_, objs, e := eventObjects(*r)
	if e != nil {
		return e
	}
	written := false
	for i, o := range objs {
		if o == nil {
			continue
		}
		typ, _ := asString(o["type"])
		switch typ {
		case "response.output_text.delta":
			if !written {
				o["delta"] = text
				written = true
			} else {
				o["delta"] = ""
			}
		case "response.output_text.done":
			o["text"] = text
		case "response.content_part.done":
			part, _ := asMap(o["part"])
			if part["type"] == "output_text" {
				part["text"] = text
			}
		case "response.content_part.added":
			part, _ := asMap(o["part"])
			if part["type"] == "output_text" {
				part["text"] = ""
			}
		case "response.output_item.done":
			item, _ := asMap(o["item"])
			if item != nil {
				setResponseText(map[string]any{"output": []any{item}}, text)
			}
		case "response.output_item.added":
			item, _ := asMap(o["item"])
			if item != nil {
				setResponseText(map[string]any{"output": []any{item}}, "")
			}
		case "response.completed":
			resp, _ := asMap(o["response"])
			setResponseText(resp, text)
		}
		r.Events[i].Data, e = replaceFrameData(r.Events[i].Data, o)
		if e != nil {
			return e
		}
	}
	if !written {
		return errors.New("responses stream has no editable text delta")
	}
	return nil
}
func setResponseText(o map[string]any, text string) {
	if _, ok := o["output_text"]; ok {
		o["output_text"] = text
	}
	output, _ := asSlice(o["output"])
	written := false
	for _, raw := range output {
		item, _ := asMap(raw)
		content, _ := asSlice(item["content"])
		for _, cr := range content {
			c, _ := asMap(cr)
			if c["type"] == "output_text" {
				if !written {
					c["text"] = text
					written = true
				} else {
					c["text"] = ""
				}
			}
		}
	}
}
func editMessages(r *model.Revision, stream bool, text string) error {
	if !stream {
		o, e := bodyObject(r.Body)
		if e != nil {
			return e
		}
		content, _ := asSlice(o["content"])
		written := false
		for _, raw := range content {
			c, _ := asMap(raw)
			if c["type"] == "text" {
				if !written {
					c["text"] = text
					written = true
				} else {
					c["text"] = ""
				}
			}
		}
		r.Body, e = marshalBody(o)
		return e
	}
	_, objs, e := eventObjects(*r)
	if e != nil {
		return e
	}
	written := false
	firstTextStart := -1
	for i, o := range objs {
		if o == nil {
			continue
		}
		typ, _ := asString(o["type"])
		if typ == "content_block_start" {
			b, _ := asMap(o["content_block"])
			if b["type"] == "text" {
				if firstTextStart < 0 {
					firstTextStart = i
				}
			}
		}
		if typ == "content_block_delta" {
			d, _ := asMap(o["delta"])
			if d["type"] == "text_delta" {
				if !written {
					d["text"] = text
					written = true
				} else {
					d["text"] = ""
				}
			}
		}
		r.Events[i].Data, e = replaceFrameData(r.Events[i].Data, o)
		if e != nil {
			return e
		}
	}
	if !written {
		if firstTextStart < 0 {
			return errors.New("Anthropic stream has no editable text content")
		}
		o := objs[firstTextStart]
		block, _ := asMap(o["content_block"])
		block["text"] = text
		r.Events[firstTextStart].Data, e = replaceFrameData(r.Events[firstTextStart].Data, o)
		return e
	}
	// Deltas carry the replacement; clear initial text to avoid duplication.
	o := objs[firstTextStart]
	block, _ := asMap(o["content_block"])
	block["text"] = ""
	r.Events[firstTextStart].Data, e = replaceFrameData(r.Events[firstTextStart].Data, o)
	if e != nil {
		return e
	}
	return nil
}
