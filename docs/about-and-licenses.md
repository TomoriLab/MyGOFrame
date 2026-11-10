# App information and license notices

The About card follows Presets in the native MyGo settings overview. It opens an information dialog rather than changing the current settings page. Developer identity and contact details match the repository's MIT license and Debian maintainer fields. The version comes from `debian/changelog`; Git revision and working-tree status describe the build inputs when Git metadata is available.

## Modal ownership and layout

`src/internal/desktop/presentation/about.go` owns the About and license dialogs. Both use MyGo `DialogBase` and the existing Fluent light/dark palette. They fit inside the window with a 12-pixel outer inset. The information body scrolls independently of its header. The license reader uses a read-only, selectable native text area with a separate copy action. Long license summaries elide in the header and remain available in a tooltip and the full notice body.

The license modal appears above About, which can appear above the compact Settings dialog. Escape or a backdrop click dismisses only the top modal. Closing these dialogs does not modify session state, capture choices, presets, or recording. No web view or additional production dependency is used.

## Catalog generation

`tools/go-mygoframe.py` prepares the MyGo renderer overlay and then runs `tools/prepare-about.py` before invoking Go. The generator writes an ignored Go constant under `src/internal/about/infrastructure/notices/` and an inspectable JSON snapshot at `build/about/catalog.json`. The executable embeds the complete catalog. `notices.Load` parses it once without filesystem or network access at runtime. Closed dialogs do not traverse the component list.

| Component group | Notice source |
| --- | --- |
| Application | Root `LICENSE` |
| Production Go modules | License, copyright, copying, notice, and patent files in modules reported by `go list -deps` with the active MyGo build flags |
| Go runtime | Toolchain license or Debian toolchain copyright |
| Swift, Foundation, libdispatch, ICU | Versioned files in `debian/runtime-notices/` |
| Rust runtime | Toolchain `COPYRIGHT-library.html`, converted to plain text, or Debian standard-library copyright |
| Private GTK/GDK, Fcitx, KDE modules | `native/notices/` and versions in `native/runtime-lock.json` |
| System libraries | Debian copyright files for resolved ELF dependencies and the established dynamic graphics/text loaders |

The system inventory uses `ldd`, `ldconfig`, and `dpkg-query` on the build host. Multiple binary libraries from one Debian source package share a catalog entry. References to `/usr/share/common-licenses/` are expanded into embedded full license texts. Optional CUDA/NVIDIA packages are labeled as vendor runtimes rather than open-source libraries. License summaries preserve upstream identifiers where available; the complete notices govern each component.

This is a build snapshot, not a scan of the installation machine. It includes dependency-package notices that can describe additional source files or optional features. It is not proof that every optional codec or driver was exercised. A release must regenerate the catalog on its actual build inputs and retain the existing corresponding-source and distribution notices required by the private runtime packaging.

Each document records its source label and SHA-256 of the original file. Identical documents share one stored copy. The generator rejects empty notices, caps each source file at 4 MiB, and caps the catalog at 24 MiB. Go string generation preserves Unicode and escapes byte-order marks that Go rejects inside source files. A build-local lock and atomic replacement serialize parallel preparation. Missing required module or runtime notice files fail the build instead of creating placeholder text.

Use the existing wrapper to regenerate and build:

```sh
CGO_ENABLED=1 python3 tools/go-mygoframe.py build -o build/mygoframe ./src/cmd/mygoframe
```

The Makefile tracks the generator, application license, version file, runtime lock, and checked-in notice files as executable inputs. System library changes require running the wrapper again to refresh the build snapshot. Do not edit generated catalog files or copy an external source tree into the repository.

## Verification

```sh
python3 -m unittest discover -s tests -p 'test_about_catalog.py'
LD_LIBRARY_PATH="$PWD/build" CGO_ENABLED=1 python3 tools/go-mygoframe.py test ./src/internal/desktop/presentation ./src/internal/about/infrastructure/notices
```

Native headless tests cover category placement, compact scrolling, developer details, complete MIT and third-party notices, read-only input, copying, nested dismissal, preserved capture state, light/dark appearance, and license bounds at 320 × 240, 640 × 480, and 1280 × 800. The catalog test checks every component's embedded document references and required direct dependency groups. Changes generated with coding assistance require human review and validation before release.
