# frozen_string_literal: true

# Written by scripts/release/formula.sh for each release; don't edit by hand.
class Termilator < Formula
  desc "Terminal workspace where coding agents work through a project's tasks"
  homepage "https://github.com/theclifmeister/termilator"
  version "0.6.0"
  license "MIT"

  livecheck do
    url :stable
    strategy :github_latest
  end

  on_macos do
    on_arm do
      url "https://github.com/theclifmeister/termilator/releases/download/v#{version}/tm_darwin_arm64.tar.gz"
      sha256 "a6da0aa10cbff275861ce42d540c2bff942f2795b55808a53f7592149cc1253a"
    end
    on_intel do
      url "https://github.com/theclifmeister/termilator/releases/download/v#{version}/tm_darwin_amd64.tar.gz"
      sha256 "989b2913d68ad6469c5fdce01a4f88babef2f046085decce42040909d4cda6e3"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/theclifmeister/termilator/releases/download/v#{version}/tm_linux_arm64.tar.gz"
      sha256 "041695bef95bd8046eebebb31c8136971642c462a0cd30c14aab0e4477e44dae"
    end
    on_intel do
      url "https://github.com/theclifmeister/termilator/releases/download/v#{version}/tm_linux_amd64.tar.gz"
      sha256 "dd3a216833eff9a2e7e4cf55e06d05061c30dd2d45c27e91dd9b6797fdbf0171"
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
