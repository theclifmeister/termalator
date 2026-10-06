# frozen_string_literal: true

# Written by scripts/release/formula.sh for each release; don't edit by hand.
class Termilator < Formula
  desc "Terminal workspace where coding agents work through a project's tasks"
  homepage "https://github.com/theclifmeister/termilator"
  version "0.6.1"
  license "MIT"

  livecheck do
    url :stable
    strategy :github_latest
  end

  on_macos do
    on_arm do
      url "https://github.com/theclifmeister/termilator/releases/download/v#{version}/tm_darwin_arm64.tar.gz"
      sha256 "05fa05a04b17755f6331f15944b4d58d8c37551be03dccc43f602ac01cdc2b55"
    end
    on_intel do
      url "https://github.com/theclifmeister/termilator/releases/download/v#{version}/tm_darwin_amd64.tar.gz"
      sha256 "e797012c981b86a5e9647523f66b74a00543bbc59c407e8baed5d50e1ac901cb"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/theclifmeister/termilator/releases/download/v#{version}/tm_linux_arm64.tar.gz"
      sha256 "eb7b3dbd05b9f01cdbd6e6e9ea377413aeb07352fde046c330be32a4f76815b6"
    end
    on_intel do
      url "https://github.com/theclifmeister/termilator/releases/download/v#{version}/tm_linux_amd64.tar.gz"
      sha256 "c1603b780841667c6885a745ef9cfe89c1f1216026b7bfde3706ee1955ce545d"
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
