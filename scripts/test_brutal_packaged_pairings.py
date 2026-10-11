import io
import importlib.util
from pathlib import Path
import tarfile
import tempfile
import unittest
import zipfile

_runner_file = Path(__file__).with_name("ci-brutal-packaged-pairings.py")
_spec = importlib.util.spec_from_file_location("brutal_packaged_runner", _runner_file)
_runner = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_runner)
unpack_distributed_archive = _runner.unpack_distributed_archive


class ArchiveInteropTests(unittest.TestCase):
    def test_real_archive_validation_rejects_unsafe_names(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            archive=root/"example.tar.gz"
            with tarfile.open(archive,"w:gz") as tar:
                item=tarfile.TarInfo("../unsafe/relay-agent")
                content=b"malicious"
                item.size=len(content)
                tar.addfile(item,io.BytesIO(content))
            with self.assertRaisesRegex(ValueError,"unsafe"):
                unpack_distributed_archive(archive,root/"unpacked","linux-amd64")

    def test_win_zip_rejects_escape_and_missing_payload(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            archive=root/"example.zip"
            with zipfile.ZipFile(archive,"w") as zipstream:
                zipstream.writestr("../outside",b"bad")
                zipstream.writestr("windows-amd64/relay-agent.exe",b"test")
            with self.assertRaisesRegex(ValueError,"unsafe"):
                unpack_distributed_archive(archive,root/"unpacked","windows-amd64")

    def test_tar_validates_new_agent_and_server_presence(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            archive=root/"example.tar.gz"
            with tarfile.open(archive,"w:gz") as tar:
                for path in ("linux-amd64/relay-agent","linux-amd64/relay-server"):
                    content=b"packaged-test-program"
                    item=tarfile.TarInfo(path)
                    item.size=len(content)
                    tar.addfile(item,io.BytesIO(content))
            extracted=unpack_distributed_archive(archive,root/"unpacked","linux-amd64")
            self.assertEqual(b"packaged-test-program",(extracted/"relay-server").read_bytes())


if __name__ == "__main__":
    unittest.main()
