# macspike — macOS guest probe (issue #24)

Throwaway probe, not wired into `internal/vm`: boots a Tart-provisioned macOS
VM directly through Code-Hex/vz (no `tart` runtime), headless, NAT, vsock.
Findings are on issue #24.

```
cd hack/macspike
GOFLAGS=-mod=mod go build -o macspike . && codesign --force --sign - --entitlements ../../onyx.entitlements macspike
./macspike ~/.tart/vms/<name>        # reads config.json, nvram.bin, disk.img
```

Then `ssh admin@$(arp -an | grep <mac> ...)` (Tart images: admin/admin), and in
the guest `cc -o vsockprobe guest/vsockprobe.c && ./vsockprobe dial|listen`.

Env knobs: `MACSPIKE_NOVERSIONPORT=1` reproduces the reboot loop (the Tart
guest daemon SIGTERMs launchd unless a `tart-version-*` console port exists);
`MACSPIKE_HID`, `MACSPIKE_NOSOCK`, `MACSPIKE_NOAUDIO`, `MACSPIKE_DISPLAY=WxH@PPI`,
`MACSPIKE_NODIAL`, `MACSPIKE_NOMAINLOOP`, `MACSPIKE_STARTOPTS`.
