VERSION ?= $(patsubst v%,%,$(shell git describe --tags --always --dirty))
TAGS    := $(shell cat build-tags.txt)
TARGETS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64
LINT    := github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2

# dist builds beam and the test kit, beamtest-* (docs/10), for each target.
dist:
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		for b in "beam:$(TAGS)" "beamtest:$(TAGS),beamtest"; do \
			name=$${b%%:*}; echo "dist/$$name-$$os-$$arch"; \
			GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -buildvcs=false -tags "$${b#*:}" \
				-ldflags "-s -w -X main.version=$(VERSION)" -o dist/$$name-$$os-$$arch ./cmd/beam || exit 1; \
		done; \
	done

test:
	go test -race -tags "$(TAGS),beamtest" -coverpkg=./... -coverprofile=cover.out ./...

crap: test
	go run ./tools/crap cover.out

lint:
	go run $(LINT) run --build-tags "$(TAGS),beamtest"

.PHONY: dist test crap lint
