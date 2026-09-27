#!/bin/sh
set -eu

BIN_DIR=${UPKEEP_BIN_DIR:-"$HOME/.local/bin"}
CONFIG_HOME=${XDG_CONFIG_HOME:-"$HOME/.config"}
CONFIG_DIR="$CONFIG_HOME/upkeep"
CONFIG_PATH="$CONFIG_DIR/config.toml"
SOURCE_DIR=
REPOSITORY_REF=main

if ! command -v go >/dev/null 2>&1; then
    echo "install.sh: go is required" >&2
    exit 1
fi

case "$0" in
    */*|install.sh)
        if [ -f "$0" ]; then
            local_script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
            if [ -f "$local_script_dir/go.mod" ] && [ -f "$local_script_dir/main.go" ]; then
                SOURCE_DIR=$local_script_dir
            fi
        fi
        ;;
esac

if [ -z "$SOURCE_DIR" ]; then
    repository_url=${UPKEEP_REPOSITORY_URL:-https://github.com/xkumiyu/upkeep}
    repository_ref=${UPKEEP_REPOSITORY_REF:-main}
    REPOSITORY_REF=$repository_ref
    case "$repository_url" in
        https://*) ;;
        *)
            echo "install.sh: UPKEEP_REPOSITORY_URL must use https" >&2
            exit 1
            ;;
    esac
    if ! command -v tar >/dev/null 2>&1; then
        echo "install.sh: tar is required for remote installation" >&2
        exit 1
    fi

    if command -v curl >/dev/null 2>&1; then
        download_tool=curl
    elif command -v wget >/dev/null 2>&1; then
        download_tool=wget
    else
        echo "install.sh: curl or wget is required for remote installation" >&2
        exit 1
    fi

    download_dir=$(mktemp -d "${TMPDIR:-/tmp}/upkeep-install.XXXXXX")
    trap 'rm -rf "$download_dir"' EXIT HUP INT TERM
    archive_path="$download_dir/source.tar.gz"
    case "$repository_ref" in
        v[0-9]*) archive_url="$repository_url/archive/refs/tags/$repository_ref.tar.gz" ;;
        *) archive_url="$repository_url/archive/refs/heads/$repository_ref.tar.gz" ;;
    esac
    if [ "$download_tool" = curl ]; then
        curl -fsSL "$archive_url" -o "$archive_path"
    else
        wget -qO "$archive_path" "$archive_url"
    fi
    tar -xzf "$archive_path" -C "$download_dir"
    for candidate in "$download_dir"/upkeep-*; do
        if [ -d "$candidate" ]; then
            SOURCE_DIR=$candidate
            break
        fi
    done
    if [ -z "$SOURCE_DIR" ] || [ ! -f "$SOURCE_DIR/go.mod" ] || [ ! -f "$SOURCE_DIR/main.go" ]; then
        echo "install.sh: downloaded archive does not contain upkeep source" >&2
        exit 1
    fi
fi

build_version=${UPKEEP_VERSION:-}
if [ -z "$build_version" ] && command -v git >/dev/null 2>&1 && [ -e "$SOURCE_DIR/.git" ]; then
    build_version=$(git -C "$SOURCE_DIR" describe --tags --always --dirty 2>/dev/null || true)
fi
if [ -z "$build_version" ]; then
    case "$REPOSITORY_REF" in
        v[0-9]*) build_version=$REPOSITORY_REF ;;
    esac
fi
if [ -z "$build_version" ]; then
    build_version=dev
fi

mkdir -p "$BIN_DIR" "$CONFIG_DIR"
BIN_DIR=$(CDPATH= cd -- "$BIN_DIR" && pwd)
if [ ! -L "$CONFIG_DIR" ]; then
    chmod 700 "$CONFIG_DIR"
fi

go -C "$SOURCE_DIR" build -buildvcs=false -ldflags "-X main.version=$build_version" -o "$BIN_DIR/upkeep" .
chmod 755 "$BIN_DIR/upkeep"

if [ ! -e "$CONFIG_PATH" ] && [ ! -L "$CONFIG_PATH" ]; then
    cp "$SOURCE_DIR/config.example.toml" "$CONFIG_PATH"
fi

ACTIVE_CONFIG_PATH="$CONFIG_PATH"

if [ -f "$ACTIVE_CONFIG_PATH" ] && [ ! -L "$ACTIVE_CONFIG_PATH" ]; then
    chmod 600 "$ACTIVE_CONFIG_PATH"
fi

printf 'upkeep is ready: %s\n' "$BIN_DIR/upkeep"
printf 'Configuration: %s\n' "$ACTIVE_CONFIG_PATH"
case ":${PATH:-}:" in
    *":$BIN_DIR:"*) ;;
    *) printf 'Add %s to PATH if needed.\n' "$BIN_DIR" ;;
esac
printf '\nNext steps:\n'
printf '  Edit the config file if needed, then run: upkeep run\n'
