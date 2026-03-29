class KindlingMac < Formula
  desc "Local Linux microVMs on macOS via Apple Virtualization Framework"
  homepage "https://github.com/kindlingvm/kindling"
  version "0.1.0"
  license "AGPL-3.0"

  on_macos do
    on_arm do
      url "https://github.com/kindlingvm/kindling/releases/download/v0.1.0/kindling-mac-arm64"
      sha256 "TODO: fill in after first release"
    end
  end

  resource "kindling" do
    url "https://github.com/kindlingvm/kindling/releases/download/v0.1.0/kindling"
    sha256 "TODO: fill in after first release"
  end

  def install
    bin.install Dir["*"].first => "kindling-mac"
    resource("kindling").stage do
      bin.install Dir["*"].first => "kindling"
    end
    (etc/"kindling-mac.yaml").write <<~EOS
      # ~/.kindling-mac.yaml
      # Configuration for kindling-mac daemon (local Linux microVMs on macOS)

      box:
        name: "box-1"
        cpus: 4
        memory_mb: 8192
        disk_mb: 51200
        auto_start: true
        shared_folders: []
        rosetta: false

      temp:
        cpus: 4
        memory_mb: 8192
        disk_mb: 20480
        shared_folders: []
        rosetta: false

      daemon:
        socket_path: "~/.kindling-mac/kindling-mac.sock"
        state_db: "~/.kindling-mac/state.db"
        guest_agent_path: "~/.kindling-mac/guest-agent"
        kernel_path: "~/.kindling-mac/vmlinuz"
        initramfs_path: "~/.kindling-mac/initramfs.cpio.gz"
    EOS
  end

  def post_install
    ohai "kindling-mac installed!"
    puts <<~EOS
      To start the daemon:
        kindling-mac

      To install as a login item (auto-start):
        cp #{plist_path} ~/Library/LaunchAgents/
        launchctl load ~/Library/LaunchAgents/com.kindling.kindling-mac.plist

      Copy the sample config:
        cp /usr/local/etc/kindling-mac.yaml ~/.kindling-mac.yaml

      Download the Linux kernel and initramfs:
        mkdir -p ~/.kindling-mac
        curl -fsSL https://github.com/kindlingvm/kindling/releases/download/v0.1.0/vmlinuz-arm64 -o ~/.kindling-mac/vmlinuz
        curl -fsSL https://github.com/kindlingvm/kindling/releases/download/v0.1.0/initramfs-arm64.cpio.gz -o ~/.kindling-mac/initramfs.cpio.gz

      First-time setup:
        kindling local box start
    EOS
  end

  def plist
    <<~EOS
      <?xml version="1.0" encoding="UTF-8"?>
      <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
      <plist version="1.0">
      <dict>
          <key>Label</key>
          <string>com.kindling.kindling-mac</string>
          <key>ProgramArguments</key>
          <array>
              <string>#{opt_bin}/kindling-mac</string>
          </array>
          <key>RunAtLoad</key>
          <true/>
          <key>KeepAlive</key>
          <true/>
      </dict>
      </plist>
    EOS
  end
end
