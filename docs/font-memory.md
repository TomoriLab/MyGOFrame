# Native text ownership and Fontconfig reports

The native text audit distinguishes application-owned resources, shared library caches, and detector limitations. A complete GUI LeakSanitizer report must retain every remaining allocation and its actual exit status. Passing the isolated ownership controls does not mean that the full GUI process is leak-free.

## Application-owned resources

MyGo 0.8.1 creates its own Pango font map and context. Its shared text system also owns cached Pango font descriptions, Cairo scaled fonts, and native font references held by Go `Font` wrappers. The upstream text system has no terminal release path. These objects are shared across windows, so closing one sharing window must not destroy them. The constructors transfer ownership to their caller: [PangoCairo font map](https://docs.gtk.org/PangoCairo/type_func.FontMap.new.html), [Pango context](https://docs.gtk.org/Pango/method.FontMap.create_context.html).

The build-local MyGo patch adds `ui.ShutdownText()`. MyGOFrame calls it after `App.Run()` has returned and application cleanup has completed. At this point the event loop has drained pending work and the windows have detached. A quit listener is too early because MyGo drains its final queued work afterward, and a window-close listener is too early because other windows can still use the shared text system.

Shutdown takes the text system's mutex, permanently replaces its native engine with a fontless fallback, invalidates cached layouts and glyph keys, and releases native resources. A delayed preload uses the same mutex and cannot reopen the Pango engine. Go atlas storage remains valid for retained snapshots. Repeated shutdown is harmless.

Native fonts use an exactly-once registry for individual owned references. Two wrappers may own separate references to the same Pango address. The registry therefore tracks reference leases rather than deduplicating native addresses. Runtime cleanup callbacks enqueue leases without calling Pango. Shutdown closes and drains the registry, stops pending cleanup handles, and releases native references under the text system's lock. Late callbacks observe retired leases and cannot release the same reference again. Shutdown does not wait for garbage collection or assume that a cleanup callback has already run.

The Pango engine destroys its scaled fonts and descriptions, releases all owned native font references, then unreferences its context and its font map. It does not terminate GTK's default font map or call process-global `FcFini`. The patch also destroys nonzero Cairo error-font results and releases a newly created font map if context creation fails.

The same audit found transient GLib ownership errors in the MyGo Linux backend. Every `g_variant_type_new` result now has a matching `g_variant_type_free` after its synchronous consumer returns. File-manager replies are unreferenced. Floating call parameters are released if bus acquisition fails before the call can consume them; nonfloating borrowed parameters retain their caller's ownership. The [GLib variant-type contract](https://docs.gtk.org/glib/ctor.VariantType.new.html) requires the allocated type to be freed.

## Fontconfig relative offsets

Fontconfig 2.17.1 stores `FcPattern` element arrays as offsets relative to the pattern object. Its accessor reconstructs the array address by adding the offset to the owner address. LeakSanitizer scans memory for pointers and cannot infer this library-specific relationship. A live pattern's internal array can therefore be reported as a direct leak even when the pattern has a legitimate owner and remains usable. See the version-matched [pattern representation](https://github.com/fontconfig/fontconfig/blob/2.17.1/src/fcint.h) and [allocation/destruction code](https://github.com/fontconfig/fontconfig/blob/2.17.1/src/fcpat.c).

The isolated Rust probe demonstrates the distinction in one process:

1. Retain one pattern in a static atomic owner. A recoverable LSan check reports 306 bytes in three internal allocations.
2. Read the pattern through `FcPatternGetString`; the value remains valid.
3. Release the legitimate owner with `FcPatternDestroy`. A second recoverable check reports no leaks and the process exits zero.

A separate negative control deliberately loses the owner. Its report includes the 24-byte owner as well, for 330 bytes in four allocations. An explicitly owned full configuration also exits cleanly when its caller destroys it. No suppression, ignored allocation, custom root, or leak-disabling option is used.

Pango's default Fontconfig configuration belongs to Fontconfig's process-global current configuration. Releasing a private Pango map does not transfer ownership of that shared configuration to the application. The native controls create and release one or five Pango map/context pairs and report the same residual configuration signature with no thread growth. This reproduces retained global configuration state rather than a per-window leak.

The tested native controls report 14,859 bytes in 223 allocations for that signature. Before terminal current-configuration release, the full GUI reported 14,841 bytes in 222 allocations. Both contain 43 element arrays and 91 value lists; one 18-byte string differs. The reason for that single string difference was not established. The classification applies to these reproduced stacks, not every allocation originating in Fontconfig.

Calling `FcFini` only to clear this report would reset state still shared with GTK or other font users. The [Fontconfig finalization contract](https://fontconfig.pages.freedesktop.org/fontconfig/fontconfig-devel/#FcFini) applies process-wide. The probe includes a paired initialization/finalization control only in a separate Fontconfig-only process with a sole initialization owner. Production shutdown uses `FcConfigSetCurrent(NULL)` after `ui.ShutdownText()` and native display shutdown. This public setter removes Fontconfig's current-configuration reference and calls `FcConfigDestroy` on that reference. It does not call `FcCacheFini`, destroy a borrowed configuration pointer, or reset the process-wide cache subsystem. Explicit configuration references and font objects remain valid. The version-matched [current-configuration setter](https://github.com/fontconfig/fontconfig/blob/2.17.1/src/fccfg.c) implements the reference transfer under the configuration lock.

This is terminal application cleanup, not per-window cleanup. Other windows, native widgets, and application workers stop before it runs. The native control keeps an explicit configuration reference across detachment, matches a font through that reference, releases the configuration, reads the retained font, and repeats initialization five times. Both this control and five Pango lifecycles followed by current-configuration release exit zero with unsuppressed ASan/LSan. The lost-owner and retained-pattern controls still produce their expected reports, so the cleanup does not disable detection.

## Verification

`make test` and `make test-race` include focused MyGo native-reference, terminal-shutdown, failure-rollback, and GLib ownership regression tests. They require no capture device. The optional native diagnostic is:

```sh
make test-font-integration
python3 tools/validate-font-memory.py --output build/font-memory.json
```

The diagnostic compiles `tests/font_ownership_probe.rs` against installed native runtime libraries. It requires the project's Rust/LLD sanitizer toolchain and a Linux font stack. It forces leak detection and nonzero leak exits, rejects suppression options, preserves raw child output, and checks expected positive and negative controls. A successful diagnostic means that its ownership and detector assertions matched their expected results. Expected owner-retained reports remain visible in its logs.

For the complete GUI, build with Go AddressSanitizer and run the same pattern/window-recreation scenario before and after the change. Record the full log, child exit code, source hashes, and actual allocation classes. Do not filter the Fontconfig signature out of the exit result or describe the whole application as leak-free when other native reports remain.
