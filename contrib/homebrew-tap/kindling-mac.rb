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

  def install
    bin.install "kindling-mac" => "kindling-mac"
    bin.install "kindling" => "kindling"
    etc.install "contrib/kindling-mac.yaml" => "kindling-mac.yaml"
    plist.install "contrib/kindling-mac.plist"
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
