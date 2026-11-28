# Homebrew formula for golab - unofficial Go port of the Google Colab CLI.
#
# HONESTY: this formula does NOT build from source and does NOT invent assets.
# It installs the same signed GitHub Release .tar.gz that install.sh verifies
# with sha256sum + cosign. The sha256 values below are copied verbatim from the
# published v0.3.0 checksums.txt (verified reachable at the URLs below).
# Homebrew's own SHA-256 check is the only gate here; the cosign keyless
# signature is checked by install.sh / `golab update --install`, not by brew.
#
# BUMP CHECKLIST (new annotated v<version> tag first):
#   1. copy the darwin_{amd64,arm64} and linux_{amd64,arm64} lines from that
#      release's checksums.txt into the four sha256 fields below;
#   2. replace every "0.3.0" in this file (version + 4 URLs);
#   3. keep internal/version.Number, packaging/golab.json, the winget files
#      and packaging/nfpm.yaml on the same version.
# Validate locally: ruby -c packaging/golab.rb ; brew audit --strict golab.rb
class Golab < Formula
  desc 'Unofficial Go port of the Google Colab CLI'
  homepage 'https://github.com/20Youssef10/golab'
  version '0.3.0'
  license 'Apache-2.0'

  on_macos do
    on_intel do
      url 'https://github.com/20Youssef10/golab/releases/download/v0.3.0/golab_0.3.0_darwin_amd64.tar.gz'
      sha256 '7026201245238ce5f518438adee2407e04fd6ea454ebb65071237eb6e7a512c4'
    end
    on_arm do
      url 'https://github.com/20Youssef10/golab/releases/download/v0.3.0/golab_0.3.0_darwin_arm64.tar.gz'
      sha256 '3a8c35b41206460bf66cd7c5b4e6f6da965dc17abf4bea174255d6ebf89f5e03'
    end
  end

  on_linux do
    on_intel do
      url 'https://github.com/20Youssef10/golab/releases/download/v0.3.0/golab_0.3.0_linux_amd64.tar.gz'
      sha256 '248d3099e7fb5b9ce13e24d32768b0aff1eaecc50b96f8e5e5037af3c2d76f88'
    end
    on_arm do
      url 'https://github.com/20Youssef10/golab/releases/download/v0.3.0/golab_0.3.0_linux_arm64.tar.gz'
      sha256 'a0f92813997fdad425bbf83b649aee78511278c4c587f1678d49d5245bc649de'
    end
  end

  def install
    bin.install 'golab'
  end

  test do
    assert_match 'golab', shell_output("#{bin}/golab version")
  end
end
