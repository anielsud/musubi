.PHONY: build test vet fmt licenses eval eval-record fixtures clean frontend build-gui bundle-mac install-gui-mac recorder-app app-mac dmg

# FTS5 is compiled into the SQLite driver only with this tag.
TAGS := -tags sqlite_fts5
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# The desktop app needs a webview (WKWebView on macOS, webkit2gtk on Linux),
# so CI checks everything else; build-gui covers the app on a Mac. -e: the
# app's package doesn't load without the built frontend (frontend/dist), and
# without -e go list then lists nothing at all.
CORE := $(shell go list -e ./... | grep -v -e /cmd/musubi-gui -e /internal/gui)

APP_BUNDLE := build/darwin/Musubi.app

build:
	go build $(TAGS) -o bin/musubi ./cmd/musubi

test:
	go test $(TAGS) $(CORE)

vet:
	go vet $(TAGS) $(CORE)

fmt:
	gofmt -w cmd internal

# Fails if any dependency is under a license that would encumber the code.
# Ignored: our own module, and the sqlite-vec Go bindings, whose module ships
# no LICENSE file; the repository is Apache-2.0 and sqlite-vec itself is
# MIT OR Apache-2.0 (checked 2026-10-09).
licenses:
	go run github.com/google/go-licenses@latest check $(CORE) --disallowed_types=forbidden,restricted,unknown \
		--ignore github.com/anielsud/musubi --ignore github.com/asg017/sqlite-vec-go-bindings

# Synthetic corpus eval with the deterministic embedder and recorded model
# replies: the CI gate.
eval: build
	bin/musubi eval --corpus testdata/synthetic --embed hash --knowledge

# Re-record the model replies the synthetic eval replays (needs Ollama with
# the configured models). Run after changing a knowledge prompt.
eval-record: build
	rm -f testdata/synthetic/llm-cache/chat.json
	bin/musubi eval --corpus testdata/synthetic --embed hash --knowledge --record

# Rebuilds testdata/fixtures: the synthetic corpus's database as each
# listed release left it, which store's tests upgrade (needs sqlite3).
fixtures:
	scripts/fixtures.sh

frontend:
	cd frontend && npm install --silent && npm run build
	rm -rf cmd/musubi-gui/frontend/dist
	mkdir -p cmd/musubi-gui/frontend
	cp -r frontend/dist cmd/musubi-gui/frontend/dist

build-gui: frontend
	CGO_ENABLED=1 CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build $(TAGS),production -ldflags "-X main.version=$(VERSION)" -o bin/musubi-gui ./cmd/musubi-gui

# Builds /Applications/Musubi.app and adds it to the Dock. Musubi needs no
# camera, microphone or screen permissions, so an ad-hoc signature is enough.
# A stable local signing identity keeps the Keychain's "Always Allow" for the
# Microsoft 365 sign-in across rebuilds; an ad-hoc signature changes every
# build, so macOS would ask again each time. Tomoe's `make dev-cert-mac`
# creates this identity; without it the app is signed ad hoc as before.
CODESIGN_IDENTITY ?= $(shell security find-identity -p codesigning 2>/dev/null | grep -q '"Tomoe Dev Signing"' && echo 'Tomoe Dev Signing' || echo '-')

# Musubi.app's contents, unsigned: install-gui-mac and app-mac sign it.
bundle-mac: build build-gui
	rm -rf $(APP_BUNDLE)
	mkdir -p $(APP_BUNDLE)/Contents/MacOS $(APP_BUNDLE)/Contents/Resources build/icon.iconset
	cp bin/musubi-gui bin/musubi $(APP_BUNDLE)/Contents/MacOS/
	swift packaging/macos/icon.swift build/icon-1024.png
	@for s in 16 32 128 256 512; do \
		sips -z $$s $$s build/icon-1024.png --out build/icon.iconset/icon_$${s}x$${s}.png >/dev/null; \
		sips -z $$((s*2)) $$((s*2)) build/icon-1024.png --out build/icon.iconset/icon_$${s}x$${s}@2x.png >/dev/null; \
	done
	iconutil -c icns build/icon.iconset -o $(APP_BUNDLE)/Contents/Resources/Musubi.icns
	sed 's/__VERSION__/$(VERSION)/g' packaging/macos/Info.plist.template > $(APP_BUNDLE)/Contents/Info.plist

