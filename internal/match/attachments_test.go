package match

import (
	"encoding/base64"
	"strings"
	"testing"
)

const (
	s3a = "https://bucket.s3.us-east-1.amazonaws.com/uploads/cat.png?versionId=v1&X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIA%2F20261014%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20261014T120000Z&X-Amz-Expires=900&X-Amz-SignedHeaders=host&X-Amz-Security-Token=tok1&X-Amz-Signature=aaaa&x-id=GetObject"
	s3b = "https://bucket.s3.us-east-1.amazonaws.com/uploads/cat.png?versionId=v1&X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIA%2F20261015%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20261015T093000Z&X-Amz-Expires=3600&X-Amz-SignedHeaders=host&X-Amz-Security-Token=tok2&X-Amz-Signature=bbbb&x-id=GetObject"
)

func TestPresignedURLsMatchWhereverAttachmentsAppear(t *testing.T) {
	for _, tc := range []struct{ name, route, body string }{
		{"chat image_url", "/v1/chat/completions", `{"messages":[{"role":"user","content":[{"type":"text","text":"what is this?"},{"type":"image_url","image_url":{"url":"URL","detail":"high"}}]}]}`},
		{"responses input_image", "/v1/responses", `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"URL"}]}]}`},
		{"responses input_file", "/v1/responses", `{"input":[{"role":"user","content":[{"type":"input_file","file_url":"URL"}]}]}`},
		{"responses tool output", "/v1/responses", `{"input":[{"type":"function_call","call_id":"c","name":"look","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":[{"type":"input_image","image_url":"URL"}]}]}`},
		{"messages image", "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"URL"}}]}]}`},
		{"messages document", "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"url","url":"URL"},"title":"t"}]}]}`},
		{"messages tool_result", "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":"url","url":"URL"}}]}]}]}`},
		{"messages system", "/v1/messages", `{"system":[{"type":"document","source":{"type":"url","url":"URL"}}],"messages":[]}`},
	} {
		with := func(u string) string { return mustKey(t, tc.route, strings.Replace(tc.body, "URL", u, 1)) }
		if with(s3a) != with(s3b) {
			t.Errorf("%s: re-signing the URL changed the key", tc.name)
		}
		if with(s3a) == with(strings.Replace(s3a, "cat.png", "dog.png", 1)) {
			t.Errorf("%s: a different object matched", tc.name)
		}
	}
}

func TestStripSignature(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"aws sigv4 keeps other parameters", s3a,
			"https://bucket.s3.us-east-1.amazonaws.com/uploads/cat.png?versionId=v1&x-id=GetObject"},
		{"aws sigv2", "https://b.s3.amazonaws.com/k.pdf?AWSAccessKeyId=AKIA&Expires=1760000000&Signature=abc%3D",
			"https://b.s3.amazonaws.com/k.pdf"},
		{"gcs v4", "https://storage.googleapis.com/b/o.jpg?X-Goog-Algorithm=GOOG4-RSA-SHA256&X-Goog-Credential=sa%40p.iam&X-Goog-Date=20261014T000000Z&X-Goog-Expires=600&X-Goog-SignedHeaders=host&X-Goog-Signature=9f",
			"https://storage.googleapis.com/b/o.jpg"},
		{"gcs v2", "https://storage.googleapis.com/b/o.jpg?GoogleAccessId=sa%40p.iam&Expires=1760000000&Signature=x",
			"https://storage.googleapis.com/b/o.jpg"},
		{"azure sas keeps response overrides", "https://acct.blob.core.windows.net/c/a.png?sp=r&st=2026-10-14T00:00:00Z&se=2026-10-14T01:00:00Z&spr=https&sv=2022-11-02&sr=b&rscd=inline&sig=abc%2B%3D",
			"https://acct.blob.core.windows.net/c/a.png?rscd=inline"},
		{"cloudfront", "https://d111.cloudfront.net/i.png?Expires=1760000000&Signature=s~&Key-Pair-Id=K2",
			"https://d111.cloudfront.net/i.png"},
		{"supabase", "https://x.supabase.co/storage/v1/object/sign/media/a.png?token=eyJ.a.b",
			"https://x.supabase.co/storage/v1/object/sign/media/a.png"},
		{"fragment kept", "https://b.s3.amazonaws.com/k.pdf?X-Amz-Signature=a&X-Amz-Date=1#page=2",
			"https://b.s3.amazonaws.com/k.pdf#page=2"},
		{"case-insensitive names", "https://b.s3.amazonaws.com/k?x-amz-signature=a&x-amz-date=1", "https://b.s3.amazonaws.com/k"},
		// Without a scheme's signature parameter nothing is removed.
		{"unsigned expires", "https://example.com/report.pdf?Expires=1760000000&v=2", "https://example.com/report.pdf?Expires=1760000000&v=2"},
		{"token outside supabase", "https://example.com/a.png?token=t", "https://example.com/a.png?token=t"},
		{"sig without sv", "https://example.com/a.png?sig=1", "https://example.com/a.png?sig=1"},
		{"not http", "s3://bucket/k?X-Amz-Signature=a", "s3://bucket/k?X-Amz-Signature=a"},
		{"no query", "https://example.com/a.png", "https://example.com/a.png"},
	} {
		if got := StripSignature(tc.in); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

func TestInlineAttachmentsMatchByContent(t *testing.T) {
	data := make([]byte, 3000)
	for i := range data {
		data[i] = byte(i * 7)
	}
	b64 := base64.StdEncoding.EncodeToString(data)
	var wrapped strings.Builder // MIME-style 76-column lines, as some encoders write
	for rest := b64; rest != ""; {
		n := min(76, len(rest))
		wrapped.WriteString(rest[:n] + `\r\n`)
		rest = rest[n:]
	}
	changed := append([]byte(nil), data...)
	changed[0] ^= 1
	other := base64.StdEncoding.EncodeToString(changed)

	for _, tc := range []struct{ name, route, body, sameAs, differs string }{
		{"chat data url", "/v1/chat/completions", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + b64 + `"}}]}]}`,
			`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + strings.TrimRight(wrapped.String(), "=") + `"}}]}]}`,
			`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + other + `"}}]}]}`},
		{"media type still matters", "/v1/chat/completions", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + b64 + `"}}]}]}`,
			"",
			`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + b64 + `"}}]}]}`},
		{"chat file and audio", "/v1/chat/completions", `{"messages":[{"role":"user","content":[{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,` + b64 + `"}},{"type":"input_audio","input_audio":{"format":"wav","data":"` + b64 + `"}}]}]}`,
			`{"messages":[{"role":"user","content":[{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,` + wrapped.String() + `"}},{"type":"input_audio","input_audio":{"format":"wav","data":"` + wrapped.String() + `"}}]}]}`,
			`{"messages":[{"role":"user","content":[{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,` + b64 + `"}},{"type":"input_audio","input_audio":{"format":"wav","data":"` + other + `"}}]}]}`},
		{"responses input_file", "/v1/responses", `{"input":[{"role":"user","content":[{"type":"input_file","filename":"a.pdf","file_data":"data:application/pdf;base64,` + b64 + `"}]}]}`,
			`{"input":[{"role":"user","content":[{"type":"input_file","filename":"a.pdf","file_data":"data:application/pdf;base64,` + wrapped.String() + `"}]}]}`,
			`{"input":[{"role":"user","content":[{"type":"input_file","filename":"b.pdf","file_data":"data:application/pdf;base64,` + b64 + `"}]}]}`},
		{"messages base64 source", "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}]}]}`,
			`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + wrapped.String() + `"}}]}]}`,
			`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + other + `"}}]}]}`},
	} {
		key := mustKey(t, tc.route, tc.body)
		if tc.sameAs != "" && mustKey(t, tc.route, tc.sameAs) != key {
			t.Errorf("%s: equal content encoded differently changed the key", tc.name)
		}
		if mustKey(t, tc.route, tc.differs) == key {
			t.Errorf("%s: different content matched", tc.name)
		}
	}

	// The matching input carries a digest, not the payload.
	_, input, err := Key("/v1/messages", []byte(`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+b64+`"}}]}]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(input), b64[:100]) || !strings.Contains(string(input), `"data":"sha256:`) {
		t.Errorf("matching input = %s", input)
	}
}

func TestAttachmentNormalizationStaysInItsSlots(t *testing.T) {
	// Text that merely contains a presigned URL is compared verbatim.
	text := func(u string) string {
		return `{"input":[{"role":"user","content":[{"type":"input_text","text":"see ` + u + `"}]}]}`
	}
	if mustKey(t, "/v1/responses", text(s3a)) == mustKey(t, "/v1/responses", text(s3b)) {
		t.Error("a URL in text was normalized")
	}
	// Fields outside the conversation are not attachment slots.
	meta := func(u string) string {
		return `{"input":"hi","metadata":{"type":"input_image","image_url":"` + u + `"}}`
	}
	if mustKey(t, "/v1/responses", meta(s3a)) == mustKey(t, "/v1/responses", meta(s3b)) {
		t.Error("metadata was normalized")
	}
	// Invalid base64 and file ids are compared verbatim.
	for _, pair := range [][2]string{
		{`{"type":"base64","media_type":"image/png","data":"not base64!"}`, `{"type":"base64","media_type":"image/png","data":"not  base64!"}`},
		{`{"type":"file","file_id":"file_1"}`, `{"type":"file","file_id":"file_2"}`},
	} {
		body := func(source string) string {
			return `{"messages":[{"role":"user","content":[{"type":"image","source":` + source + `}]}]}`
		}
		if mustKey(t, "/v1/messages", body(pair[0])) == mustKey(t, "/v1/messages", body(pair[1])) {
			t.Errorf("%s matched %s", pair[0], pair[1])
		}
	}
}
