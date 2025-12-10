#!/bin/bash
set -e

# Define output name
VSIX_NAME="k8s-lsp-client.vsix"
OUTPUT_PATH="$PWD/$VSIX_NAME"

# Create a temporary build directory
BUILD_DIR=$(mktemp -d)
echo "Building in temporary directory: $BUILD_DIR"

# Ensure cleanup happens on exit
cleanup() {
    echo "Cleaning up..."
    rm -rf "$BUILD_DIR"
}
trap cleanup EXIT

# 1. Copy client source to build directory
echo "Copying client source..."
# Copy everything from client/ to BUILD_DIR/
cp -r client/. "$BUILD_DIR/"

# 2. Build Go binary into the build directory
echo "Building Go binaries..."

# Linux
echo "Building for Linux..."
mkdir -p "$BUILD_DIR/bin/linux/x64/rules"
GOOS=linux GOARCH=amd64 go build -o "$BUILD_DIR/bin/linux/x64/k8s-lsp" .
cp rules/k8s.yaml "$BUILD_DIR/bin/linux/x64/rules/"

# macOS (Darwin) - AMD64
echo "Building for macOS (AMD64)..."
mkdir -p "$BUILD_DIR/bin/darwin/x64/rules"
GOOS=darwin GOARCH=amd64 go build -o "$BUILD_DIR/bin/darwin/x64/k8s-lsp" .
cp rules/k8s.yaml "$BUILD_DIR/bin/darwin/x64/rules/"

# macOS (Darwin) - ARM64
echo "Building for macOS (ARM64)..."
mkdir -p "$BUILD_DIR/bin/darwin/arm64/rules"
GOOS=darwin GOARCH=arm64 go build -o "$BUILD_DIR/bin/darwin/arm64/k8s-lsp" .
cp rules/k8s.yaml "$BUILD_DIR/bin/darwin/arm64/rules/"

# 3. (Skipped as we did it per platform)

# 4. Install dependencies and package in the build directory
echo "Packaging extension..."
pushd "$BUILD_DIR" > /dev/null

# Install dependencies if node_modules doesn't exist (or just run install to be safe/update)
npm install

# Compile TypeScript
npm run compile

# Package into VSIX
npx vsce package -o "$OUTPUT_PATH"

popd > /dev/null

echo "Done! Package created at: $OUTPUT_PATH"
