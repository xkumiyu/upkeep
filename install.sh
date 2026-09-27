#!/bin/sh
set -eu

BIN_DIR=${UPKEEP_BIN_DIR:-"$HOME/.local/bin"}
CONFIG_HOME=${XDG_CONFIG_HOME:-"$HOME/.config"}
CONFIG_DIR="$CONFIG_HOME/upkeep"
CONFIG_PATH="$CONFIG_DIR/config.toml"
repository_url=${UPKEEP_REPOSITORY_URL:-https://github.com/xkumiyu/upkeep}
version=${UPKEEP_VERSION:-}

case "$version" in
    "") ;;
    v[0-9]*.[0-9]*.[0-9]*)
        version_number=${version#v}
        case "$version_number" in
            *[!0-9.]*|*.*.*.*|*..*|.*|*.)
                echo "install.sh: UPKEEP_VERSION must match vX.Y.Z" >&2
                exit 1
                ;;
        esac
        ;;
    *)
        echo "install.sh: UPKEEP_VERSION must match vX.Y.Z" >&2
        exit 1
        ;;
esac

case "$repository_url" in
    https://*/*) ;;
    *)
        echo "install.sh: UPKEEP_REPOSITORY_URL must use https" >&2
        exit 1
        ;;
esac
repository_url=${repository_url%/}

release_os_name=$(uname -s)
case "$release_os_name" in
    Linux) release_os=linux ;;
    Darwin) release_os=darwin ;;
    *)
        echo "install.sh: unsupported operating system: $release_os_name (supported: Linux, macOS)" >&2
        exit 1
        ;;
esac

release_arch_name=$(uname -m)
case "$release_arch_name" in
    x86_64|amd64) release_arch=amd64 ;;
    arm64|aarch64) release_arch=arm64 ;;
    *)
        echo "install.sh: unsupported architecture: $release_arch_name (supported: amd64, arm64)" >&2
        exit 1
        ;;
esac

if command -v curl >/dev/null 2>&1; then
    download_tool=curl
elif command -v wget >/dev/null 2>&1; then
    download_tool=wget
else
    echo "install.sh: curl or wget is required" >&2
    exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
    checksum_tool=sha256sum
elif command -v shasum >/dev/null 2>&1; then
    checksum_tool=shasum
else
    echo "install.sh: sha256sum or shasum is required" >&2
    exit 1
fi

if [ -n "$version" ]; then
    release_path="/releases/download/$version"
    release_label=$version
else
    release_path=/releases/latest/download
    release_label=latest
fi

asset_name="upkeep-$release_os-$release_arch"
download_dir=$(mktemp -d "${TMPDIR:-/tmp}/upkeep-install.XXXXXX")
trap 'rm -rf "$download_dir"' EXIT HUP INT TERM
checksums_path="$download_dir/checksums.txt"
asset_path="$download_dir/$asset_name"

download_file() {
    if [ "$download_tool" = curl ]; then
        curl -fsSL --proto '=https' --tlsv1.2 "$1" -o "$2"
    else
        wget -qO "$2" "$1"
    fi
}

verify_checksum() {
    checksum_line=$(awk -v asset="$1" '$2 == asset || $2 == "*" asset { print; exit }' "$checksums_path")
    if [ -z "$checksum_line" ]; then
        return 1
    fi
    printf '%s\n' "$checksum_line" >"$download_dir/expected-checksum"
    if [ "$checksum_tool" = sha256sum ]; then
        (CDPATH= cd -- "$download_dir" && sha256sum -c expected-checksum >/dev/null 2>&1)
    else
        (CDPATH= cd -- "$download_dir" && shasum -a 256 -c expected-checksum >/dev/null 2>&1)
    fi
}

if ! download_file "$repository_url$release_path/checksums.txt" "$checksums_path"; then
    echo "install.sh: release $release_label is unavailable at $repository_url" >&2
    echo "install.sh: check the release tag or set UPKEEP_VERSION=vX.Y.Z" >&2
    exit 1
fi
if ! download_file "$repository_url$release_path/$asset_name" "$asset_path"; then
    echo "install.sh: release $release_label has no $asset_name asset" >&2
    echo "install.sh: check the release tag or set UPKEEP_VERSION=vX.Y.Z" >&2
    exit 1
fi
if ! verify_checksum "$asset_name"; then
    echo "install.sh: checksum verification failed for $asset_name" >&2
    exit 1
fi
chmod 755 "$asset_path"
if ! installed_version=$("$asset_path" --version 2>/dev/null); then
    echo "install.sh: downloaded $asset_name cannot run on this platform" >&2
    exit 1
fi

mkdir -p "$BIN_DIR" "$CONFIG_DIR"
BIN_DIR=$(CDPATH= cd -- "$BIN_DIR" && pwd)
if [ ! -L "$CONFIG_DIR" ]; then
    chmod 700 "$CONFIG_DIR"
fi

config_initialized=0
if [ ! -e "$CONFIG_PATH" ] && [ ! -L "$CONFIG_PATH" ]; then
    config_asset_path="$download_dir/config.example.toml"
    if ! download_file "$repository_url$release_path/config.example.toml" "$config_asset_path"; then
        echo "install.sh: release $release_label is missing config.example.toml" >&2
        exit 1
    fi
    if ! verify_checksum config.example.toml; then
        echo "install.sh: checksum verification failed for config.example.toml" >&2
        exit 1
    fi
    cp "$config_asset_path" "$CONFIG_PATH"
    config_initialized=1
fi

mv "$asset_path" "$BIN_DIR/upkeep"
chmod 755 "$BIN_DIR/upkeep"

if [ -f "$CONFIG_PATH" ] && [ ! -L "$CONFIG_PATH" ]; then
    chmod 600 "$CONFIG_PATH"
fi

path_needs_update=0
case ":${PATH:-}:" in
    *":$BIN_DIR:"*) ;;
    *) path_needs_update=1 ;;
esac

display_bin_dir=$BIN_DIR
case "$BIN_DIR" in
    "$HOME") display_bin_dir='~' ;;
    "$HOME"/*) display_bin_dir="~/${BIN_DIR#"$HOME"/}" ;;
esac
display_config_path=$CONFIG_PATH
case "$CONFIG_PATH" in
    "$HOME") display_config_path='~' ;;
    "$HOME"/*) display_config_path="~/${CONFIG_PATH#"$HOME"/}" ;;
esac

printf 'Installed %s\n' "$installed_version"
if [ "$path_needs_update" -ne 0 ] || [ "$config_initialized" -ne 0 ]; then
    printf '\nNext:\n'
    if [ "$path_needs_update" -ne 0 ]; then
        printf '  Add %s to PATH.\n' "$display_bin_dir"
    fi
    if [ "$config_initialized" -ne 0 ]; then
        printf '  Edit %s, then run `upkeep run`.\n' "$display_config_path"
    fi
fi
