# frozen_string_literal: true

# Written by scripts/release/formula.sh for each release; don't edit by hand.
class Terminatr < Formula
  desc "Terminal workspace where coding agents work through a project's tasks"
  homepage "https://github.com/theclifmeister/terminatr"
  version "0.14.0"
  license "MIT"

  livecheck do
    url :stable
    strategy :github_latest
  end

  on_macos do
    on_arm do
      url "https://github.com/theclifmeister/terminatr/releases/download/v#{version}/tm_darwin_arm64.tar.gz"
      sha256 "471400e28d4737fb6cd2afe1701055f57ea072b7bf31ff33c4473283b468684f"
    end
    on_intel do
      url "https://github.com/theclifmeister/terminatr/releases/download/v#{version}/tm_darwin_amd64.tar.gz"
      sha256 "af48c8e19d54e0d5dbf41f7bd8f84d44f1247ad6daa6e26a262a2fb442dc9f6c"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/theclifmeister/terminatr/releases/download/v#{version}/tm_linux_arm64.tar.gz"
      sha256 "9874c93ae01c113ddd3cc5e0846f86a728ccaf86372922bbba8dd824e0d63118"
    end
    on_intel do
      url "https://github.com/theclifmeister/terminatr/releases/download/v#{version}/tm_linux_amd64.tar.gz"
      sha256 "e4b5c4bcbf4a8d31405142ff7ac6eb63613c0ae55effbf91701530bb27923a9a"
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
