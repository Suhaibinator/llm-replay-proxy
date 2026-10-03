package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/local/llm-replay-proxy/integration/rehearsal"
)

func main() {
	baseURL := flag.String("proxy", "http://127.0.0.1:8080", "replay proxy base URL")
	apiName := flag.String("api", "responses", "chat, responses, or anthropic")
	model := flag.String("model", "demo-model", "provider model ID")
	workflow := flag.String("workflow", "text", "text or tools")
	prompt := flag.String("prompt", "Reply with the word ready.", "prompt sent through Go Common")
	timeout := flag.Duration("timeout", 30*time.Second, "whole rehearsal timeout")
	flag.Parse()

	api, err := rehearsal.API(*apiName)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	client, err := rehearsal.New(ctx, *baseURL, *model)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	var answer string
	switch *workflow {
	case "text":
		answer, err = client.Text(ctx, api, *prompt)
	case "tools":
		answer, err = client.ToolRoundTrip(ctx, api, *prompt)
	default:
		log.Fatalf("unknown workflow %q (use text or tools)", *workflow)
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(answer)
}
