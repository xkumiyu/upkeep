#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
test_dir=$(mktemp -d "${TMPDIR:-/tmp}/upkeep-install-test.XXXXXX")
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM

fake_bin=$test_dir/fake-bin
release_dir=$test_dir/release
home_dir=$test_dir/home
latest_bin=$home_dir/.local/bin
latest_config=$home_dir/.config
mkdir -p "$fake_bin" "$release_dir"

cat >"$fake_bin/curl" <<'EOF'
#!/bin/sh
set -eu

output=
url=
while [ "$#" -gt 0 ]; do
    case "$1" in
        -o)
            output=$2
            shift 2
            ;;
        --*|-*)
            shift
            ;;
        *)
            url=$1
            shift
            ;;
    esac
done

case "$url" in
    https://example.test/upkeep/releases/latest/download/*|https://example.test/upkeep/releases/download/v1.2.3/*) ;;
    *)
        echo "unexpected URL: $url" >&2
        exit 1
        ;;
esac

cp "$UPKEEP_FAKE_RELEASE/${url##*/}" "$output"
EOF
chmod 755 "$fake_bin/curl"

cat >"$fake_bin/uname" <<'EOF'
#!/bin/sh
case "$1" in
    -s) printf '%s\n' Linux ;;
    -m) printf '%s\n' x86_64 ;;
esac
EOF
chmod 755 "$fake_bin/uname"

cat >"$fake_bin/go" <<'EOF'
#!/bin/sh
echo "go must not be called" >&2
exit 1
EOF
chmod 755 "$fake_bin/go"

cat >"$release_dir/upkeep-linux-amd64" <<'EOF'
#!/bin/sh
if [ "${1:-}" = "--version" ]; then
    printf '%s\n' 'upkeep v1.2.3'
fi
EOF
chmod 755 "$release_dir/upkeep-linux-amd64"
printf '%s\n' '# fake config' >"$release_dir/config.example.toml"
if command -v sha256sum >/dev/null 2>&1; then
    (CDPATH= cd -- "$release_dir" && sha256sum upkeep-linux-amd64 config.example.toml >checksums.txt)
else
    (CDPATH= cd -- "$release_dir" && shasum -a 256 upkeep-linux-amd64 config.example.toml >checksums.txt)
fi

test_path=$fake_bin:/usr/bin:/bin

(
    unset UPKEEP_VERSION
    PATH=$test_path \
    UPKEEP_FAKE_RELEASE=$release_dir \
    UPKEEP_REPOSITORY_URL=https://example.test/upkeep \
    UPKEEP_BIN_DIR="$latest_bin" \
    XDG_CONFIG_HOME="$latest_config" \
    HOME="$home_dir" \
    /bin/sh "$script_dir/install.sh" >"$test_dir/latest-install.out"
)
test -x "$latest_bin/upkeep"
test "$("$latest_bin/upkeep" --version)" = 'upkeep v1.2.3'
test -f "$latest_config/upkeep/config.toml"
grep -q '^Installed upkeep v1.2.3$' "$test_dir/latest-install.out"
grep -q '^Next:$' "$test_dir/latest-install.out"
grep -q '^  Add ~/.local/bin to PATH\.$' "$test_dir/latest-install.out"
grep -q '^  Edit ~/.config/upkeep/config.toml, then run `upkeep run`\.$' "$test_dir/latest-install.out"

(
    UPKEEP_VERSION=v1.2.3 \
    PATH=$test_path \
    UPKEEP_FAKE_RELEASE=$release_dir \
    UPKEEP_REPOSITORY_URL=https://example.test/upkeep \
    UPKEEP_BIN_DIR="$latest_bin" \
    XDG_CONFIG_HOME="$latest_config" \
    HOME="$home_dir" \
    /bin/sh "$script_dir/install.sh" >"$test_dir/update.out"
)
test -x "$latest_bin/upkeep"
test -f "$latest_config/upkeep/config.toml"
grep -q '^Installed upkeep v1.2.3$' "$test_dir/update.out"
grep -q '^Next:$' "$test_dir/update.out"
grep -q '^  Add ~/.local/bin to PATH\.$' "$test_dir/update.out"
if grep -q 'Edit the ' "$test_dir/update.out"; then
    echo "install.sh suggested editing an existing config" >&2
    exit 1
fi

(
    unset UPKEEP_VERSION
    PATH="$latest_bin:$test_path" \
    UPKEEP_FAKE_RELEASE=$release_dir \
    UPKEEP_REPOSITORY_URL=https://example.test/upkeep \
    UPKEEP_BIN_DIR="$latest_bin" \
    XDG_CONFIG_HOME="$latest_config" \
    HOME="$home_dir" \
    /bin/sh "$script_dir/install.sh" >"$test_dir/no-next.out"
)
test "$(cat "$test_dir/no-next.out")" = 'Installed upkeep v1.2.3'

bad_release_dir=$test_dir/bad-release
mkdir -p "$bad_release_dir"
cp "$release_dir/upkeep-linux-amd64" "$bad_release_dir/upkeep-linux-amd64"
sed 's/^[0-9a-f]*/0000000000000000000000000000000000000000000000000000000000000000/' \
    "$release_dir/checksums.txt" >"$bad_release_dir/checksums.txt"
if (
    PATH=$test_path \
    UPKEEP_FAKE_RELEASE=$bad_release_dir \
    UPKEEP_REPOSITORY_URL=https://example.test/upkeep \
    UPKEEP_BIN_DIR="$test_dir/bad-bin" \
    XDG_CONFIG_HOME="$test_dir/bad-config" \
    /bin/sh "$script_dir/install.sh" >"$test_dir/bad-checksum.out" 2>&1
); then
    echo "install.sh accepted a bad checksum" >&2
    exit 1
fi
grep -q 'checksum verification failed for upkeep-linux-amd64' "$test_dir/bad-checksum.out"

if (
    UPKEEP_VERSION=main \
    PATH=$test_path \
    UPKEEP_FAKE_RELEASE=$release_dir \
    UPKEEP_REPOSITORY_URL=https://example.test/upkeep \
    UPKEEP_BIN_DIR="$test_dir/invalid-bin" \
    XDG_CONFIG_HOME="$test_dir/invalid-config" \
    /bin/sh "$script_dir/install.sh" >"$test_dir/invalid-version.out" 2>&1
); then
    echo "install.sh accepted an invalid UPKEEP_VERSION" >&2
    exit 1
fi
grep -q 'UPKEEP_VERSION must match vX.Y.Z' "$test_dir/invalid-version.out"
