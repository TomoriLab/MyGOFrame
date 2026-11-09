# Application-private native runtime build

MyGOFrame builds corrected GTK/GDK 3.24.52 and Fcitx5GClient 5.1.7 as separate application-private shared libraries. It also builds the KDE GTK color and window-decoration modules from kde-gtk-config 6.8.2 with explicit unload cleanup. The private GTK and GDK must preserve the installed version's complete 4,571-symbol and 761-symbol export sets respectively; Fcitx must preserve its 29-symbol set. Each GTK module exports `gtk_module_init` and `g_module_unload`; modules do not require a SONAME. The bundle does not replace system libraries. The build rejects a source, patch, SONAME, or export-set mismatch.

`native/runtime-lock.json` pins the original source archive URLs and SHA-256 values, local patch hashes, build options, and exported-symbol hashes. GTK and GDK share one pinned source/build owner; the two Debian production patches that prevent offscreen grabs are retained with their source-package provenance. Only this lock, minimal patches, and full upstream notices are tracked. Downloaded archives live in ignored `build/native-inputs/`; extracted source trees and intermediate objects live in `build/native-runtime-build/`.

## Build and offline inputs

Install the native build dependencies listed in `debian/control`, including the matching GTK 3.24.52 development package, Meson, Ninja, CMake, ECM, patchelf, Wayland development files/protocols, and a native C/C++ compiler. Go, Swift, Rust, and media-library requirements remain unchanged. Native cross compilation is not configured; build on the target architecture.

```sh
make native-inputs
make native-runtime
make
```

The first command downloads and verifies the three pinned upstream archives. Later commands reuse them after checking their hashes. To require an offline build:

```sh
make native-runtime NATIVE_RUNTIME_FLAGS=--offline
make NATIVE_RUNTIME_FLAGS=--offline
```

The helper disables Meson subproject downloads and verifies every patch before applying it to generated source. It removes Meson's temporary GDK build-tree RUNPATH from the staged GTK file before verifying all output libraries. Build logs are `build/native-runtime-build/gdk-build.log` and `fcitx-build.log`. A fingerprint records the lock, helper, system GTK file, native architecture, compiler/build-tool versions, flags, and dependency versions. A valid cached output avoids rebuilding. `NATIVE_CC` and `NATIVE_CXX` select native compilers; the default is `cc` and `c++`.

Published libraries are staged as new files and renamed into place. The manifest is published last. A running GUI keeps its existing mapped library inodes while a new build is prepared. A build does not truncate a library mapped by another process.

## Manifest and runtime selection

Development output is `build/native-runtime/`. The Debian package installs the same component set under `/usr/lib/<multiarch>/mygoframe/native/`:

| File | Role |
| --- | --- |
| `libgtk-3.so.0` | Corrected GTK settings and widget shutdown ownership with the existing public ABI. |
| `libgdk-3.so.0` | Corrected GDK with the existing public ABI. |
| `libFcitx5GClient.so.2` | Corrected Fcitx D-Bus client library used by the existing GTK input module. |
| `manifest.json` | Component hashes, SONAMEs, versions, architecture, system compatibility, and source provenance. |
| `gtk-3.0/modules/libcolorreload-gtk-module.so` | KDE color CSS reload module with monitor/provider unload cleanup. |
| `gtk-3.0/modules/libwindow-decorations-gtk-module.so` | KDE decoration CSS reload module with weak settings and unload cleanup. |

Manifest schema version 1 contains `architecture`, `multiarch`, `libraries.gtk`, `libraries.gdk`, `libraries.fcitx`, `systemCompatibility.gtk`, `systemCompatibility.fcitx`, and `sources`. Library records use a relative plain `file` basename plus `soname`, `version`, `sha256`, `exportsCount`, and `exportsSha256`. The system GTK file hash is a strict compatibility check. Fcitx system metadata records the optional system module's baseline and does not introduce a mandatory system Fcitx dependency.

