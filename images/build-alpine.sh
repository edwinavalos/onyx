#!/usr/bin/env bash
# Build the Onyx base guest image: Alpine Linux (aarch64) as a raw ext4 root
# disk plus the matching kernel and initramfs, with onyx-guest baked in.
#
# Runs entirely inside an arm64 Docker container; the host needs Docker and
# nothing else. Output lands in images/out/.
set -euo pipefail

ALPINE_VERSION="${ALPINE_VERSION:-3.22}"
ROOTFS_SIZE_MB="${ROOTFS_SIZE_MB:-2048}"
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${REPO_ROOT}/images/out"
mkdir -p "${OUT}"

# Guest agent, static linux/arm64.
echo "==> building onyx-guest"
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags '-s -w' -o "${OUT}/onyx-guest" "${REPO_ROOT}/cmd/onyx-guest"

echo "==> building rootfs in docker (alpine ${ALPINE_VERSION})"
docker run --rm -i --platform linux/arm64 \
  -v "${OUT}:/out" \
  -v "${REPO_ROOT}/images/rootfs-overlay:/overlay:ro" \
  -e ALPINE_VERSION="${ALPINE_VERSION}" \
  -e ROOTFS_SIZE_MB="${ROOTFS_SIZE_MB}" \
  "alpine:${ALPINE_VERSION}" /bin/sh -s <<'INNER'
set -eu -o pipefail
R=/rootfs
MIRROR="https://dl-cdn.alpinelinux.org/alpine/v${ALPINE_VERSION}"

apk add --no-cache e2fsprogs

mkdir -p "$R"
apk --root "$R" --initdb --arch aarch64 --allow-untrusted \
  -X "${MIRROR}/main" -X "${MIRROR}/community" \
  add alpine-base linux-virt openrc \
      e2fsprogs blkid util-linux \
      bash sudo shadow ca-certificates curl git openssh-client openssh-server \
      nodejs npm tmux

# Repositories for in-guest apk use.
printf '%s/main\n%s/community\n' "${MIRROR}" "${MIRROR}" > "$R/etc/apk/repositories"


# Root has no password (serial console only; the VM is the boundary).
sed -i 's|^root:[^:]*:|root::|' "$R/etc/shadow"

# Work user: uid 1000, passwordless sudo, bash login shell.
chroot "$R" /bin/sh -c '
  adduser -D -u 1000 -s /bin/bash dev &&
  passwd -d dev >/dev/null &&
  echo "dev ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/dev && chmod 0440 /etc/sudoers.d/dev
'

# Coding harness. The chroot needs DNS; the guest gets its own resolv.conf
# from DHCP at boot so this copy is removed afterwards.
cp /etc/resolv.conf "$R/etc/resolv.conf"
echo "==> installing @anthropic-ai/claude-code"
chroot "$R" /bin/sh -c 'npm config set prefix /usr/local && npm install -g --no-fund --no-audit @anthropic-ai/claude-code' 2>&1 | tail -3
chroot "$R" /usr/local/bin/claude --version
rm -f "$R/etc/resolv.conf"

# Overlay: services, inittab, module list, etc.
cp -a /overlay/. "$R/"
install -m 0755 /out/onyx-guest "$R/usr/local/bin/onyx-guest"
chroot "$R" chown -R dev:dev /home/dev
# Enable services.
for svc in devfs dmesg mdev hwdrivers; do ln -sf "/etc/init.d/$svc" "$R/etc/runlevels/sysinit/$svc"; done
for svc in modules sysctl hostname bootmisc loopback; do ln -sf "/etc/init.d/$svc" "$R/etc/runlevels/boot/$svc"; done
for svc in networking sshd onyx-guest; do ln -sf "/etc/init.d/$svc" "$R/etc/runlevels/default/$svc"; done
for svc in mount-ro killprocs savecache; do ln -sf "/etc/init.d/$svc" "$R/etc/runlevels/shutdown/$svc"; done

# Kernel + initramfs out of the rootfs. Vz on arm64 wants an uncompressed
# Image. Alpine ships an EFI "zboot" wrapper (PE header, "zimg" magic at
# offset 4, then LE u32 payload offset and size) around a gzip'd Image.
cp "$R"/boot/initramfs-virt /out/initramfs
K="$R"/boot/vmlinuz-virt
if [ "$(dd if="$K" bs=1 skip=4 count=4 2>/dev/null)" = "zimg" ]; then
  off=$(od -An -tu4 -j8 -N4 "$K" | tr -d ' ')
  len=$(od -An -tu4 -j12 -N4 "$K" | tr -d ' ')
  echo "==> unwrapping zboot kernel (payload @${off}, ${len} bytes)"
  dd if="$K" bs=1 skip="$off" count="$len" 2>/dev/null | gzip -dc > /out/vmlinux
elif gzip -t "$K" 2>/dev/null; then
  gzip -dc "$K" > /out/vmlinux
else
  cp "$K" /out/vmlinux
fi

# Raw ext4 root disk populated straight from the directory tree.
rm -f /out/rootfs.img
truncate -s "${ROOTFS_SIZE_MB}M" /out/rootfs.img
mkfs.ext4 -q -F -L onyxroot -d "$R" /out/rootfs.img
echo "rootfs: $(du -h /out/rootfs.img | cut -f1) (sparse)"
INNER

echo "==> done:"
ls -lh "${OUT}"
