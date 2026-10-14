# The whisper.cpp Go bindings are cgo-based: they need libwhisper.a (built
# here via cmake) plus include/library paths exported to every recipe.

WHISPER := third_party/whisper.cpp
BUILD   := $(WHISPER)/build_go
LIB     := $(BUILD)/src/libwhisper.a

export C_INCLUDE_PATH            := $(CURDIR)/$(WHISPER)/include:$(CURDIR)/$(WHISPER)/ggml/include
export LIBRARY_PATH              := $(CURDIR)/$(BUILD)/src:$(CURDIR)/$(BUILD)/ggml/src
export GGML_METAL_PATH_RESOURCES := $(CURDIR)/$(WHISPER)

ifeq ($(shell uname -s),Darwin)
export LIBRARY_PATH  := $(LIBRARY_PATH):$(CURDIR)/$(BUILD)/ggml/src/ggml-blas:$(CURDIR)/$(BUILD)/ggml/src/ggml-metal
export CGO_LDFLAGS   := -framework Foundation -framework Metal -framework MetalKit
endif

.DEFAULT_GOAL := build

.PHONY: build lib env clean hooks generate androidtv-apk

# eval "$(make env)" once per shell, then plain `go run ./cmd/castor` / `go build` work.
env:
	@echo 'export C_INCLUDE_PATH="$(C_INCLUDE_PATH)"'
	@echo 'export LIBRARY_PATH="$(LIBRARY_PATH)"'
	@echo 'export GGML_METAL_PATH_RESOURCES="$(GGML_METAL_PATH_RESOURCES)"'
	@echo 'export CGO_LDFLAGS="$(CGO_LDFLAGS)"'

build: lib
	go build -o castor ./cmd/castor

# The cgo library alone, which CI links its tests and lint runs against.
lib: $(LIB)

clean:
	rm -rf $(BUILD) castor

# Regenerate gen/ from proto/; buf runs the protoc plugins pinned as go tools.
generate:
	buf generate

# Rebuild the committed Android TV player; needs ANDROID_HOME and a JDK 17+. Bump its VERSION with any change.
androidtv-apk:
	services/apiserver/internal/device/androidtv/receiver/build.sh

# Enable the Conventional Commits commit-msg hook (git + bash only, no installs).
hooks:
	git config core.hooksPath .githooks

$(LIB):
	cmake -S $(WHISPER) -B $(BUILD) -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_EXAMPLES=OFF \
		-DCMAKE_C_FLAGS="-I$(CURDIR)/$(WHISPER)/ggml/include"
	cmake --build $(BUILD) --target whisper -j
