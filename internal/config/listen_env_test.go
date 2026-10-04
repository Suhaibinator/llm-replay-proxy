package config

import (
	"context"
	"testing"
)

func TestEmptyListenEnvironmentUsesDefault(t *testing.T) {
	t.Setenv("REPLAY_LISTEN", "")
	c, err := LoadWithOverrides(context.Background(), "", Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:8080" {
		t.Fatalf("listen = %q", c.Listen)
	}
}
