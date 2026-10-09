# frozen_string_literal: true

# Written by scripts/release/formula.sh for each release; don't edit by hand.
class Terminatr < Formula
  desc "Terminal workspace where coding agents work through a project's tasks"
  homepage "https://github.com/theclifmeister/terminatr"
  version "0.15.1"
  license "MIT"

  livecheck do
    url :stable
    strategy :github_latest
  end

  on_macos do
    on_arm do
      url "https://github.com/theclifmeister/terminatr/releases/download/v#{version}/tm_darwin_arm64.tar.gz"
      sha256 "0b9c387e6630e7b368558cec20e1fb627cee313b8c007a3f983841ce48278b13"
    end
    on_intel do
      url "https://github.com/theclifmeister/terminatr/releases/download/v#{version}/tm_darwin_amd64.tar.gz"
      sha256 "8c26328c02a6847c2071c3e7528c19c14c248d4475a2def08527136f2c6782d9"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/theclifmeister/terminatr/releases/download/v#{version}/tm_linux_arm64.tar.gz"
      sha256 "f4694830a725d6f89c88c51adafdeb4dd7290383add2e2a3b20cc98081fcdee6"
    end
    on_intel do
      url "https://github.com/theclifmeister/terminatr/releases/download/v#{version}/tm_linux_amd64.tar.gz"
      sha256 "60c18479c9fcf96c520feb8fff106e1598ea42ff619e5b7db809f2a92597418d"
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
