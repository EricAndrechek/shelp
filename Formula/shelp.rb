# Homebrew formula for shelp.
#
# While the repo is local-only, tap it straight from disk:
#   brew tap ericandrechek/shelp ~/Code/public-projects/shelp
#   brew install ericandrechek/shelp/shelp
# Once it has a public home, point `url`/`head` at that remote instead.
class Shelp < Formula
  desc "Turn a task into a shell command, or explain one, with the model you have"
  homepage "https://github.com/ericandrechek/shelp"
  url "file:///Users/eric/Code/public-projects/shelp", using: :git, tag: "v0.1.0"
  license "MIT"
  head "file:///Users/eric/Code/public-projects/shelp", using: :git, branch: "main"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-X main.version=#{version}"), "./cmd/shelp"
  end

  def caveats
    <<~EOS
      Hook shelp into your shell so suggestions land on your prompt line:
        zsh:  echo 'eval "$(shelp init zsh)"' >> ~/.zshrc
        bash: echo 'eval "$(shelp init bash)"' >> ~/.bashrc
        fish: echo 'shelp init fish | source' >> ~/.config/fish/config.fish

      Then see which models work on this Mac:
        shelp providers

      Optional one-time setup:
        shelp setup shortcuts   # Apple Intelligence Pro via Shortcuts (click "Add Shortcut")
        sudo fm license         # Apple's on-device model (macOS 27+)
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/shelp version")
    assert_match "print -z", shell_output("#{bin}/shelp init zsh")
    assert_match "fm", shell_output("#{bin}/shelp providers")
  end
end
