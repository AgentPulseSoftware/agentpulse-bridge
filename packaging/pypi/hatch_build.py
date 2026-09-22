"""Build hook: forces one platform wheel tag per invocation (BR-20).

manylinux_2_17 is honest here only because .goreleaser.yaml builds with
CGO_ENABLED=0. build_wheels.py sets AGENTPULSE_WHEEL_TAG to one of these
exact strings; this hook never infers a tag from the host.
"""

from __future__ import annotations

import os
from typing import Any

from hatchling.builders.hooks.plugin.interface import BuildHookInterface

VALID_WHEEL_TAGS = frozenset(
    {
        "py3-none-macosx_11_0_arm64",
        "py3-none-macosx_11_0_x86_64",
        "py3-none-manylinux_2_17_x86_64",
        "py3-none-manylinux_2_17_aarch64",
    }
)


class WheelTagBuildHook(BuildHookInterface):
    PLUGIN_NAME = "custom"

    def initialize(self, version: str, build_data: dict[str, Any]) -> None:
        if self.target_name != "wheel":
            return
        tag = os.environ.get("AGENTPULSE_WHEEL_TAG")
        if not tag or tag not in VALID_WHEEL_TAGS:
            raise ValueError(f"AGENTPULSE_WHEEL_TAG must be one of {sorted(VALID_WHEEL_TAGS)}, got {tag!r}")
        build_data["pure_python"] = False
        build_data["infer_tag"] = False
        build_data["tag"] = tag
