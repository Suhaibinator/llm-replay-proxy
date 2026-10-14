package match

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strings"
)

// Attachments are images, files and audio that a request carries inline
// (base64 or a data: URL) or by URL. Wherever one appears in the
// conversation, including inside tool results, normalizeAttachments rewrites
// the copy used for matching:
//
//   - A presigned URL loses its signing parameters (credential, signature,
//     date, expiry, session token). They change every time the URL is signed,
//     while the object it names does not. Parameters are removed only when
//     their scheme's signature parameter is present; the host, path and every
//     other parameter still have to match.
//   - Inline data becomes the SHA-256 of its decoded bytes, so equal content
//     matches however its base64 was wrapped or padded, and the matching
//     input and request comparisons show a short digest instead of megabytes.
//     A value that is not valid base64 is compared verbatim.
//
// Provider file ids are compared verbatim, and a URL is never fetched: the
// key stays a pure function of the request, so replay works offline.

// conversationFields name the request members that hold the conversation, by API.
var conversationFields = map[string][]string{
	"chat_completions": {"messages"},
	"responses":        {"input"},
	"messages":         {"messages", "system"},
}

func normalizeAttachments(api string, request map[string]any) {
	for _, field := range conversationFields[api] {
		walkAttachments(request[field])
	}
}

// walkAttachments rewrites attachment slots in v, recognised by their content
// part's type, at any depth.
func walkAttachments(v any) {
	switch node := v.(type) {
	case []any:
		for _, child := range node {
			walkAttachments(child)
		}
	case map[string]any:
		switch node["type"] {
		case "image_url": // Chat Completions
			if image, ok := node["image_url"].(map[string]any); ok {
				urlField(image, "url")
			} else {
				urlField(node, "image_url")
			}
		case "input_image": // Responses
			urlField(node, "image_url")
		case "input_file": // Responses
			urlField(node, "file_url")
			dataField(node, "file_data")
		case "file": // Chat Completions
			if file, ok := node["file"].(map[string]any); ok {
				dataField(file, "file_data")
			}
		case "input_audio": // Chat Completions
			if audio, ok := node["input_audio"].(map[string]any); ok {
				dataField(audio, "data")
			}
		case "image", "document": // Messages
			if source, ok := node["source"].(map[string]any); ok {
				switch source["type"] {
				case "url":
					urlField(source, "url")
				case "base64":
					dataField(source, "data")
				}
			}
		}
		for _, child := range node {
			walkAttachments(child)
		}
	}
}

// urlField normalizes a slot holding a URL, which may also be a data: URL.
func urlField(o map[string]any, field string) {
	s, ok := o[field].(string)
	if !ok {
		return
	}
	if digest, ok := dataURLDigest(s); ok {
		o[field] = digest
		return
	}
	o[field] = StripSignature(s)
}

// dataField normalizes a slot holding inline content: a data: URL or bare base64.
func dataField(o map[string]any, field string) {
	s, ok := o[field].(string)
	if !ok {
		return
	}
	if digest, ok := dataURLDigest(s); ok {
		o[field] = digest
	} else if sum, ok := base64Digest(s); ok {
		o[field] = sum
	}
}

// dataURLDigest replaces the payload of a base64 data: URL with its digest,
// keeping the media type: data:image/png;base64,iVBOR… becomes
// data:image/png;sha256:<hex>.
func dataURLDigest(s string) (string, bool) {
	if len(s) < 5 || !strings.EqualFold(s[:5], "data:") {
		return "", false
	}
	header, payload, ok := strings.Cut(s[5:], ",")
	if !ok {
		return "", false
	}
	mediaType, found := cutSuffixFold(header, ";base64")
	if !found {
		return "", false // percent-encoded text: short, compared verbatim
	}
	sum, ok := base64Digest(payload)
	if !ok {
		return "", false
	}
	return "data:" + mediaType + ";" + sum, true
}

func cutSuffixFold(s, suffix string) (string, bool) {
	if len(s) >= len(suffix) && strings.EqualFold(s[len(s)-len(suffix):], suffix) {
		return s[:len(s)-len(suffix)], true
	}
	return s, false
}

