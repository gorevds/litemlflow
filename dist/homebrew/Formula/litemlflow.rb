class Litemlflow < Formula
  desc "Single-binary, MLflow-API-compatible experiment tracker with first-class LLM tracing"
  homepage "https://litemlflow.dev"
  version "0.4.0-rc1"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://github.com/litemlflow/litemlflow/releases/download/v#{version}/litemlflow-v#{version}-darwin-arm64"
      sha256 "PLACEHOLDER_DARWIN_ARM64"
    end
    on_intel do
      url "https://github.com/litemlflow/litemlflow/releases/download/v#{version}/litemlflow-v#{version}-darwin-amd64"
      sha256 "PLACEHOLDER_DARWIN_AMD64"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/litemlflow/litemlflow/releases/download/v#{version}/litemlflow-v#{version}-linux-arm64"
      sha256 "PLACEHOLDER_LINUX_ARM64"
    end
    on_intel do
      url "https://github.com/litemlflow/litemlflow/releases/download/v#{version}/litemlflow-v#{version}-linux-amd64"
      sha256 "PLACEHOLDER_LINUX_AMD64"
    end
  end

  def install
    bin.install Dir["litemlflow-*"].first => "litemlflow"
  end

  test do
    output = shell_output("#{bin}/litemlflow version")
    assert_match "litemlflow", output
  end
end
