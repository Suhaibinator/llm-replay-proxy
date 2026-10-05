package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestHistoryLinksTheServedRevision(t *testing.T) {
	ctx := context.Background()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return response(`{"id":"c1","model":"demo-1","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`, "application/json"), nil
	})}
	db := openTestStore(t, "auto")
	h := New(db, Config{Client: client, Upstreams: map[string]Upstream{"/v1/chat/completions": {URL: "https://upstream.invalid"}}})
	body := `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`
	for range 3 {
		if w := perform(h, "/v1/chat/completions", body); w.Code != 200 {
			t.Fatalf("status %d %s", w.Code, w.Body.String())
		}
	}
	s, _ := db.Settings(ctx)
	now := time.Now()
	in, err := db.Insights(ctx, s.ActiveCollectionID, now.Add(-time.Hour), now.Add(time.Hour), "")
	if err != nil {
		t.Fatal(err)
	}
	// One recorded row and two hits, each attributed to the revision served.
	if in.Totals.Recorded != 1 || in.Totals.Hits != 2 || in.Totals.UpstreamTokens.Input != 7 || in.Totals.ReplayedTokens.Input != 14 || in.Totals.ReplayedTokens.Output != 6 {
		t.Fatalf("totals %+v", in.Totals)
	}
	if len(in.Models) != 1 || in.Models[0].Model != "demo-1" {
		t.Fatalf("models %+v", in.Models)
	}
}
