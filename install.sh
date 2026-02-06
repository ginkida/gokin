#!/bin/sh
# Install script for gokin
# Usage: curl -fsSL https://raw.githubusercontent.com/ginkida/gokin/main/install.sh | sh
set -e

REPO="ginkida/gokin"
INSTALL_DIR=""
BINARY_NAME="gokin"

log() {
    printf '%s\n' "$1"
}

error() {
    printf 'Error: %s\n' "$1" >&2
    exit 1
}

detect_os() {
    os="$(uname -s)"
    case "$os" in
        Linux*)  echo "linux" ;;
        Darwin*) echo "darwin" ;;
        *)       error "Unsupported OS: $os. Only Linux and macOS are supported." ;;
    esac
}

detect_arch() {
    arch="$(uname -m)"
    case "$arch" in
        x86_64|amd64)  echo "amd64" ;;
        aarch64|arm64)  echo "arm64" ;;
        *)              error "Unsupported architecture: $arch. Only amd64 and arm64 are supported." ;;
    esac
}

get_latest_version() {
    url="https://api.github.com/repos/${REPO}/releases/latest"
    response="$(curl -fsSL "$url" 2>/dev/null)" || error "Failed to fetch latest release from GitHub API."
    version="$(printf '%s' "$response" | grep '"tag_name"' | sed 's/.*"tag_name": *"//;s/".*//')"
    if [ -z "$version" ]; then
        error "Could not determine latest version."
    fi
    echo "$version"
}

choose_install_dir() {
    if [ -n "$INSTALL_DIR" ]; then
        return
    fi

    # Prefer ~/.local/bin if it exists or can be created
    local_bin="$HOME/.local/bin"
    if [ -d "$local_bin" ] && [ -w "$local_bin" ]; then
        INSTALL_DIR="$local_bin"
        return
    fi

    # Try to create ~/.local/bin
    if mkdir -p "$local_bin" 2>/dev/null; then
        INSTALL_DIR="$local_bin"
        return
    fi

    # Fallback to /usr/local/bin with sudo
    INSTALL_DIR="/usr/local/bin"
}

check_path() {
    case ":$PATH:" in
        *:"$INSTALL_DIR":*) return 0 ;;
    esac
    return 1
}

main() {
    log "Installing gokin..."
    log ""

    os="$(detect_os)"
    arch="$(detect_arch)"
    log "  OS:   $os"
    log "  Arch: $arch"

    version="$(get_latest_version)"
    log "  Version: $version"
    log ""

    asset="gokin-${os}-${arch}.tar.gz"
    download_url="https://github.com/${REPO}/releases/download/${version}/${asset}"

    tmpdir="$(mktemp -d)"
    trap 'rm -rf "$tmpdir"' EXIT

    log "Downloading ${asset}..."
    curl -fsSL -o "${tmpdir}/${asset}" "$download_url" || error "Failed to download ${download_url}"

    log "Extracting..."
    tar xzf "${tmpdir}/${asset}" -C "$tmpdir" || error "Failed to extract ${asset}"

    # The archive contains a binary named gokin-{os}-{arch}
    src="${tmpdir}/gokin-${os}-${arch}"
    if [ ! -f "$src" ]; then
        error "Expected binary not found in archive: gokin-${os}-${arch}"
    fi
    chmod +x "$src"

    choose_install_dir
    dest="${INSTALL_DIR}/${BINARY_NAME}"

    log "Installing to ${dest}..."
    if [ -w "$INSTALL_DIR" ]; then
        mv "$src" "$dest"
    else
        log "  (requires sudo)"
        sudo mv "$src" "$dest"
    fi

    log ""
    log "gokin ${version} installed to ${dest}"

    if ! check_path; then
        log ""
        log "WARNING: ${INSTALL_DIR} is not in your PATH."
        log "Add it by running:"
        log ""
        log "  export PATH=\"${INSTALL_DIR}:\$PATH\""
        log ""
        log "To make it permanent, add the line above to your ~/.bashrc, ~/.zshrc, or equivalent."
    fi

    log ""
    log "Run 'gokin --help' to get started."
}

main
