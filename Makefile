.PHONY: build frontend test clean
frontend:
	cd web && npm ci && npm run build
build: frontend
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o bin/replay-proxy ./cmd/replay-proxy
test:
	CGO_ENABLED=1 go test -race ./...
clean:
	rm -rf bin web/.next
