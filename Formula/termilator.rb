# frozen_string_literal: true

# Written by scripts/release/formula.sh for each release; don't edit by hand.
class Termilator < Formula
  desc "Terminal workspace where coding agents work through a project's tasks"
  homepage "https://github.com/theclifmeister/termilator"
  version "0.5.2"
  license "MIT"

  livecheck do
    url :stable
    strategy :github_latest
  end

  on_macos do
    on_arm do
      url "https://github.com/theclifmeister/termilator/releases/download/v#{version}/tm_darwin_arm64.tar.gz"
      sha256 "5ce80a36dd7cf22d5059e27b83d5b7b32b949e746768033f0a36bd95d59cac4c"
    end
    on_intel do
      url "https://github.com/theclifmeister/termilator/releases/download/v#{version}/tm_darwin_amd64.tar.gz"
      sha256 "f5e58ae056f6a4d7f32e9ebe2c75baed2bb23e9fc1419e2bfeb6a52d3ce7225b"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/theclifmeister/termilator/releases/download/v#{version}/tm_linux_arm64.tar.gz"
      sha256 "9e68f35dfe8de9ee1ef00d5fe6d71be4a4b98fef0b5d2030887fe5561f8866f0"
    end
    on_intel do
      url "https://github.com/theclifmeister/termilator/releases/download/v#{version}/tm_linux_amd64.tar.gz"
      sha256 "f877c2920c05d968fe93aaf83e9ed3c6d5dee7390cb686aefc97273f88d42b66"
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
