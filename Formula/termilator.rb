# frozen_string_literal: true

# Written by scripts/release/formula.sh for each release; don't edit by hand.
class Termilator < Formula
  desc "Terminal workspace where coding agents work through a project's tasks"
  homepage "https://github.com/theclifmeister/termilator"
  version "0.5.0"
  license "MIT"

  livecheck do
    url :stable
    strategy :github_latest
  end

  on_macos do
    on_arm do
      url "https://github.com/theclifmeister/termilator/releases/download/v#{version}/tm_darwin_arm64.tar.gz"
      sha256 "46bbdc13a80966a6f90bda39a2ea56f016d07b6fcbf976909f6f8f5f892c590f"
    end
    on_intel do
      url "https://github.com/theclifmeister/termilator/releases/download/v#{version}/tm_darwin_amd64.tar.gz"
      sha256 "9fdf29f85332b63847cc84f08e6bf1cea47991aa1a9081d5aa735d955477e654"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/theclifmeister/termilator/releases/download/v#{version}/tm_linux_arm64.tar.gz"
      sha256 "0d2f9d9fad2d8dfb16b8034634cfe9ee7bf9c701472dd56662c32b0c249ce4eb"
    end
    on_intel do
      url "https://github.com/theclifmeister/termilator/releases/download/v#{version}/tm_linux_amd64.tar.gz"
      sha256 "3d758b80fd14ee80d06321bdfd9a90725520f31d58b542a5eace703b2fd5fcb1"
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
