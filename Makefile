.PHONY: build test release clean
build:
	go build -o bin/vk-manager ./cmd/manager
	go build -o bin/vk-agent ./cmd/agent

test:
	go vet ./... && go test ./...

# Static, dependency-free binaries for common targets.
release:
	@mkdir -p dist
	@for t in linux/amd64 linux/arm64 darwin/arm64 darwin/amd64 windows/amd64; do \
	  os=$${t%/*}; arch=$${t#*/}; ext=""; [ $$os = windows ] && ext=".exe"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="-s -w" -o dist/vk-agent-$$os-$$arch$$ext ./cmd/agent || exit 1; \
	done
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/vk-manager-linux-amd64 ./cmd/manager
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/vk-manager-linux-arm64 ./cmd/manager

clean:
	rm -rf bin dist
