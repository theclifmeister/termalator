# frozen_string_literal: true

# Written by scripts/release/formula.sh for each release; don't edit by hand.
class Termilator < Formula
  desc "Terminal workspace where coding agents work through a project's tasks"
  homepage "https://github.com/theclifmeister/termilator"
  version "0.5.1"
  license "MIT"

  livecheck do
    url :stable
    strategy :github_latest
  end

  on_macos do
    on_arm do
      url "https://github.com/theclifmeister/termilator/releases/download/v#{version}/tm_darwin_arm64.tar.gz"
      sha256 "0f41912fc9b3e1e0cc9d4ad22c7d2f9a263fa6e69af2fedb337e9bc40b780a23"
    end
    on_intel do
      url "https://github.com/theclifmeister/termilator/releases/download/v#{version}/tm_darwin_amd64.tar.gz"
      sha256 "5a6bc5c12fcd907c095aa8189587f48bd5ce15767bddbb242b438c9d3664516a"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/theclifmeister/termilator/releases/download/v#{version}/tm_linux_arm64.tar.gz"
      sha256 "2446aca721efb967109a6e163d75f110c835ea9b6cf7bffd7c1662c13efee360"
    end
    on_intel do
      url "https://github.com/theclifmeister/termilator/releases/download/v#{version}/tm_linux_amd64.tar.gz"
      sha256 "4fbdc55f20add203e3bc2af2448e1461e692887767af6caf3fdb67ee52eaa737"
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