install-gui-mac: bundle-mac
	codesign --sign "$(CODESIGN_IDENTITY)" --force --deep $(APP_BUNDLE)
	# Replace the contents, not the folder: an account without admin rights
	# can't delete entries in /Applications, but owns the app folder it made.
	rm -rf /Applications/Musubi.app/Contents
	ditto $(APP_BUNDLE) /Applications/Musubi.app
	@if ! defaults read com.apple.dock persistent-apps | grep -q 'Musubi.app'; then \
		defaults write com.apple.dock persistent-apps -array-add '<dict><key>tile-data</key><dict><key>file-data</key><dict><key>_CFURLString</key><string>file:///Applications/Musubi.app/</string><key>_CFURLStringType</key><integer>15</integer></dict></dict></dict>'; \
		killall Dock; \
	fi
	@echo "Installed /Applications/Musubi.app ($(VERSION))"

# Musubi Recorder.app: the meeting recorder as a windowless app
# (LSUIElement) for its LaunchAgent; scripts/install-recorder.sh installs
# and loads it. Signed with the identity Tomoe's `make dev-cert-mac`
# creates, never ad hoc: macOS keeps the microphone and screen-recording
# grants only while the signature's identity stays the same.
RECORDER_APP := bin/Musubi Recorder.app
RECORDER_IDENTITY ?= Tomoe Dev Signing

recorder-app:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/musubi-recorder ./cmd/musubi-recorder
	rm -rf "$(RECORDER_APP)"
	mkdir -p "$(RECORDER_APP)/Contents/MacOS" "$(RECORDER_APP)/Contents/Frameworks"
	cp bin/musubi-recorder "$(RECORDER_APP)/Contents/MacOS/"
	# Bundle the native libraries the binary loads via @rpath (sherpa-onnx
	# and ONNX Runtime, otherwise found in the Go module cache, which only
	# exists on the build machine) and point the binary at the bundled
	# copies instead. The diarizer finds ONNX Runtime the same way
	# (internal/recorder/onnxrt).
	@set -e; dst="$(RECORDER_APP)/Contents/MacOS/musubi-recorder"; \
	for rp in $$(otool -l "$$dst" | awk '/cmd LC_RPATH/ {getline; getline; print $$2}'); do \
		for lib in $$(otool -L "$$dst" | awk '/@rpath\// {sub("@rpath/", "", $$1); print $$1}'); do \
			if [ -f "$$rp/$$lib" ]; then install -m 644 "$$rp/$$lib" "$(RECORDER_APP)/Contents/Frameworks/"; fi; \
		done; \
		install_name_tool -delete_rpath "$$rp" "$$dst"; \
	done; \
	install_name_tool -add_rpath @executable_path/../Frameworks "$$dst"
	sed 's/__VERSION__/$(VERSION)/g' packaging/macos/recorder/Info.plist.template > "$(RECORDER_APP)/Contents/Info.plist"
	codesign --sign "$(RECORDER_IDENTITY)" --force --deep "$(RECORDER_APP)"
	@echo "Built $(RECORDER_APP) ($(VERSION)); install with scripts/install-recorder.sh"

# Musubi.app for others to install: the app with the recorder inside it
# (Contents/Helpers) and the recorder's LaunchAgent template. On first
# launch Musubi installs the agent itself ([recorder] install_agent), so
# no script or checkout is needed. The recorder keeps its own signature:
# the outer app is signed without --deep. CI signs ad hoc (no Developer
# ID yet): CODESIGN_IDENTITY=- RECORDER_IDENTITY=-.
DIST_APP := build/dist/Musubi.app
DMG := build/Musubi-$(VERSION).dmg

app-mac: bundle-mac recorder-app
	rm -rf build/dist
	mkdir -p $(DIST_APP)/Contents/Helpers
	ditto $(APP_BUNDLE) $(DIST_APP)
	ditto "$(RECORDER_APP)" "$(DIST_APP)/Contents/Helpers/Musubi Recorder.app"
	cp packaging/macos/recorder/launchagent.plist.template $(DIST_APP)/Contents/Resources/recorder-launchagent.plist.template
	codesign --sign "$(CODESIGN_IDENTITY)" --force $(DIST_APP)/Contents/MacOS/musubi
	codesign --sign "$(CODESIGN_IDENTITY)" --force $(DIST_APP)
	codesign --verify --deep --strict $(DIST_APP)
	@echo "Built $(DIST_APP) ($(VERSION))"

# A disk image with Musubi.app and a link to /Applications to drag it to.
dmg: app-mac
	rm -rf build/dmg $(DMG)
	mkdir -p build/dmg
	ditto $(DIST_APP) build/dmg/Musubi.app
	ln -s /Applications build/dmg/Applications
	hdiutil create -volname "Musubi $(VERSION)" -srcfolder build/dmg -fs HFS+ -format UDZO -ov $(DMG)
	@echo "Built $(DMG)"

clean:
	rm -rf bin build
