# terminatr build.
#
# tm links libghostty-vt statically through cgo (go.mitchellh.com/libghostty).
# `make` fetches Ghostty at a pinned commit, builds libghostty-vt with Zig
# into .build/, and points pkg-config at it. If no Zig 0.16 is on PATH, the
# pinned Zig is downloaded into .build/ too. Nothing is installed
# system-wide.
#
#   make            build bin/tm
#   make run        build, start the server and open the dashboard
#   make test       go test ./... (unit + integration + fuzz seed corpora; every PR)
#   make test-race  the same with -race (main and weekly in CI)
#   make e2e        every end-to-end scenario (internal/e2e) against bin/tm
#   make e2e-smoke  the core scenarios (every PR)
#   make e2e-smoke-race  the same with tm built with -race (weekly in CI)
#   make test-claude  the scenarios against the real claude (needs a login; costs cents)
#   make test-codex   the scenarios against the real codex (needs a ChatGPT login)
#   make fuzz       run every fuzz target for FUZZTIME each (weekly)
#   make vet        go vet ./... and staticcheck ./...
#   make vet-cross  go vet ./... for windows, darwin and freebsd, without cgo (no libghostty)
#   make toolchain  check Go, Zig, pkg-config and git
#   make env        print the PKG_CONFIG_PATH export, for gopls or a plain `go build`
#   make release-snapshot  cross-build the release archives into dist/ (no tag, no upload)
#   make clean      remove bin/      make distclean   also remove .build/

# Must match the commit go.mitchellh.com/libghostty is developed against
# (its CMakeLists.txt GIT_TAG). Bump both together; see CONTRIBUTING.md.
GHOSTTY_REV  ?= 33da6848d63b3bba2b4f31ab1531d618f2795192
GHOSTTY_REPO ?= https://github.com/ghostty-org/ghostty.git
# Zig builds for the host CPU by default, which gives a library that can
# crash with SIGILL on older CPUs (seen in CI: AVX-512 code restored from
# cache onto a runner without it). Build for the baseline of the target
# architecture instead; override with e.g. GHOSTTY_CPU=native for local use.
GHOSTTY_CPU  ?= baseline
# Zig target triple for cross builds (releases, scripts/release/): empty
# builds for the host. Each target gets its own output directory.
GHOSTTY_TARGET ?=
GO           ?= go
ZIG_MIN      := 0.16.0
# staticcheck, pinned; `make vet` runs it after go vet.
STATICCHECK  ?= honnef.co/go/tools/cmd/staticcheck@v0.8.1

# A linked worktree (a terminatr thread's) has no .build of its own: it
# uses the main checkout's when that has one, instead of fetching and
# building Ghostty again. make BUILD=/path overrides.
MAIN_BUILD  := $(shell c=$$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null) && \
	[ "$$c" != "$$(git rev-parse --path-format=absolute --git-dir)" ] && \
	[ -d "$${c%/.git}/.build" ] && echo "$${c%/.git}/.build")
BUILD       := $(abspath $(if $(wildcard .build),.build,$(or $(MAIN_BUILD),.build)))

# Zig. The pinned Ghostty commit builds with Zig 0.16 (build.zig.zon), and
# Zig breaks its build API between minor releases, so a zig on PATH is used
# only if it is 0.16.x. Otherwise the pinned release is downloaded into
# .build/ and checked against its published sha256. ZIG=/path/to/zig wins.
ZIG_VERSION := 0.16.0
ZIG_OS      := $(shell uname -s | sed 's/Darwin/macos/;s/Linux/linux/')
ZIG_ARCH    := $(shell uname -m | sed 's/arm64/aarch64/;s/amd64/x86_64/')
ZIG_PKG     := zig-$(ZIG_ARCH)-$(ZIG_OS)-$(ZIG_VERSION)
ZIG_LOCAL   := $(BUILD)/$(ZIG_PKG)/zig
ZIG_SHA256_aarch64-macos := b23d70deaa879b5c2d486ed3316f7eaa53e84acf6fc9cc747de152450d401489
ZIG_SHA256_x86_64-macos  := 0387557ed1877bc6a2e1802c8391953baddba76081876301c522f52977b52ba7
ZIG_SHA256_aarch64-linux := ea4b09bfb22ec6f6c6ceac57ab63efb6b46e17ab08d21f69f3a48b38e1534f17
ZIG_SHA256_x86_64-linux  := 70e49664a74374b48b51e6f3fdfbf437f6395d42509050588bd49abe52ba3d00
ifeq ($(origin ZIG),undefined)
  ZIG_ON_PATH := $(shell command -v zig 2>/dev/null)
  ZIG := $(if $(and $(ZIG_ON_PATH),$(filter 0.16.%,$(shell $(ZIG_ON_PATH) version 2>/dev/null))),$(ZIG_ON_PATH),$(ZIG_LOCAL))
