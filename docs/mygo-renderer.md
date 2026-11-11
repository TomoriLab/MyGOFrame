# MyGo renderer patch

The application depends on the official `github.com/egoist/mygo v0.8.1` module. The patches add opt-in external images, GPU-ready observation, graphics-context cleanup, explicit GLES selection for DMA-BUF presentation, native text/GLib ownership fixes, and capability-gated event-driven paint requests.

`tools/prepare-mygo.py` applies the patch only inside the ignored build directory using Python 3 and GNU `patch`. It rejects another MyGo version or a module replacement. The checked-in `go.mod` and module cache remain unchanged. No dependency source tree is tracked in this repository or copied into `third_party/`.

Development builds copy the official cached module to `build/mygo-source/` and generate `build/mygo.mod` with a replacement pointing to that temporary source. `tools/go-mygoframe.py` supplies this alternate module file to Go. Use `make`, `make test`, and `make test-race` to include the renderer extension when building or testing the application. Direct `go build ./src/cmd/mygoframe` does not select the patch.

Debian source preparation vendors the official pinned dependencies. During its build, the helper copies only the affected vendor files to `build/mygo-overlay-source/` and generates `build/mygo-overlay.json`. Go receives this sparse source overlay and retains its normal vendor mode. The original vendor snapshot remains unchanged. Application build and test commands use the same wrapper in both environments.

The helper serializes preparation and caches results using its own contents, module metadata, original affected files, and patch contents. The sparse overlay tree has a generated module boundary so application patterns such as `go test ./...` do not traverse its incomplete packages. `make clean` removes generated sources, overlays, alternate module files, and the preparation cache. It preserves the source package's vendor snapshot.

The read-only `Window.GPUContextID()` observer reports an existing renderer identifier without making a context current or allocating a renderer. Timed UI checks use it to identify native window roles independently of context creation order.

The extension exposes `ui.ExternalImage`, `ui.NewExternalBitmap`, external image invalidation and retirement, and `Bitmap.SetGPUReadyCallback`. Texture preparation and normal release run with the owning GL context current. An optional `Abandon` method handles context loss. `MYGO_GL_API=gles` selects GLES before GTK creates its shared painting contexts and configures every native GL area for GLES 3. An explicitly set `GDK_GL` remains respected.

Update the patch and its tests before changing MyGo versions. The upstream dependency is MIT licensed and retains its original notices. The patch can be removed once equivalent public APIs are available upstream.

`ui.ShutdownText()` performs terminal cleanup of the shared native text engine after the application event loop and application cleanup finish. It preserves exactly-once font-reference ownership across pending runtime cleanups. Focused ownership tests run through `make test` and `make test-race`, including the sparse Debian vendor overlay. See [Native text ownership](font-memory.md) for the shutdown contract and terminal release of the Fontconfig current-configuration reference.

GLES GL areas use texture attachments even for opaque native windows. GTK 3.24.52 checks only `GL_ANGLE_framebuffer_blit` for GLES renderbuffer blits and otherwise reads the complete renderbuffer to a Cairo image on the CPU. Its texture composition path avoids that readback. Window transparency remains controlled by the original window options. See [GUI presentation](gui-presentation.md) for the reproduction and timing boundary.

The event-driven paint patch is paired with `features.eventDrivenPaint` in the verified private GDK runtime. Older bundles retain update-callback scheduling. See [native GUI presentation](gui-presentation.md) for the scheduling guards and recreation regression.
