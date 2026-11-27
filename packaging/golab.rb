# Homebrew formula for golab - unofficial Go port of the Google Colab CLI.
#
# HONESTY: this formula does NOT build from source and does NOT invent assets.
# It installs the same signed GitHub Release .tar.gz that install.sh verifies
# with sha256sum + cosign. The sha256 values below are copied verbatim from the
# published v0.2.0 checksums.txt (verified reachable at the URLs below).
# Homebrew's own SHA-256 check is the only gate here; the cosign keyless
# signature is checked by install.sh / `golab update --install`, not by brew.
#
# BUMP CHECKLIST (new annotated v<version> tag first):
#   1. copy the darwin_{amd64,arm64} and linux_{amd64,arm64} lines from that
#      release's checksums.txt into the four sha256 fields below;
#   2. replace every "0.2.0" in this file (version + 4 URLs);
#   3. keep internal/version.Number, packaging/golab.json, the winget files
#      and packaging/nfpm.yaml on the same version.
# Validate locally: ruby -c packaging/golab.rb ; brew audit --strict golab.rb
class Golab < Formula
  desc 'Unofficial Go port of the Google Colab CLI'
  homepage 'https://github.com/20Youssef10/golab'
  version '0.2.0'
  license 'Apache-2.0'

  on_macos do
    on_intel do
      url 'https://github.com/20Youssef10/golab/releases/download/v0.2.0/golab_0.2.0_darwin_amd64.tar.gz'
      sha256 '689c593a910ecd9bddbc855745af54fb3679cd46c122f276b38b9cc9e1f35c04'
    end
    on_arm do
      url 'https://github.com/20Youssef10/golab/releases/download/v0.2.0/golab_0.2.0_darwin_arm64.tar.gz'
      sha256 '25f75b771b63dafddbf50876c53ff79cb4795b0d6ae73a4e0fbaf0598d4654c6'
    end
  end

  on_linux do
    on_intel do
      url 'https://github.com/20Youssef10/golab/releases/download/v0.2.0/golab_0.2.0_linux_amd64.tar.gz'
      sha256 '27d58b4f0317644050d03c0f161d4f4a8afa0abe1fa965224a74bd6de7eb746e'
    end
    on_arm do
      url 'https://github.com/20Youssef10/golab/releases/download/v0.2.0/golab_0.2.0_linux_arm64.tar.gz'
      sha256 'd974a79a9818edb14dbab192011078146bc9d26a1d2ec8265201db4dddfe1ed9'
    end
  end

  def install
    bin.install 'golab'
  end

  test do
    assert_match 'golab', shell_output("#{bin}/golab version")
  end
end
