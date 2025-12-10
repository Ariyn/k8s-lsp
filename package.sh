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
mkdir -p "$BUILD_DIR/bin/linux/rules"
GOOS=linux GOARCH=amd64 go build -o "$BUILD_DIR/bin/linux/k8s-lsp" .
cp rules/k8s.yaml "$BUILD_DIR/bin/linux/rules/"

# macOS (Darwin) - AMD64 and ARM64 (Universal binary not supported easily in this structure, so just AMD64 for now or both?)
# VS Code runs on Node, process.platform is 'darwin'.
# We can just provide amd64, Rosetta handles it on M1/M2 usually. Or we can try to detect arch in extension.ts but process.arch is available.
# For simplicity, let's just build amd64 for darwin.
echo "Building for macOS..."
mkdir -p "$BUILD_DIR/bin/darwin/rules"
GOOS=darwin GOARCH=amd64 go build -o "$BUILD_DIR/bin/darwin/k8s-lsp" .
cp rules/k8s.yaml "$BUILD_DIR/bin/darwin/rules/"

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
