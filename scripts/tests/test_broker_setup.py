import os
from pathlib import Path
import runpy
import socket
import sqlite3
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
SETUP = runpy.run_path(str(ROOT / "infra/image/collab-broker-setup"))


class BrokerSetupTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(dir="/tmp")
        self.addCleanup(self.temporary.cleanup)
        self.path = Path(self.temporary.name) / "broker.sock"

    def test_stale_socket_is_removed_but_active_socket_is_retained(self):
        with socket.socket(socket.AF_UNIX) as server:
            server.bind(str(self.path))
            server.listen(1)
            with self.assertRaisesRegex(ValueError, "already active"):
                SETUP["remove_stale_socket"](self.path)
            self.assertTrue(self.path.exists())
        SETUP["remove_stale_socket"](self.path)
        self.assertFalse(self.path.exists())

    def test_regular_files_and_symlinks_are_never_unlinked(self):
        self.path.write_text("keep")
        with self.assertRaises(ValueError):
            SETUP["remove_stale_socket"](self.path)
        self.assertEqual(self.path.read_text(), "keep")
        self.path.unlink()
        self.path.symlink_to("missing")
        with self.assertRaises(ValueError):
            SETUP["remove_stale_socket"](self.path)
        self.assertTrue(self.path.is_symlink())

    def test_compatibility_alias_is_idempotent_and_rejects_existing_file(self):
        target = self.path.parent / "shared.sock"
        SETUP["alias"](self.path, target)
        SETUP["alias"](self.path, target)
        self.assertEqual(self.path.readlink(), target)
        self.path.unlink()
        self.path.write_text("keep")
        with self.assertRaises(ValueError):
            SETUP["alias"](self.path, target)
        self.assertEqual(self.path.read_text(), "keep")

    def test_migration_preserves_history_and_never_overwrites_current_database(self):
        state = self.path.parent
        (state / "broker").mkdir()
        source = state / "broker.db"
        with sqlite3.connect(source) as db:
            db.execute("PRAGMA journal_mode=WAL")
            db.execute("CREATE TABLE fixture(value TEXT)")
            db.execute("INSERT INTO fixture VALUES ('old')")
        SETUP["migrate_database"](state, os.getuid(), os.getgid())
        target = state / "broker/broker.db"
        with sqlite3.connect(target) as db:
            self.assertEqual(db.execute("SELECT value FROM fixture").fetchall(), [("old",)])
            db.execute("INSERT INTO fixture VALUES ('new')")
        SETUP["migrate_database"](state, os.getuid(), os.getgid())
        with sqlite3.connect(target) as db:
            self.assertEqual(db.execute("SELECT COUNT(*) FROM fixture").fetchone(), (2,))
        self.assertTrue(source.is_file())

    def test_retiring_shared_mode_removes_only_the_managed_alias(self):
        target = self.path.parent / "shared.sock"
        self.path.symlink_to(target)
        SETUP["retire_alias"](self.path, target)
        self.assertFalse(self.path.is_symlink())
        self.path.symlink_to("unrelated")
        SETUP["retire_alias"](self.path, target)
        self.assertEqual(self.path.readlink(), Path("unrelated"))
