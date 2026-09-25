#!/bin/sh
# Install the deconflict client from a GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/cloudcons/deconflict-cli/main/install.sh | sh
#
# Options, as flags or environment variables:
#   --version X.Y.Z   DECONFLICT_VERSION      release to install (default: latest)
#   --dir PATH        DECONFLICT_INSTALL_DIR  where the binary goes (default: ~/.local/bin)
#
# The archive is checked against the release's SHA256SUMS before anything is
# written to the install directory. Nothing else on the machine is touched: the
# agent hooks are a separate, explicit step (`deconflict install`).
#
# Everything runs inside main, so a download cut off halfway through a pipe to
# sh executes nothing.

set -eu

REPO=cloudcons/deconflict-cli

say() { printf 'deconflict: %s\n' "$*" >&2; }
die() { say "$*"; exit 1; }

usage() {
	cat >&2 <<-EOF
	usage: install.sh [--version X.Y.Z] [--dir PATH]
	  --version  release to install (default: latest; env DECONFLICT_VERSION)
	  --dir      where the binary goes (default: ~/.local/bin; env DECONFLICT_INSTALL_DIR)
	EOF
	exit "$1"
}

detect_os() {
	case "$(uname -s)" in
	Linux) echo linux ;;
	Darwin) echo darwin ;;
	*) die "unsupported OS $(uname -s); on Windows use install.ps1, otherwise download from https://github.com/$REPO/releases" ;;
	esac
}

detect_arch() {
	case "$(uname -m)" in
	x86_64 | amd64)
		# A shell running under Rosetta reports x86_64 on Apple silicon; the
		# native binary is the one to install there.
		if [ "$1" = darwin ] && [ "$(sysctl -n hw.optional.arm64 2>/dev/null || echo 0)" = 1 ]; then
			echo arm64
		else
			echo amd64
		fi
		;;
	aarch64 | arm64) echo arm64 ;;
	*) die "unsupported architecture $(uname -m); releases are built for amd64 and arm64" ;;
	esac
}

# The /releases/latest page redirects to the tag, which avoids the API and its
# unauthenticated rate limit.
latest_version() {
	url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest") ||
		die "could not reach github.com to find the latest release"
	tag=${url##*/}
	case "$tag" in
	v[0-9]*) echo "${tag#v}" ;;
	*) die "could not work out the latest release from $url" ;;
	esac
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	else
		die "need sha256sum or shasum to verify the download"
	fi
}

main() {
	version=${DECONFLICT_VERSION:-}
	dir=${DECONFLICT_INSTALL_DIR:-"$HOME/.local/bin"}

	while [ $# -gt 0 ]; do
		case "$1" in
		--version) [ $# -ge 2 ] || die "--version needs a value"; version=$2; shift 2 ;;
		--version=*) version=${1#*=}; shift ;;
		--dir) [ $# -ge 2 ] || die "--dir needs a value"; dir=$2; shift 2 ;;
		--dir=*) dir=${1#*=}; shift ;;
		-h | --help) usage 0 ;;
		*) say "unknown option $1"; usage 2 ;;
		esac
	done

	command -v curl >/dev/null 2>&1 || die "curl is required"
	command -v tar >/dev/null 2>&1 || die "tar is required"

	os=$(detect_os)
	arch=$(detect_arch "$os")
	[ -n "$version" ] || version=$(latest_version)
	version=${version#v}

	name="deconflict_${version}_${os}_${arch}"
	base="https://github.com/$REPO/releases/download/v$version"

	tmp=$(mktemp -d 2>/dev/null || mktemp -d -t deconflict)
	trap 'rm -rf "$tmp"' EXIT INT TERM

	say "downloading $name"
	curl -fsSL -o "$tmp/$name.tar.gz" "$base/$name.tar.gz" ||
		die "download failed: $base/$name.tar.gz (does v$version exist?)"
	curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS" ||
		die "download failed: $base/SHA256SUMS"

	want=$(awk -v f="$name.tar.gz" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")
	[ -n "$want" ] || die "$name.tar.gz is not listed in SHA256SUMS"
	got=$(sha256 "$tmp/$name.tar.gz")
	[ "$want" = "$got" ] || die "checksum mismatch for $name.tar.gz: expected $want, got $got"

	tar -xzf "$tmp/$name.tar.gz" -C "$tmp" deconflict

	mkdir -p "$dir" 2>/dev/null || die "cannot create $dir; pick another with --dir"
	[ -w "$dir" ] || die "$dir is not writable; rerun with sudo or pick another with --dir"
	# Write beside the target and rename, so a running deconflict (an agent's
	# MCP server, say) keeps its inode instead of having it rewritten underneath.
	cp "$tmp/deconflict" "$dir/.deconflict.new"
	chmod 0755 "$dir/.deconflict.new"
	mv -f "$dir/.deconflict.new" "$dir/deconflict"

	say "installed $("$dir/deconflict" version 2>/dev/null || echo "v$version") to $dir/deconflict"
	case ":$PATH:" in
	*":$dir:"*) ;;
	*) say "$dir is not on your PATH; add it to your shell profile" ;;
	esac
	say "next: deconflict install --agent all"
}

main "$@"
