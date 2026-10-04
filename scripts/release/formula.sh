#!/bin/sh
# Writes the Homebrew formula for a release to stdout, from the release's
# checksums.txt. The release workflow runs it after publishing and opens a
# pull request with the result (Formula/termalator.rb):
#
#   scripts/release/formula.sh 0.2.0 dist/checksums.txt > Formula/termalator.rb
#
# One formula for macOS and Linux, not a cask: casks are macOS-only, and a
# formula installs the signed, notarised binary as it is. Homebrew sets no
# quarantine flag on formula downloads, and tm links only system
# libraries, so Homebrew rewrites nothing in it and the signature stays.
set -eu
version=${1:?usage: formula.sh <version> <checksums.txt>}
sums=${2:?usage: formula.sh <version> <checksums.txt>}
version=${version#v}
sha() {
	s=$(awk -v f="tm_$1.tar.gz" '$2 == f {print $1}' "$sums")
	[ -n "$s" ] || { echo "formula.sh: no tm_$1.tar.gz in $sums" >&2; exit 1; }
	echo "$s"
}
darwin_arm64=$(sha darwin_arm64)
darwin_amd64=$(sha darwin_amd64)
linux_arm64=$(sha linux_arm64)
linux_amd64=$(sha linux_amd64)
cat <<RUBY
# frozen_string_literal: true

# Written by scripts/release/formula.sh for each release; don't edit by hand.
class Termalator < Formula
  desc "Terminal workspace where coding agents work through a project's tasks"
  homepage "https://github.com/theclifmeister/termalator"
  version "$version"
  license "MIT"

  livecheck do
    url :stable
    strategy :github_latest
  end

  on_macos do
    on_arm do
      url "https://github.com/theclifmeister/termalator/releases/download/v#{version}/tm_darwin_arm64.tar.gz"
      sha256 "$darwin_arm64"
    end
    on_intel do
      url "https://github.com/theclifmeister/termalator/releases/download/v#{version}/tm_darwin_amd64.tar.gz"
      sha256 "$darwin_amd64"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/theclifmeister/termalator/releases/download/v#{version}/tm_linux_arm64.tar.gz"
      sha256 "$linux_arm64"
    end
    on_intel do
      url "https://github.com/theclifmeister/termalator/releases/download/v#{version}/tm_linux_amd64.tar.gz"
      sha256 "$linux_amd64"
    end
  end

  def install
    bin.install "tm"
    doc.install "README.md", "docs/OPERATIONS.md"
  end

  def caveats
    <<~EOS
      A running tm server keeps the old binary after \`brew upgrade\`;
      \`tm server restart\` switches to the new one (agents are resumed).
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/tm version")
    system bin/"tm", "selftest"
  end
end
RUBY
