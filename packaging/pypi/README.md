# agentpulse (PyPI packaging)

`pip install agentpulse` puts the same Go binary Homebrew and
`install.sh` install onto `PATH` — no Python code runs in front of it.

## Layout

- `pyproject.toml` — `PYPI_NAME` (`"agentpulse"`) is defined exactly once,
  as `[project] name`. `VERSION` (written by the build script, never
  committed) supplies `dynamic = ["version"]`. `shared-scripts` installs
  the binary via the wheel's `.data/scripts/` mechanism, not
  `[project.scripts]` (a Python wrapper).
- `hatch_build.py` — build hook forcing one wheel tag from
  `AGENTPULSE_WHEEL_TAG`; never infers a tag from the host.
- `build_wheels.py` — discovers the four binaries from
  `dist/artifacts.json`, converts semver to PEP 440, builds and verifies
  one wheel per target.
- `test_build_wheels.py` — unit tests for the semver-to-PEP-440 table.

## The four tags (BR-20)

| GoReleaser target | Wheel tag |
| --- | --- |
| `darwin/arm64` | `py3-none-macosx_11_0_arm64` |
| `darwin/amd64` | `py3-none-macosx_11_0_x86_64` |
| `linux/amd64` | `py3-none-manylinux_2_17_x86_64` |
| `linux/arm64` | `py3-none-manylinux_2_17_aarch64` |

`manylinux_2_17` is honest only because `CGO_ENABLED=0` leaves no dynamic
libc dependency to be incompatible about. Installs via `shared-scripts`,
not a generated Python launcher: hatchling's build-hook API can force the
tag and place a scripts file, so the setuptools/`console_scripts`
fallbacks were not needed.

## Local verification (no network to pypi.org)

Run from the repository root, after `goreleaser build --snapshot --clean`:
```
python3 -m venv /tmp/ap-tools && /tmp/ap-tools/bin/pip install -q build hatchling twine
/tmp/ap-tools/bin/python packaging/pypi/build_wheels.py --dist dist --out dist/wheels --version 0.0.0
/tmp/ap-tools/bin/twine check dist/wheels/*.whl
/tmp/ap-tools/bin/python -m unittest discover -s packaging/pypi
python3 -m venv /tmp/ap-wheel && /tmp/ap-wheel/bin/pip install --no-index dist/wheels/<wheel for this machine>
/tmp/ap-wheel/bin/agentpulse version
```
Publishing happens only from a `v*` tag, only through the
`PYPI_API_TOKEN` secret, in one `twine upload` step in
`release.yml`. Nothing here contacts PyPI.
