from pathlib import Path
import sys
import unittest
from unittest.mock import Mock, patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import sandbox_storage_checks as storage


class StorageOwnershipTests(unittest.TestCase):
    def test_docker_startup_mode_is_accepted(self):
        for mode in ("700", "710"):
            with self.subTest(mode=mode), patch.object(storage, "run", side_effect=[
                    "750 1001 1001", "700 1001 1001", f"{mode} 1001 1001"]):
                storage.check_volume_ownership(Mock())

    def test_insecure_permissions_or_wrong_owner_are_rejected(self):
        for value in ("750 1001 1001", "770 1001 1001", "711 1001 1001",
                      "710 0 1001", "710 1001 0"):
            with self.subTest(value=value), patch.object(storage, "run", side_effect=[
                    "750 1001 1001", "700 1001 1001", value]):
                with self.assertRaisesRegex(ValueError, "Unsafe persistent volume ownership"):
                    storage.check_volume_ownership(Mock())
