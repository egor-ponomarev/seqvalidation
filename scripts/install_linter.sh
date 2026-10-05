#!/bin/sh
set -eu

version=$1
destination=$2
seq_install_dir=$(mktemp -d)
trap 'rm -rf "$seq_install_dir"' EXIT HUP INT TERM

curl -fsSL "https://raw.githubusercontent.com/golangci/golangci-lint/v${version}/install.sh" \
    -o "$seq_install_dir/install.sh"
sh "$seq_install_dir/install.sh" -b "$seq_install_dir/bin" "v$version"

installed_version=$("$seq_install_dir/bin/golangci-lint" version --short)
if [ "$installed_version" != "$version" ]; then
    printf 'Unexpected golangci-lint version: %s\n' "$installed_version" >&2
    exit 1
fi

mkdir -p "$(dirname "$destination")"
mv "$seq_install_dir/bin/golangci-lint" "$destination"
