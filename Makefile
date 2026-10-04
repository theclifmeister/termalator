# termalator build.
#
# tm links libghostty-vt statically through cgo (go.mitchellh.com/libghostty).
# `make` fetches Ghostty at a pinned commit, builds libghostty-vt with Zig
# into .build/, and points pkg-config at it. Nothing is installed system-wide.
#
#   make            build bin/tm
#   make test       go test ./...
#   make vet        go vet ./...
#   make toolchain  check Go, Zig, pkg-config and git
#   make env        print the PKG_CONFIG_PATH export, for gopls or a plain `go build`
#   make clean      remove bin/      make distclean   also remove .build/

# Must match the commit go.mitchellh.com/libghostty is developed against
# (its CMakeLists.txt GIT_TAG). Bump both together; see README.md.
GHOSTTY_REV  ?= 33da6848d63b3bba2b4f31ab1531d618f2795192
GHOSTTY_REPO ?= https://github.com/ghostty-org/ghostty.git
ZIG          ?= zig
GO           ?= go
ZIG_MIN      := 0.16.0

BUILD       := $(abspath .build)
GHOSTTY_SRC := $(BUILD)/ghostty-src
GHOSTTY_OUT := $(BUILD)/ghostty-$(shell echo $(GHOSTTY_REV) | cut -c1-12)
STAMP       := $(GHOSTTY_OUT)/.built

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/theclifmeister/termalator/internal/version.Version=$(VERSION)

export PKG_CONFIG_PATH := $(GHOSTTY_OUT)/share/pkgconfig$(if $(PKG_CONFIG_PATH),:$(PKG_CONFIG_PATH))
export CGO_ENABLED := 1

.PHONY: all build test vet ghostty toolchain env clean distclean

all: build

build: $(STAMP)
	$(GO) build -ldflags '$(LDFLAGS)' -o bin/tm ./cmd/tm

test: $(STAMP)
	$(GO) test ./...

vet: $(STAMP)
	$(GO) vet ./...

ghostty: $(STAMP)

$(STAMP): | toolchain
	@mkdir -p $(GHOSTTY_SRC)
	@if [ ! -d $(GHOSTTY_SRC)/.git ]; then \
		git -C $(GHOSTTY_SRC) init -q && \
		git -C $(GHOSTTY_SRC) remote add origin $(GHOSTTY_REPO); \
	fi
	git -C $(GHOSTTY_SRC) fetch -q --depth 1 origin $(GHOSTTY_REV)
	git -C $(GHOSTTY_SRC) checkout -q --force FETCH_HEAD
	cd $(GHOSTTY_SRC) && $(ZIG) build -Demit-lib-vt -Demit-xcframework=false \
		-Doptimize=ReleaseFast --prefix $(GHOSTTY_OUT)
	@test -f $(GHOSTTY_OUT)/share/pkgconfig/libghostty-vt-static.pc || \
		{ echo "libghostty-vt build produced no pkg-config file" >&2; exit 1; }
	@touch $@

toolchain:
	@command -v $(GO) >/dev/null || { echo "missing: go (see README.md)" >&2; exit 1; }
	@command -v git >/dev/null || { echo "missing: git" >&2; exit 1; }
	@command -v pkg-config >/dev/null || { echo "missing: pkg-config (brew install pkgconf / apt install pkg-config)" >&2; exit 1; }
	@command -v $(ZIG) >/dev/null || { echo "missing: zig >= $(ZIG_MIN) (https://ziglang.org/download/)" >&2; exit 1; }
	@v=$$($(ZIG) version); \
	if [ "$$(printf '%s\n%s\n' "$(ZIG_MIN)" "$$v" | sort -V | head -n1)" != "$(ZIG_MIN)" ]; then \
		echo "zig $$v is too old; need >= $(ZIG_MIN)" >&2; exit 1; \
	fi

env:
	@echo 'export PKG_CONFIG_PATH=$(PKG_CONFIG_PATH)'

clean:
	rm -rf bin

distclean: clean
	rm -rf $(BUILD)
