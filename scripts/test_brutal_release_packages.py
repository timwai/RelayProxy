import io
import tarfile
import tempfile
import unittest
from pathlib import Path

from verify_brutal_release_packages import read_tar, safe_name, validate_agent_config


class ReleasePackageAuditTests(unittest.TestCase):
    def test_safe_archive_members_and_path_traversal(self):
        for valid in ("windows-amd64/relay-agent.exe", "darwin-arm64/RelayProxy.app/Contents/Info.plist"):
            self.assertTrue(safe_name(valid))
        for invalid in ("../secrets", "/etc/passwd", "a/../../etc", "a\\bad"):
            self.assertFalse(safe_name(invalid))

    def test_default_bbr_is_required_in_agent_package(self):
        config = b"transport:\n  bandwidth:\n    up_mbps: 0\n    down_mbps: 0 # default BBR\n"
        self.assertTrue(validate_agent_config(config))
        for changed in (config.replace(b"up_mbps: 0", b"up_mbps: 4"),
                        config.replace(b"down_mbps: 0", b"down_mbps: 16")):
            with self.assertRaisesRegex(ValueError, "default"):
                validate_agent_config(changed)

    def test_archives_reject_missing_files_and_links(self):
        with tempfile.TemporaryDirectory() as directory:
            file = Path(directory) / "bad.tar.gz"
            with tarfile.open(file, "w:gz") as archive:
                info = tarfile.TarInfo("linux-amd64/relay-agent")
                payload = b"production-binary-placeholder"
                info.size = len(payload)
                archive.addfile(info, io.BytesIO(payload))
            with self.assertRaisesRegex(ValueError, "missing"):
                read_tar(file, {"linux-amd64/relay-agent", "linux-amd64/relay-server"})

    def test_absolute_path_traversal_never_accepted(self):
        with tempfile.TemporaryDirectory() as directory:
            file = Path(directory) / "bad.tar.gz"
            with tarfile.open(file, "w:gz") as archive:
                info = tarfile.TarInfo("../bad")
                payload = b"not-allowed"
                info.size = len(payload)
                archive.addfile(info, io.BytesIO(payload))
            with self.assertRaisesRegex(ValueError, "unsafe"):
                read_tar(file, {"linux-amd64/relay-agent"})


if __name__ == "__main__":
    unittest.main()
