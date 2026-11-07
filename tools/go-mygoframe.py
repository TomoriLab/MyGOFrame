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
os.execvp(go, [go, sys.argv[1], *flags, *sys.argv[2:]])
