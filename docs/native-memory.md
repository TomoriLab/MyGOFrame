# Native display and input-method ownership

The NVIDIA signature is live driver-worker state retained by an initialized GDK-owned EGL display at Go shutdown. Fcitx loses two D-Bus introspection owners. The application-private runtime repairs those owners without changing the system installation or suppressing LeakSanitizer findings.

## NVIDIA worker state

On NVIDIA 615.78.08, an isolated Go/EGL control reproduces the complete GUI signature: a 65,536-byte NVIDIA worker buffer and six 512-byte glibc thread-specific-data arrays, totaling 68,608 bytes in seven allocations. Context destruction and `eglReleaseThread` leave the initialized display and six driver workers alive. Terminating the owned display returns the process from 16 threads to its six Go threads and removes all seven allocations. One-cycle and five-cycle controls finish with zero recoverable LSan reports and exit zero.

GDB observes NVIDIA workers calling libc's `pthread_create` directly, while Mesa workers use the ASan interceptor. Incomplete detector visibility of native worker roots is an inference from that trace and the live-state reclamation control. These allocations are not established permanently lost driver heap. Go 1.27 normal return runs its sanitizer hook before raw process exit, so native library finalizers cannot provide the required ordering.

GTK 3.24.52 initializes and retains the Wayland EGL display but omits termination in its original finalizer. The corrected GDK terminates its owned display before disconnecting Wayland. Rust continues to borrow that display. The application calls terminal `mygo.App.ShutdownNative()` after the event loop, worker/session cleanup, window retirement, and text cleanup. It rejects live windows, webviews, trays, modals, and popups. GDK assigns associated-resource cleanup to [`gdk_display_close`](https://docs.gtk.org/gdk3/method.Display.close.html).

## GDK lifetimes exposed by terminal shutdown

The GDK patch repairs reproduced incomplete lifetimes in ten upstream files:

| Owner | Repair |
| --- | --- |
| Display and monitors | Destroy registry/global/output proxies before connection teardown. |
| EGL display | Terminate its initialization and roll it back on API-binding failure. |
| Window | Balance its backend construction reference and retire callbacks, exports, foreign surfaces, and delayed implementation references. |
| Seat and input devices | Release construction references, stop sources, handle nullable protocol objects, and safely iterate self-removing lists. |
| Focus and pointer tracking | Release replaced pointer, keyboard, tablet, and drag focus references and the pointer-info window reference. Emit seat removal while its devices remain available to observers. |
| Drag context | Release its retained device reference in the base finalizer. |
| Cairo shared-memory surface | Retire buffer/pool proxies before disconnect and preserve mapped pixels until the last legitimate Cairo owner releases them. |

Wayland requires proxy and queue destruction before [`wl_display_disconnect`](https://wayland.freedesktop.org/docs/html/apb.html). Retained Cairo surfaces can outlive their GDK window and display. A nonowning registry retires native proxies while preserving the mapping. A mutex serializes this registry against concurrent final Cairo destruction. Display close releases only an existing in-flight compositor reference, after unlocking. It never resurrects a zero-reference surface or adds a detector root.

## Fcitx introspection ownership

Fcitx5GClient 5.1.7's `STATIC_INTERFACE_INFO` parses an owned `GDBusNodeInfo`, keeps a borrowed `interfaces[0]`, and loses the parent node. Two process-once uses account for 176 bytes in eight allocations. The repair takes an interface reference and unreferences the parsed node. The legitimate process-lifetime interface remains accessible and the existing Fcitx GTK input module remains enabled. [Upstream source](https://github.com/fcitx/fcitx5-gtk/blob/5.1.7/fcitx-gclient/fcitxgclient.c).

Installed Debian 5.1.7-1 and official 5.1.7-2 both reproduce this signature. Patched one-cycle and twenty-cycle input-context controls remove precisely those eight allocations. SONAME 2 and all 29 public exports are preserved.

## Runtime and detector boundaries

The approved private runtime contains corrected GTK, GDK, and Fcitx libraries. Pinned sources, minimal patches, notices, exports, SONAMEs, and hashes are checked by the [native build procedure](native-runtime-build.md). No complete dependency source tree is tracked. Upstream libraries retain their licenses.

GTK's internal tooltip can destroy its window from a display-closed callback. The original settings getter returns NULL as soon as the display is marked closed, while header-bar and label cleanup still needs the existing settings. The GTK repair retains access to an already-owned cached settings object during normal close handlers and performs its final release afterward. It never creates new settings for a closed display. A public tooltip ownership control reproduces the original NULL-object criticals and verifies their removal. Blind destruction of all GTK toplevels is unsafe because the tooltip still owns its internal window pointer.

The existing NVIDIA policy selects Mesa DRI/Zink only when its compatibility conditions permit it. Explicit graphics settings remain authoritative. Forcing the original NVIDIA GBM path can still reproduce its separate modifier-storage leak. See [EGL allocation paths](egl-leaks.md).

The original display/input-method verification retained the independently reproduced Fontconfig relative-offset reachability report of 14,841 bytes in 222 allocations and actual LSan exit 23. The subsequent terminal current-configuration release resolves that signature through the public Fontconfig API. No report filtering, suppression, ignored objects, added roots, global D-Bus shutdown, or private pointer freeing is used. A font-only result does not establish that the process exits cleanly under LSan or that every driver/input device is validated. See [Fontconfig controls](font-memory.md).

## Reproduction

```sh
make
CC=clang-23 CGO_ENABLED=1 python3 tools/go-mygoframe.py build -asan -o build/mygoframe-asan ./cmd/mygoframe
```

Remove inherited explicit graphics compatibility selectors before startup to exercise the tested automatic route. Keep `GTK_IM_MODULE=fcitx`, `ASAN_OPTIONS=detect_leaks=1:leak_check_at_exit=1:halt_on_error=1`, and `LSAN_OPTIONS=detect_leaks=1:leak_check_at_exit=1:exitcode=23`. Run cases sequentially and retain complete output, mapped library hashes, selected environment, and actual exits:

```sh
./build/mygoframe-asan --smoke-ui-pattern --capture-window --smoke-capture-window-cycles 0 --smoke-ui-seconds 8
./build/mygoframe-asan --smoke-ui-pattern --capture-window --smoke-capture-window-cycles 16 --smoke-ui-seconds 18
./build/mygoframe-asan --smoke-ui-video /dev/video0 --format NV12 --width 3840 --height 2160 --fps 60 --video-backend dma --capture-window --smoke-capture-window-cycles 3 --smoke-ui-seconds 10 --measure-latency
```

The device case requires `videoBackend=dmabuf`, rendered frames in both window roles, and nonempty GPU metrics. Texture-ready timings do not measure input-to-photon latency. A separate fully instrumented GDK checks accesses inside that library; Go ASan with release GDK alone does not instrument every native load/store.

The framework also owns nine native observer connections: five settings notifications, two display signals, and two seat signals. Terminal shutdown disconnects every handler before releasing any retained instance and before closing the display. This prevents late theme/font callbacks from querying closed GTK settings.

## KDE GTK module owners

KDE GTK Config 6.8.2's color-reload and window-decoration modules retain paths, `GFile` objects, file monitors, and callbacks without an unload hook. Closing GTK settings unloads their code and loses the path/file globals, producing an additional 268 bytes in six allocations on the tested configuration paths. A subsequent watched-file event can also reach unloaded callback code.

The approved private module builds add [`g_module_unload`](https://docs.gtk.org/gmodule/callback.ModuleUnload.html). They disconnect notifications, cancel and release monitors, release paths/files, and remove CSS providers using their original screen owner before code unload. A weak settings reference avoids a settings/module reference cycle. The controls verify provider installation/removal, theme changes, file events after unload, unmapped module code, and release/ASan builds. Module names and the desktop's theme selection remain unchanged.

The modules retain their GPL-2.0-only OR GPL-3.0-only OR LicenseRef-KDE-Accepted-GPL terms. The package includes the full upstream notices and exact corresponding source. The application's source remains MIT licensed. Explicit `GTK_PATH`, `GTK_EXE_PREFIX`, and absolute module selections remain authoritative; such overrides can select the original system modules instead of these fixes.
