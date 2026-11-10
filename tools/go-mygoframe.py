#!/usr/bin/env python3
"""Run Go with the build-local MyGo renderer patch."""

import importlib.util
import os
from pathlib import Path
import sys

source = Path(__file__).resolve().parent / "prepare-mygo.py"
specification = importlib.util.spec_from_file_location("mygoframe_go_build", source)
module = importlib.util.module_from_spec(specification)
specification.loader.exec_module(module)
go = os.environ.get("GO", "go")
if len(sys.argv) < 2:
    raise SystemExit("A Go subcommand is required.")
flags = module.prepare(go)
l10n_source = Path(__file__).resolve().parent / "prepare-l10n.py"
l10n_specification = importlib.util.spec_from_file_location("mygoframe_l10n_build", l10n_source)
l10n_module = importlib.util.module_from_spec(l10n_specification)
l10n_specification.loader.exec_module(l10n_module)
l10n_module.prepare()
version_source = Path(__file__).resolve().parent / "version.py"
version_specification = importlib.util.spec_from_file_location("mygoframe_version_build", version_source)
version_module = importlib.util.module_from_spec(version_specification)
version_specification.loader.exec_module(version_module)
version_module.generate()
about_source = Path(__file__).resolve().parent / "prepare-about.py"
about_specification = importlib.util.spec_from_file_location("mygoframe_about_build", about_source)
about_module = importlib.util.module_from_spec(about_specification)
about_specification.loader.exec_module(about_module)
about_module.prepare(go, flags)
os.execvp(go, [go, sys.argv[1], *flags, *sys.argv[2:]])
