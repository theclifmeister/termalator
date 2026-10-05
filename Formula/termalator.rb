# frozen_string_literal: true

# Written by scripts/release/formula.sh for each release; don't edit by hand.
class Termalator < Formula
  desc "Terminal workspace where coding agents work through a project's tasks"
  homepage "https://github.com/theclifmeister/termalator"
  version "0.1.0"
  license "MIT"

  livecheck do
    url :stable
    strategy :github_latest
  end

  on_macos do
    on_arm do
      url "https://github.com/theclifmeister/termalator/releases/download/v#{version}/tm_darwin_arm64.tar.gz"
      sha256 "2214d765bf8552466f26f9cd40d31985ce3cc0fd3e7f065e3a6cedb3a49e82ad"
    end
    on_intel do
      url "https://github.com/theclifmeister/termalator/releases/download/v#{version}/tm_darwin_amd64.tar.gz"
      sha256 "2373742be4cc5f62154baa3f6b26fe95887c877cbc1c3c721e00e58f2b8d8aab"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/theclifmeister/termalator/releases/download/v#{version}/tm_linux_arm64.tar.gz"
      sha256 "e13ee6c1453988ec55a5d29e1e8cecf610984b438738c675a3b8aa0b8167f1fc"
    end
    on_intel do
      url "https://github.com/theclifmeister/termalator/releases/download/v#{version}/tm_linux_amd64.tar.gz"
      sha256 "f5401723add7f7ab484010d19f0b52fefb2afad5869b08ba758098cb52a9abb3"
    end
  end

  def install
    bin.install "tm"
    doc.install "README.md", "docs/OPERATIONS.md"
  end

  def caveats
    <<~EOS
      A running tm server keeps the old binary after `brew upgrade`;
      `tm server restart` switches to the new one (agents are resumed).
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/tm version")
    system bin/"tm", "selftest"
  end
end