The optional `gtkModules` map contains exactly `colorreload` and `windowDecorations` when modules are bundled. Its records use the relative module paths shown above plus versions, hashes, and export-set checks. With no explicit `GTK_PATH` or `GTK_EXE_PREFIX`, startup prepends the bundle's `gtk-3.0` directory to the module search path. Existing `GTK_MODULES` names and absolute module selections remain unchanged. Explicit module search paths retain their selection. `MYGOFRAME_GTK_MODULE_RUNTIME_ACTIVE` tracks only the owned module prefix so system mode can remove it independently of the library prefix.

The optional `features` map advertises capabilities of the verified libraries. `glesOpaqueTexture` identifies the pinned GTK patch that allocates RGB8 textures for opaque GLES areas. Startup enables `MYGO_GL_GLES_OPAQUE_TEXTURE=1` only after validating that bundle. `MYGOFRAME_GLES_TEXTURE_RUNTIME_ACTIVE` tracks the application-owned setting so an older bundle or system mode removes it. Explicit caller settings remain unchanged; `MYGO_GL_GLES_OPAQUE_TEXTURE=0` retains the previous RGBA texture path. Missing capability metadata also retains that fallback.

`MYGOFRAME_NATIVE_RUNTIME=auto` is the default. The startup loader validates the bundle before a single process-scoped re-execution selects its private directory. `MYGOFRAME_NATIVE_RUNTIME=system` selects the system libraries. `MYGOFRAME_NATIVE_RUNTIME_DIR=/absolute/directory` selects an explicit compatible bundle, including a modified local build with its own manifest. Explicit loader overrides retain their selection. No system loader configuration or library installation is changed.

`LD_PRELOAD` and `LD_AUDIT` preserve system-loader control. Dynamic tokens or semicolon separators in `LD_LIBRARY_PATH` bypass automatic selection; combining those with an explicit runtime directory fails clearly. Hardware-specific GTK variants under `glibc-hwcaps` also require system mode. These limits prevent validating a different GTK file from the one the native loader selects.

## Debian source and binary packages

`make deb` downloads and verifies inputs before creating the isolated source snapshot. It places the archives in generated `native-inputs/`, which is included in the orig tarball. Package configuration checks these inputs with `--offline`, and the complete build uses `NATIVE_RUNTIME_FLAGS=--offline`. Cleaning removes generated build products while preserving the source package's pinned archives. A prepared `.dsc` therefore rebuilds without fetching native sources.

The package includes the exact source archives, local patches, lock, helper, and complete copyright/license records under `/usr/share/doc/mygoframe/native-source/`. GTK/GDK and Fcitx retain their LGPL terms. The KDE modules retain `GPL-2.0-only OR GPL-3.0-only OR LicenseRef-KDE-Accepted-GPL`; complete upstream license-choice texts are included under `native/notices/KDE-*`. The application MIT license does not replace these terms. The source bundle has the same `tools/`, `native/`, and `native-inputs/` layout needed by the build helper. Packaging preserves these filenames without automatic documentation compression so a copy in a writable directory remains directly buildable. LGPL-covered libraries remain replaceable through an explicit compatible runtime directory and editable local manifest. See the complete [GNU LGPL 2.1 terms](https://www.gnu.org/licenses/old-licenses/lgpl-2.1.html) and the bundled notices.

Debian debug processing changes ELF bytes. After `dh_strip`, the packaging helper rechecks SONAMEs and export sets, then atomically refreshes the installed manifest's file hashes. Private libraries do not publish a system `shlibs` interface and do not register a global library search path.

## Verification

```sh
python3 -m unittest discover -s tests -p test_native_runtime_build.py -v
python3 tools/prepare-native-runtime.py --offline
python3 tools/prepare-native-runtime.py --refresh-manifest /path/to/extracted/native
```

Software tests cover corrupted/downloaded input rejection, archive path containment, manifest path containment, and preservation of an already-open previous library during publication. Native preparation verifies complete export sets and rejects RPATH/RUNPATH entries. Package verification must check the post-strip manifest, extract and launch the complete `.deb`, and run the full GUI ownership scenario. Passing build checks alone does not establish that the GUI is free of native leaks.
