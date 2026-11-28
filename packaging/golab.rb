# Homebrew formula for golab - unofficial Go port of the Google Colab CLI.
#
# HONESTY: this formula does NOT build from source and does NOT invent assets.
# It installs the same signed GitHub Release .tar.gz that install.sh verifies
# with sha256sum + cosign. The sha256 values below are copied verbatim from the
# published v0.4.0 checksums.txt (verified reachable at the URLs below).
# Homebrew's own SHA-256 check is the only gate here; the cosign keyless
# signature is checked by install.sh / `golab update --install`, not by brew.
#
# BUMP CHECKLIST (new annotated v<version> tag first):
#   1. copy the darwin_{amd64,arm64} and linux_{amd64,arm64} lines from that
#      release's checksums.txt into the four sha256 fields below;
#   2. replace every "0.4.0" in this file (version + 4 URLs);
#   3. keep internal/version.Number, packaging/golab.json, the winget files
#      and packaging/nfpm.yaml on the same version.
# Validate locally: ruby -c packaging/golab.rb ; brew audit --strict golab.rb
class Golab < Formula
  desc 'Unofficial Go port of the Google Colab CLI'
  homepage 'https://github.com/20Youssef10/golab'
  version '0.4.0'
  license 'Apache-2.0'

  on_macos do
    on_intel do
      url 'https://github.com/20Youssef10/golab/releases/download/v0.4.0/golab_0.4.0_darwin_amd64.tar.gz'
      sha256 'fa251d3c6272911faaa0311fecb5cb1bb5ef8f8914fb21dfa49f737f7152fcaa'
    end
    on_arm do
      url 'https://github.com/20Youssef10/golab/releases/download/v0.4.0/golab_0.4.0_darwin_arm64.tar.gz'
      sha256 '947404c361c878438e0ba808b7c02130231545e3b869ce333dbac190d229318f'
    end
  end

  on_linux do
    on_intel do
      url 'https://github.com/20Youssef10/golab/releases/download/v0.4.0/golab_0.4.0_linux_amd64.tar.gz'
      sha256 'ff1d218ee0dce37a9ee88a32632322b7f16eeaa024ca135082c4b7c17bc3fda5'
    end
    on_arm do
      url 'https://github.com/20Youssef10/golab/releases/download/v0.4.0/golab_0.4.0_linux_arm64.tar.gz'
      sha256 'aea6cfbde8e6d48c8f3aee9c9790ff210ea8ba8c117390a1debd536a634a044f'
    end
  end

  def install
    bin.install 'golab'
  end

  test do
    assert_match 'golab', shell_output("#{bin}/golab version")
  end
end
