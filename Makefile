# Plain targets, no CI assumptions. Server targets need Go; iOS targets need macOS with
# Xcode and XcodeGen (https://github.com/yonaskolb/XcodeGen) on PATH or in XCODEGEN.

XCODEGEN ?= xcodegen
IOS_DESTINATION ?= generic/platform=iOS
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: all test server-test server-build server-run ios-project ios-build ios-test clean

all: server-test server-build

test: server-test

server-test:
	cd server && go vet ./... && go test ./...

server-build:
	cd server && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o ../bin/healthkit-relay .

# Local try-out: archive in ./tmp; the first start prints the admin token.
server-run: server-build
	mkdir -p tmp/archive
	./bin/healthkit-relay -archive-dir tmp/archive

ios-project:
	cd ios && $(XCODEGEN) generate

ios-build: ios-project
	cd ios && xcodebuild -project HealthKitRelay.xcodeproj -scheme HealthKitRelay \
		-configuration Release -destination '$(IOS_DESTINATION)' -derivedDataPath build \
		-allowProvisioningUpdates build

ios-test: ios-project
	cd ios && xcodebuild -project HealthKitRelay.xcodeproj -scheme HealthKitRelay \
		-destination 'platform=iOS Simulator,name=iPhone 17' -derivedDataPath build test

clean:
	rm -rf bin tmp ios/build ios/*.xcodeproj
