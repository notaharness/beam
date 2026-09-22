VERSION ?= $(shell git describe --tags --always --dirty)
TAGS    := $(shell cat build-tags.txt)
TARGETS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64
LINT    := github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2

dist:
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; echo "dist/beam-$$os-$$arch"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -buildvcs=false -tags "$(TAGS)" \
			-ldflags "-s -w -X main.version=$(VERSION)" -o dist/beam-$$os-$$arch ./cmd/beam || exit 1; \
	done

test:
	go test -race -tags "$(TAGS)" -coverpkg=./... -coverprofile=cover.out ./...

crap: test
	go run ./tools/crap cover.out

lint:
	go run $(LINT) run --build-tags "$(TAGS)"

.PHONY: dist test crap lint
