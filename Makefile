SHELL := /bin/bash
SWIFTC ?= $(shell command -v swiftc 2>/dev/null || test ! -x "$$HOME/.local/share/swiftly/bin/swiftc" || echo "$$HOME/.local/share/swiftly/bin/swiftc")
GO ?= go
CARGO ?= cargo
RUST_TARGET_DIR := $(CURDIR)/build/rust-target
AUDIO_SOURCE ?=
export AUDIO_SOURCE
AUDIO_OUTPUT ?=
export AUDIO_OUTPUT
PACKAGES := libpipewire-0.3 libavcodec libavutil libswscale
NATIVE_LIBS := $(shell pkg-config --libs $(PACKAGES)) -lpthread -lm
SWIFT_IMPORT_FLAGS := -I core/include $(foreach option,$(shell pkg-config --cflags $(PACKAGES)),-Xcc $(option))
SWIFT_FLAGS := -swift-version 6 -O -g $(SWIFT_IMPORT_FLAGS)
SWIFT_SOURCES := core/swift/Engine.swift
CORE_TEST_SOURCES := tests/legacy/swift/Audio.swift tests/legacy/swift/AudioRing.swift tests/legacy/swift/FramePool.swift tests/legacy/swift/PixelConverter.swift tests/legacy/swift/Video.swift
RUST_SOURCES := $(shell rg --files core/rust -g '*.rs' -g 'Cargo.toml')
RUST_ASAN := RUSTC_BOOTSTRAP=1 RUSTFLAGS='-Zsanitizer=address -C link-arg=-fuse-ld=lld' CARGO_TARGET_DIR='$(CURDIR)/build/rust-asan-target' ASAN_OPTIONS='detect_leaks=1:halt_on_error=1:abort_on_error=1' LSAN_OPTIONS='exitcode=23:report_objects=1'
HEADERS := $(wildcard core/include/*)

.PHONY: all core run install-desktop test test-race test-sanitize test-thread-sanitize test-audio-integration check-dependencies clean
all: build/mygoframe
core: build/libmygoframe_core.so

check-dependencies:
	@test -n "$(SWIFTC)" || { echo 'Swift 6.0 or newer is required. Set SWIFTC=/path/to/swiftc.'; exit 1; }
	@"$(SWIFTC)" --version
	@pkg-config --exists $(PACKAGES)
	@$(GO) version
	@$(CARGO) --version
	@bindgen --version

build:
	mkdir -p build

build/libmygoframe_media.so: $(RUST_SOURCES) $(HEADERS) Makefile | build
	CARGO_TARGET_DIR="$(RUST_TARGET_DIR)" $(CARGO) build --manifest-path core/rust/Cargo.toml --release
	cp "$(RUST_TARGET_DIR)/release/libmygoframe_media.so" $@

build/libmygoframe_core.so: build/libmygoframe_media.so $(SWIFT_SOURCES) $(HEADERS) Makefile | build
	"$(SWIFTC)" $(SWIFT_FLAGS) -emit-library -module-name MyGOFrameCore $(SWIFT_SOURCES) -L build -lmygoframe_media -Xlinker -rpath -Xlinker '$$ORIGIN' -o $@

build/mygoframe: build/libmygoframe_core.so $(shell rg --files cmd internal -g '*.go' -g '*.png') go.mod go.sum
	CGO_ENABLED=1 $(GO) build -trimpath -o $@ ./cmd/mygoframe

run: all
	./build/mygoframe

install-desktop: all
	$(GO) run ./tools/install-desktop

build/pool-tests: tests/legacy/swift/FramePool.swift tests/pool_tests.swift | build
	"$(SWIFTC)" -swift-version 6 -O -g $^ -o $@

build/video-tests: $(CORE_TEST_SOURCES) tests/video_tests.swift $(HEADERS) Makefile | build
	"$(SWIFTC)" $(SWIFT_FLAGS) $(CORE_TEST_SOURCES) tests/video_tests.swift $(NATIVE_LIBS) -o $@

build/audio-tests: $(CORE_TEST_SOURCES) tests/audio_tests.swift $(HEADERS) Makefile | build
	"$(SWIFTC)" $(SWIFT_FLAGS) $(CORE_TEST_SOURCES) tests/audio_tests.swift $(NATIVE_LIBS) -o $@

test: all build/pool-tests build/video-tests build/audio-tests
	CARGO_TARGET_DIR="$(RUST_TARGET_DIR)" $(CARGO) test --manifest-path core/rust/Cargo.toml
	./build/pool-tests
	./build/video-tests
	./build/audio-tests
	LD_LIBRARY_PATH="$(CURDIR)/build:$${LD_LIBRARY_PATH:-}" CGO_ENABLED=1 $(GO) test ./...

test-race: all
	LD_LIBRARY_PATH="$(CURDIR)/build:$${LD_LIBRARY_PATH:-}" CGO_ENABLED=1 $(GO) test -race ./...
	$(GO) vet ./...

build/video-sanitize: $(CORE_TEST_SOURCES) tests/video_tests.swift $(HEADERS) Makefile | build
	"$(SWIFTC)" $(SWIFT_FLAGS) -sanitize=address $(CORE_TEST_SOURCES) tests/video_tests.swift $(NATIVE_LIBS) -o build/video-sanitize

build/audio-sanitize: $(CORE_TEST_SOURCES) tests/audio_tests.swift $(HEADERS) Makefile | build
	"$(SWIFTC)" $(SWIFT_FLAGS) -sanitize=address $(CORE_TEST_SOURCES) tests/audio_tests.swift $(NATIVE_LIBS) -o build/audio-sanitize

test-sanitize: build/video-sanitize build/audio-sanitize
	./build/video-sanitize
	./build/audio-sanitize
	$(RUST_ASAN) $(CARGO) test --manifest-path core/rust/Cargo.toml --offline --lib

test-audio-integration: build/audio-sanitize
	./build/audio-sanitize --integration
	@if [[ -n "$(AUDIO_SOURCE)" ]]; then \
		$(RUST_ASAN) $(CARGO) test --manifest-path core/rust/Cargo.toml --offline --lib audio::tests::pipewire_real_device_lifecycle -- --ignored --exact --nocapture --test-threads=1; \
	else \
		echo 'Rust audio hardware test skipped: set AUDIO_SOURCE to run it.'; \
	fi

test-thread-sanitize: | build
	"$(SWIFTC)" $(SWIFT_FLAGS) -sanitize=thread $(CORE_TEST_SOURCES) tests/audio_tests.swift $(NATIVE_LIBS) -o build/audio-thread-sanitize
	./build/audio-thread-sanitize

clean:
	rm -rf build
