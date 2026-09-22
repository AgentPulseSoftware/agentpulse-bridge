"""Unit tests for build_wheels.semver_to_pep440."""

from __future__ import annotations

import unittest

from build_wheels import semver_to_pep440


class SemverToPep440Test(unittest.TestCase):
    def test_plain_semver_passes_through(self) -> None:
        self.assertEqual(semver_to_pep440("1.2.3"), "1.2.3")
        self.assertEqual(semver_to_pep440("0.0.0"), "0.0.0")

    def test_rc_prerelease(self) -> None:
        self.assertEqual(semver_to_pep440("1.2.3-rc.1"), "1.2.3rc1")

    def test_beta_prerelease(self) -> None:
        self.assertEqual(semver_to_pep440("1.2.3-beta.2"), "1.2.3b2")

    def test_alpha_prerelease(self) -> None:
        self.assertEqual(semver_to_pep440("1.2.3-alpha.5"), "1.2.3a5")

    def test_snapshot_prerelease_becomes_local_version(self) -> None:
        self.assertEqual(
            semver_to_pep440("1.2.3-snapshot-8d6880b"),
            "1.2.3+snapshot.8d6880b",
        )

    def test_unrecognised_prerelease_is_a_hard_error(self) -> None:
        with self.assertRaises(ValueError):
            semver_to_pep440("1.2.3-dev.1")

    def test_not_a_semver_is_a_hard_error(self) -> None:
        with self.assertRaises(ValueError):
            semver_to_pep440("v1.2.3")
        with self.assertRaises(ValueError):
            semver_to_pep440("1.2")


if __name__ == "__main__":
    unittest.main()