// base64Digest returns "sha256:<hex>" of the bytes s encodes. Line breaks and
// spaces are ignored and padding is optional, as decoders that providers use
// allow. An empty or invalid payload is not a digest.
func base64Digest(s string) (string, bool) {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', ' ', '\t':
			return -1
		}
		return r
	}, s)
	s = strings.TrimRight(s, "=")
	if s == "" {
		return "", false
	}
	decoded, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(decoded)
	return "sha256:" + hex.EncodeToString(sum[:]), true
}

// signingScheme lists the query parameters one URL signing scheme adds. They
// are removed only when every trigger parameter is present.
type signingScheme struct {
	trigger []string
	params  []string
	path    string // a path the URL must contain, when the names are generic
}

// Parameter names are compared case-insensitively.
var signingSchemes = []signingScheme{
	// AWS Signature Version 4: S3 and S3-compatible stores (R2, MinIO, B2, Spaces…).
	{trigger: []string{"x-amz-signature"}, params: []string{"x-amz-algorithm", "x-amz-credential", "x-amz-date", "x-amz-expires", "x-amz-signedheaders", "x-amz-signature", "x-amz-security-token"}},
	// AWS Signature Version 2.
	{trigger: []string{"awsaccesskeyid", "signature"}, params: []string{"awsaccesskeyid", "signature", "expires", "x-amz-security-token"}},
	// Google Cloud Storage V4 and V2.
	{trigger: []string{"x-goog-signature"}, params: []string{"x-goog-algorithm", "x-goog-credential", "x-goog-date", "x-goog-expires", "x-goog-signedheaders", "x-goog-signature"}},
	{trigger: []string{"googleaccessid", "signature"}, params: []string{"googleaccessid", "expires", "signature"}},
	// Azure shared access signatures. Response header overrides (rsc*) stay.
	{trigger: []string{"sig", "sv"}, params: []string{"sig", "se", "st", "sv", "sp", "spr", "sr", "ss", "srt", "si", "sip", "ses", "sdd", "skoid", "sktid", "skt", "ske", "sks", "skv", "saoid", "suoid", "scid"}},
	// CloudFront signed URLs.
	{trigger: []string{"key-pair-id", "signature"}, params: []string{"expires", "signature", "key-pair-id", "policy"}},
	// Supabase Storage signed URLs.
	{trigger: []string{"token"}, params: []string{"token"}, path: "/storage/v1/object/sign/"},
}

// StripSignature removes URL signing parameters from an http(s) URL and
// returns any other string unchanged. The remaining parameters keep their
// order and exact encoding, and the fragment is kept.
func StripSignature(s string) string {
	lower := strings.ToLower(s)
	if !strings.HasPrefix(lower, "https://") && !strings.HasPrefix(lower, "http://") {
		return s
	}
	rest, fragment, hasFragment := strings.Cut(s, "#")
	base, query, hasQuery := strings.Cut(rest, "?")
	if !hasQuery || query == "" {
		return s
	}
	parts := strings.Split(query, "&")
	names := make([]string, len(parts))
	present := map[string]bool{}
	for i, part := range parts {
		name, _, _ := strings.Cut(part, "=")
		if unescaped, err := url.QueryUnescape(name); err == nil {
			name = unescaped
		}
		names[i] = strings.ToLower(name)
		present[names[i]] = true
	}
	remove := map[string]bool{}
	for _, scheme := range signingSchemes {
		if scheme.path != "" && !strings.Contains(base, scheme.path) {
			continue
		}
		triggered := true
		for _, name := range scheme.trigger {
			triggered = triggered && present[name]
		}
		if triggered {
			for _, name := range scheme.params {
				remove[name] = true
			}
		}
	}
	if len(remove) == 0 {
		return s
	}
	kept := parts[:0]
	for i, part := range parts {
		if !remove[names[i]] {
			kept = append(kept, part)
		}
	}
	out := base
	if len(kept) > 0 {
		out += "?" + strings.Join(kept, "&")
	}
	if hasFragment {
		out += "#" + fragment
	}
	return out
}
