# Platform setup without Homebrew

The recommended installation with Homebrew brings Lima, Git and the platform
packages boxt needs. Use this page only when installing boxt from a release
archive or building from source without Homebrew.

After installing the prerequisites below, follow
[Install boxt](../README.md#install-boxt) for the archive or source install.

## macOS

```bash
brew install lima git
limactl --version
```

If Homebrew is not available, install [Lima](https://lima-vm.io/docs/installation/)
with your preferred package manager and install Git 2.42 or newer, for example
with the Xcode command-line tools (`xcode-select --install`).

Lima uses Apple Virtualization (`vz`) where available, so QEMU is not normally
required on macOS.

## Ubuntu / Debian

Install QEMU and `qemu-img` for your CPU architecture:

```bash
# x86_64
sudo apt-get update
sudo apt-get install qemu-system-x86 qemu-utils git

# aarch64
sudo apt-get update
sudo apt-get install qemu-system-arm qemu-utils git
```

Install Lima from the
[Lima installation guide](https://lima-vm.io/docs/installation/).

Verify `/dev/kvm` is enabled (see below).

## Fedora / RHEL

```bash
# x86_64
sudo dnf install qemu-system-x86 qemu-img git

# aarch64
sudo dnf install qemu-system-arm qemu-img git
```

Install Lima from the [Lima installation guide](https://lima-vm.io/docs/installation/).

Verify `/dev/kvm` is enabled (see below).

## KVM on Linux

Verify `/dev/kvm` exists and is usable by your user, and add yourself to the
`kvm` group (then start a new login session):

```bash
ls -l /dev/kvm
sudo usermod -aG kvm "$USER"
```

Without KVM, Lima falls back to slow QEMU software emulation (TCG). `boxt doctor`
reports a warning rather than failing.

## Windows 11 + WSL2

Install `limactl` and the QEMU packages **inside** the WSL environment by
following the instructions above that match your WSL distribution. WSL defaults
to Ubuntu.

Enable nested virtualization in `%UserProfile%\.wslconfig`:

```ini
[wsl2]
nestedVirtualization=true
memory=12GB
```

After changing `.wslconfig`, restart WSL with `wsl --shutdown`.

Verify `/dev/kvm` exists inside WSL (see above). Keep project files and boxt data
on the WSL ext4 filesystem, for example `~/projects/myapp`, not `/mnt/c/...`.
`boxt doctor` warns when a project is on a Windows-mounted path.

## Verify

Run `boxt doctor` after installing the prerequisites and boxt. It reports any
missing host dependency with the exact command or setting to fix it.

[Back to the README](../README.md#install)