endif
GHOSTTY_SRC := $(BUILD)/ghostty-src
GHOSTTY_OUT := $(BUILD)/ghostty-$(shell echo $(GHOSTTY_REV) | cut -c1-12)-$(GHOSTTY_CPU)$(if $(GHOSTTY_TARGET),-$(GHOSTTY_TARGET))
STAMP       := $(GHOSTTY_OUT)/.built
# READY: the build's pkg-config files name their prefix relative to
# themselves (${pcfiledir}), so a moved or renamed checkout still links
# its own library instead of the one at the path it was built at.
READY       := $(GHOSTTY_OUT)/.relocatable

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/theclifmeister/terminatr/internal/version.Version=$(VERSION) \
           -X github.com/theclifmeister/terminatr/internal/version.LibGhostty=$(shell echo $(GHOSTTY_REV) | cut -c1-12)

export PKG_CONFIG_PATH := $(GHOSTTY_OUT)/share/pkgconfig$(if $(PKG_CONFIG_PATH),:$(PKG_CONFIG_PATH))
export CGO_ENABLED := 1
# Go's build cache doesn't key on pkg-config output, so a cached cgo package
# would keep linking the previous library path. CGO_CFLAGS is part of the
# key: naming the library build here forces a rebuild when it changes. The
# full path, not just the build name: every worktree has its own .build/,
# and a package cached in another (since removed) worktree would link that
# worktree's library.
CGO_CFLAGS ?= -O2 -g
export CGO_CFLAGS += -DTM_LIBGHOSTTY=$(GHOSTTY_OUT)

.PHONY: all build run test test-race test-claude test-codex e2e e2e-smoke e2e-smoke-race fuzz vet vet-cross ghostty toolchain env clean distclean zig-path release-ghostty release-snapshot release

all: build

build: $(READY)
	$(GO) build -ldflags '$(LDFLAGS)' -o bin/tm ./cmd/tm

# `make run` is the way to try terminatr: scripts/run.sh starts the server
# and opens the dashboard. RUN_ARGS starts a session with that command
# first, e.g. make run RUN_ARGS="top".
run: build
	@TM=bin/tm ./scripts/run.sh $(RUN_ARGS)

test: $(READY)
	$(GO) test ./...

test-race: $(READY)
	$(GO) test -race ./...

FUZZTIME ?= 5m
# -fuzzminimizetime: Go minimizes each new interesting input for up to 60s
# by default, as long as -fuzztime itself. One found near the end outlives
# the deadline and the run fails "context deadline exceeded" with execs at
# 0/sec (FuzzJoinDecode, weekly run 37904293277; no input is slow: Join
# and Decode take well under a millisecond on any input tried).
FUZZMINTIME ?= 3s
fuzz: $(READY)
	@set -e; for pkg in $$($(GO) list ./...); do \
		for t in $$($(GO) test -list '^Fuzz' $$pkg | grep '^Fuzz' || true); do \
			echo "== $$pkg $$t"; \
			$(GO) test $$pkg -run '^$$' -fuzz "^$$t\$$" -fuzztime $(FUZZTIME) -fuzzminimizetime $(FUZZMINTIME); \
		done; \
	done

# End-to-end scenarios (internal/e2e, docs/SPEC.md §16.2). The harness
# builds tm itself; E2E_FLAGS=-update rewrites golden screens. The smoke
# set is every scenario named TestSmoke*. E2E_RACE=1 builds tm and the
# test with -race; the harness then fails a scenario whose tm printed
# "WARNING: DATA RACE". A race-built tm takes about a second to start, and
# every agent hook starts one, so PRs and main run the smoke set without
# -race (`go test -race ./...` still covers the code on main); only the
# weekly workflow runs e2e-smoke-race. E2E_SHARD=I/N runs only shard I of N of the smoke set
# (scripts/e2e-shard.sh), as CI does.
E2E_RACE ?=
E2E_SHARD ?=
E2E_GOFLAGS := $(if $(filter 1,$(E2E_RACE)),-race)

e2e: $(READY)
	E2E=1 E2E_RACE=$(E2E_RACE) $(GO) test $(E2E_GOFLAGS) -count=1 ./internal/e2e $(E2E_FLAGS)

