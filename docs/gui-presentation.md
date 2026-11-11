# Native GUI presentation

The Linux renderer uses MyGo's native GtkGLArea surfaces for both the controls window and the separate capture window. DMA-BUF ownership, GPU completion fences, latest-frame capture, and CPU fallback remain in the existing Go/Swift/Rust paths.

## GLES composition

GTK 3.24.52 normally allocates a renderbuffer for an opaque GtkGLArea. Its GLES blit check recognizes `GL_ANGLE_framebuffer_blit` but omits the GLES 3 core operation. The previous MyGo workaround requested an alpha-bearing texture to avoid software renderbuffer readback. GDK then uploaded a CPU Cairo background before blending that texture, even for opaque application windows. These paths are in the pinned source's `gtk/gtkglarea.c`, `gdk/gdkglcontext.c`, and `gdk/gdkgl.c`.

The private GTK patch uses an RGB8 texture for an opaque GLES area and retains RGBA8 for transparent areas. GDK can compose both through its existing texture path. On GLES 3.1 and newer, GDK queries the texture's alpha size and skips background upload and blending when it is zero. GLES 3.0 keeps GDK's conservative alpha assumption and remains on GPU texture composition. The [GLES texture specification](https://github.com/KhronosGroup/OpenGL-Refpages/blob/main/es3.0/glTexImage2D.xml) defines RGB sampling with an alpha value of one. Allocation, resizing, and destruction remain in GtkGLArea's existing texture ownership paths.

The verified runtime manifest advertises `features.glesOpaqueTexture`, which enables `MYGO_GL_GLES_OPAQUE_TEXTURE=1` before GTK initialization. MyGo then requests no alpha for opaque GLES windows. Older private bundles and system libraries retain the RGBA texture workaround. Native transparency still follows `WindowOptions.Transparent`; desktop OpenGL keeps its existing attachment policy. The [GtkGLArea alpha property](https://docs.gtk.org/gtk3/method.GLArea.set_has_alpha.html) controls attachment components rather than native window transparency. Set `MYGO_GL_GLES_OPAQUE_TEXTURE=0` to compare the previous RGBA composition path with the same runtime.

An opaque-renderbuffer blit candidate reduced CPU time but caused intermittent missed presentations in single-window NV12 controls. The final implementation retains texture composition for both windows and bounded frame requests. It adds no continuous redraw loop, frame queue, or per-window composition exception.

## Frame requests

The verified private runtime advertises `features.eventDrivenPaint` and enables `MYGO_GDK_EVENT_DRIVEN_PAINT=1` before GTK initialization. External GL invalidations queue native damage directly. GDK retires its independent smoothed-refresh deadline for these requests when no animation update or backend freeze is active and no paint callback is executing. Pending flush and paint sources are rescheduled through the existing priority and lifetime rules. The display refresh interval remains unchanged.

This avoids sampling a 60 Hz capture stream through an unrelated 60 Hz paint timer. Window recreation changes the relative phase of those clocks and previously reduced distinct-frame submission to 44–54 fps. Requests made during rendering still use a deferred GTK update because GtkGLArea clears its render request after emitting its render signal. Animation updates and backend throttling retain normal scheduling.

Older bundles and system libraries retain the coalesced update callback. `MYGO_GDK_EVENT_DRIVEN_PAINT=0` explicitly selects that path for comparison. Runtime capability tests cover activation, explicit overrides, downgrade cleanup, and system-library cleanup. Deferred callbacks stop when idle or closed; external damage adds no persistent timer.

This preserves bounded latest-frame presentation. It does not queue captured frames to force an apparent frame rate, change GPU fence release order, or claim that every incoming frame reaches physical scanout.

## Validation

`make test` and `make test-race` include native scheduling tests for two independent windows, request coalescing, continued streaming, idle removal, closed-window rejection, and requests during rendering. They also check attachment selection and native shutdown ordering.

For a performance check, run `tools/benchmark-video.py --format BGR3 --width 2560 --height 1440 --fps 60 --capture-window` on a free capture device. Keep the window geometry, compositor, display scale, source mode, and graphics-runtime selection equal across comparisons. Record per-context roles from the application; context creation order does not identify a window. GPU-ready sample rates are distinct from render-callback counts and physical scanout rates. Capture-to-texture-ready latency does not measure controller-to-display latency.

Whole-GUI memory checks use unsuppressed ASan/LSan and preserve raw reports and exit codes. Include native window recreation, fullscreen transitions, resizing, capture stop/restart, audio monitoring at zero volume, and forced GPU failure with CPU fallback. NVIDIA initialization under ASan can take tens of seconds on the tested driver, so the harness timeout must include initialization in addition to the requested UI duration.
