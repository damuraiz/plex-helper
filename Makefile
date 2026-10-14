GO ?= go
PYTHON ?= python3
VERSION ?= 0.1.0-2
export GOTOOLCHAIN := local
export SOURCE_DATE_EPOCH ?= 1791936000

.PHONY: test build spk package-test clean
test:
	$(GO) test ./...

build:
	mkdir -p dist
	CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o dist/plex-helper-local ./cmd/plex-helper

spk:
	$(PYTHON) scripts/build-spk.py --go "$(GO)" --version "$(VERSION)"

package-test:
	$(PYTHON) scripts/test-package.py --go "$(GO)"

clean:
	rm -rf dist