e2e-smoke: $(READY)
	@run='^TestSmoke'; \
	$(if $(E2E_SHARD),run=$$(GO=$(GO) scripts/e2e-shard.sh "$$run" $(E2E_SHARD)) || exit 1; echo "shard $(E2E_SHARD): -run '$$run'";) \
	E2E=1 E2E_RACE=$(E2E_RACE) $(GO) test $(E2E_GOFLAGS) -count=1 -run "$$run" ./internal/e2e $(E2E_FLAGS)

e2e-smoke-race:
	$(MAKE) e2e-smoke E2E_RACE=1

# The real-Claude suite (docs/SPEC.md §16.4): build tag realclaude, the
# claude on PATH, Haiku. On demand, and on a machine with a login.
test-claude: $(READY)
	E2E=1 $(GO) test -tags realclaude -count=1 -timeout 30m -run '^TestReal' -v ./internal/e2e $(E2E_FLAGS)

# The real-Codex suite (T105): build tag realcodex, the codex on PATH
# logged in with ChatGPT, gpt-6-luna. On demand only, never in CI or the
# weekly job (user, 2026-10-08). CODEX_HOME=<dir> keeps its threads out
# of ~/.codex; that dir needs its own `codex login` once.
test-codex: $(READY)
	E2E=1 $(GO) test -tags realcodex -count=1 -timeout 30m -run '^TestRealCodex' -v ./internal/e2e $(E2E_FLAGS)

vet: $(READY)
	$(GO) vet ./...
	$(GO) run $(STATICCHECK) ./...

# Type-checks the tree for other OSes in seconds, without cgo: internal/emu
# is then its stub (emu_nocgo.go). Windows and FreeBSD catch Unix-only and
# darwin/linux-only code outside a per-OS file (internal/plat).
VET_GOOS ?= windows darwin freebsd
vet-cross:
	@for goos in $(VET_GOOS); do echo "GOOS=$$goos go vet ./..."; CGO_ENABLED=0 GOOS=$$goos $(GO) vet ./... || exit 1; done

ghostty: $(READY)

$(STAMP): | toolchain $(if $(filter $(ZIG_LOCAL),$(ZIG)),$(ZIG_LOCAL))
	@mkdir -p $(GHOSTTY_SRC)
	@if [ ! -d $(GHOSTTY_SRC)/.git ]; then \
		git -C $(GHOSTTY_SRC) init -q && \
		git -C $(GHOSTTY_SRC) remote add origin $(GHOSTTY_REPO); \
	fi
	git -C $(GHOSTTY_SRC) fetch -q --depth 1 origin $(GHOSTTY_REV)
	git -C $(GHOSTTY_SRC) checkout -q --force FETCH_HEAD
	cd $(GHOSTTY_SRC) && $(ZIG) build -Demit-lib-vt -Demit-xcframework=false \
		-Doptimize=ReleaseFast -Dcpu=$(GHOSTTY_CPU) $(if $(GHOSTTY_TARGET),-Dtarget=$(GHOSTTY_TARGET)) \
		--prefix $(GHOSTTY_OUT)
	@test -f $(GHOSTTY_OUT)/share/pkgconfig/libghostty-vt-static.pc || \
		{ echo "libghostty-vt build produced no pkg-config file" >&2; exit 1; }
	@touch $@

