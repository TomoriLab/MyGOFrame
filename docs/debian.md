# Debian packaging

The package installs MyGOFrame, its private Swift and Rust libraries, a desktop launcher, the rounded icon, AppStream metadata, and a manual page. The initial target is Debian sid on amd64. Runtime dependencies follow the FFmpeg and glibc ABI of the build host; the resulting binary is not intended for Debian stable.

## Build

Install the dependencies listed in `debian/control`. Provision the official Swift 6.3.3 Linux toolchain separately. The version is pinned because the package includes matching Swift, Foundation, dispatch, and ICU license notices. Debian's `swift` package provides OpenStack storage and is unrelated to the compiler. Rust 2024 support and Go 1.27.3 or newer are also required.

```sh
go mod download
make deb
```

`go mod download` prepares the pinned dependency cache and may access the network. `make deb` copies the source into an isolated temporary tree, vendors the cached modules, and builds without network module or Cargo access. The checkout's normal build is preserved. Artifacts are written to `build/debian/`: the application and debug-symbol packages, the `.dsc`, orig tarball, Debian source tarball, build information, and changes file. The artifacts are unsigned.

Use a custom compiler or output directory when needed:

```sh
SWIFTC=/path/to/swift/usr/bin/swiftc python3 tools/build-debian.py --output /path/to/packages
```

The helper enables `pkg.mygoframe.external-toolchains` to use the externally provisioned Swift compiler. Other Debian build dependencies remain checked. A clean Debian archive build is not provided because this compiler and MyGo are not packaged in the target archive.

## Install

```sh
sudo apt install ./build/debian/mygoframe_0.1.0-1_amd64.deb
mygoframe
```

The package installs the executable in `/usr/bin` and private libraries in `/usr/lib/<multiarch>/mygoframe`. Swift standard and Foundation libraries are linked statically into the private facade using LLD so the installed core retains RELRO protection. The installed application therefore requires no Swift toolchain or home-directory loader path. FFmpeg, PipeWire, GTK, and graphics libraries remain system dependencies. GTK and graphics dependencies are declared explicitly because MyGo loads them dynamically.

The package uses the session's existing V4L2 and PipeWire permissions. It does not change groups or device permissions. A user launcher previously installed with `make install-desktop` can shadow the packaged launcher; remove that user launcher if it still points to a moved checkout.

## Rebuild and validation

See `debian/README.source` for rebuilding the `.dsc` source package. The source includes the pinned Go vendor snapshot and runtime license notices. The package build runs the standard software tests unless `DEB_BUILD_OPTIONS=nocheck` is set. Hardware and sanitizer checks remain explicit commands in [Verification](verification.md).

```sh
lintian build/debian/mygoframe_0.1.0-1_amd64.changes
apt-get -s install ./build/debian/mygoframe_0.1.0-1_amd64.deb
dpkg-deb -I build/debian/mygoframe_0.1.0-1_amd64.deb
```

The loader paths use executable-relative private directories. Package validation should extract and execute the `.deb` with `LD_LIBRARY_PATH` unset, check that all ELF dependencies resolve, and exercise the native window and capture window. The [Debian shared-library policy](https://www.debian.org/doc/debian-policy/ch-sharedlibs.html) distinguishes these private libraries from a public library ABI.
