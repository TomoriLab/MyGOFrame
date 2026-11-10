# Release version management

`version.json` is the source of the application version, Debian revision, and release date. Application releases use Semantic Versioning. Debian revisions identify packaging changes independently. The current release is read with:

```sh
python3 tools/version.py show
python3 tools/version.py debian
python3 tools/version.py check
```

## Change a release

Use one command to update the canonical file and its derived manifests. Every new release requires a changelog note. Repeat `--note` for additional entries.

```sh
python3 tools/version.py bump patch --note 'Describe the application changes.'
python3 tools/version.py bump packaging --note 'Describe the packaging changes.'
python3 tools/version.py set 1.2.3-rc.1 --debian-revision 1 --date 2026-11-10 --note 'Prepare a release candidate.'
```

`bump` accepts `major`, `minor`, `patch`, and `packaging`. Application bumps reset the Debian revision to 1; a packaging bump keeps the application version and increments the Debian revision. `set` accepts an explicit Semantic Version and optional revision. The default date is the local date. These commands update Cargo.toml, the package entry in Cargo.lock, the Debian changelog, the manual header, and the latest AppStream release. Earlier changelog and AppStream entries remain as release history. A prerelease such as `1.2.3-rc.1` maps to Debian `1.2.3~rc.1-1`.

`sync` repairs derived files from the current canonical file. It requires `--note` when it must create a new changelog or AppStream release. `check` reports mismatches without repairing them. Build preparation runs this check and stops on drift. Validation and target discovery precede writes; failed synchronization restores changed files. Review the generated release notes and dates before publishing. The tool does not create commits or tags.

## Build identity

Make refreshes the generated identity before checking whether the executable needs rebuilding, so a changed Git revision or dirty state cannot retain stale version output. Unchanged metadata preserves the generated file timestamp and avoids unnecessary recompilation. The Go wrapper generates ignored, pure Go build information under `src/internal/version/domain/` before generating dependency notices. Both consume `version.json` and the same provenance rules. Bootstrap injects the information into desktop services. `--version`, `--version-json`, the native application version, and the About dialog use this release identity. About shows the application and Debian versions separately.

```sh
./build/mygoframe --version
./build/mygoframe --version-json
python3 tools/version.py metadata
```

Metadata includes the complete Git revision and working-tree modification state when available. Human-readable summaries shorten the revision to twelve characters. `SOURCE_DATE_EPOCH`, when supplied, records a reproducible source timestamp; generation does not insert the current build time.

The Debian helper records source revision and modification state in generated `build-source.json` before creating an archive. Source-package rebuilds use that provenance when `.git` is absent. Release numbers always come from `version.json`, including in source packages. Generated Go files are removed by Debian cleaning and regenerated before compilation.

ABI revisions, session and preset schemas, native-runtime manifest schemas, and supported dependency/toolchain versions have independent compatibility contracts. They are not application release numbers and are not changed by release synchronization.

## Verification

```sh
make check-version
python3 -m unittest discover -s tests -p 'test_version.py'
```

Tests cover release and packaging updates, preserved history, prerelease mapping, rejected invalid input, rollback, drift detection, and reproducible source-package metadata. Bootstrap tests compare the CLI build identity with the embedded About catalog. Building and testing a release does not publish a package or validate a Git tag.