# Windows builds name their archive ghostty-vt-static.lib, which cgo's
# linker-flag allowlist rejects (it knows .a, not .lib): publish it as
# libghostty-vt.a, the name the other targets have, and point the .pc at it.
$(READY): $(STAMP)
	@if [ -f $(GHOSTTY_OUT)/lib/ghostty-vt-static.lib ]; then \
		cp $(GHOSTTY_OUT)/lib/ghostty-vt-static.lib $(GHOSTTY_OUT)/lib/libghostty-vt.a && \
		for pc in $(GHOSTTY_OUT)/share/pkgconfig/*.pc; do \
			sed 's|ghostty-vt-static\.lib|libghostty-vt.a|' "$$pc" > "$$pc.tmp" && mv "$$pc.tmp" "$$pc" || exit 1; \
		done; \
	fi
	@for pc in $(GHOSTTY_OUT)/share/pkgconfig/*.pc; do \
		sed 's|^prefix=.*|prefix=$${pcfiledir}/../..|' "$$pc" > "$$pc.tmp" && mv "$$pc.tmp" "$$pc" || \
		{ echo "can't rewrite $$pc: run make once where $(BUILD) is writable" >&2; exit 1; }; \
	done
	@touch $@

$(ZIG_LOCAL):
	@test -n "$(ZIG_SHA256_$(ZIG_ARCH)-$(ZIG_OS))" || \
		{ echo "no pinned Zig for $(ZIG_ARCH)-$(ZIG_OS); install Zig $(ZIG_VERSION) and pass ZIG=/path/to/zig" >&2; exit 1; }
	@mkdir -p $(BUILD)
	@echo "fetching $(ZIG_PKG) into .build/"
	curl -fsSL --retry 3 -o $(BUILD)/$(ZIG_PKG).tar.xz https://ziglang.org/download/$(ZIG_VERSION)/$(ZIG_PKG).tar.xz
	@got=$$( (command -v sha256sum >/dev/null && sha256sum $(BUILD)/$(ZIG_PKG).tar.xz || shasum -a 256 $(BUILD)/$(ZIG_PKG).tar.xz) | cut -d' ' -f1); \
	if [ "$$got" != "$(ZIG_SHA256_$(ZIG_ARCH)-$(ZIG_OS))" ]; then \
		echo "$(ZIG_PKG).tar.xz: sha256 $$got does not match the pinned checksum" >&2; rm -f $(BUILD)/$(ZIG_PKG).tar.xz; exit 1; \
	fi
	tar -xJf $(BUILD)/$(ZIG_PKG).tar.xz -C $(BUILD)
	@rm -f $(BUILD)/$(ZIG_PKG).tar.xz
	@touch $@

toolchain:
	@command -v $(GO) >/dev/null || { echo "missing: go (see CONTRIBUTING.md)" >&2; exit 1; }
	@command -v git >/dev/null || { echo "missing: git" >&2; exit 1; }
	@command -v pkg-config >/dev/null || { echo "missing: pkg-config (brew install pkgconf / apt install pkg-config)" >&2; exit 1; }
	@command -v cc >/dev/null || { echo "missing: a C compiler (xcode-select --install / apt install build-essential)" >&2; exit 1; }
	@if [ "$(ZIG)" = "$(ZIG_LOCAL)" ]; then \
		if [ -x "$(ZIG_LOCAL)" ]; then :; \
		else command -v curl >/dev/null || { echo "missing: curl (to fetch Zig $(ZIG_VERSION))" >&2; exit 1; }; \
		echo "zig: no Zig 0.16 on PATH; $(ZIG_PKG) will be fetched into .build/"; fi; \
		exit 0; \
	fi; \
	command -v $(ZIG) >/dev/null || { echo "missing: $(ZIG)" >&2; exit 1; }; \
	v=$$($(ZIG) version); \
	if [ "$$(printf '%s\n%s\n' "$(ZIG_MIN)" "$$v" | sort -V | head -n1)" != "$(ZIG_MIN)" ]; then \
		echo "zig $$v is too old; need >= $(ZIG_MIN)" >&2; exit 1; \
	fi

env:
	@echo 'export PKG_CONFIG_PATH=$(PKG_CONFIG_PATH)'
	@echo 'export CGO_CFLAGS="$(CGO_CFLAGS)"'

# Releases (docs/OPERATIONS.md): goreleaser cross-compiles tm with zig cc
# for darwin/linux x amd64/arm64, against one libghostty-vt build per
# target in .build/release/ (scripts/release/). `make release-snapshot`
# builds the archives into dist/ without a tag; `make release` is what
# the tag-triggered release workflow runs.
GORELEASER_VERSION ?= v2.18.2
GORELEASER ?= $(or $(shell command -v goreleaser 2>/dev/null),$(GO) run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION))
RELEASE_ENV = TM_ROOT=$(CURDIR) TM_GHOSTTY_REV=$(shell echo $(GHOSTTY_REV) | cut -c1-12) TM_GHOSTTY_CPU=$(GHOSTTY_CPU)

zig-path: | toolchain $(if $(filter $(ZIG_LOCAL),$(ZIG)),$(ZIG_LOCAL))
	@echo $(ZIG)

release-ghostty:
	./scripts/release/ghostty.sh
	./scripts/release/conpty.sh

release-snapshot: release-ghostty
	$(RELEASE_ENV) $(GORELEASER) release --snapshot --clean

release: release-ghostty
	$(RELEASE_ENV) $(GORELEASER) release --clean

clean:
	rm -rf bin

distclean: clean
	rm -rf $(BUILD) dist
