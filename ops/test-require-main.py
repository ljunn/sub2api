"""Exercise the release gate in disposable Git repositories, without runtime access."""

import pathlib
import shutil
import subprocess
import tempfile
import unittest


class RequireMainTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="sub2api-main-gate-")
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        self.remote = self.root / "origin.git"
        self.repo = self.root / "source"
        self.run_cmd("git", "init", "--bare", "--initial-branch=main", str(self.remote))
        self.run_cmd("git", "clone", str(self.remote), str(self.repo))
        self.git("config", "user.name", "Release Test")
        self.git("config", "user.email", "release-test@example.invalid")
        (self.repo / "ops").mkdir()
        shutil.copy2(pathlib.Path(__file__).resolve().parent / "require-main.sh", self.repo / "ops")
        (self.repo / "tracked.txt").write_text("baseline\n")
        self.git("add", ".")
        self.git("commit", "-m", "初始化发布校验夹具")
        self.git("push", "-u", "origin", "main")
        self.head = self.git("rev-parse", "HEAD")

    def run_cmd(self, *args, cwd=None):
        result = subprocess.run(args, cwd=cwd, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout.strip()

    def git(self, *args):
        return self.run_cmd("git", *args, cwd=self.repo)

    def gate(self, allowed):
        result = subprocess.run(["bash", "ops/require-main.sh"], cwd=self.repo, capture_output=True, text=True)
        if allowed:
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.strip(), self.head)
        else:
            self.assertNotEqual(result.returncode, 0, "Unsafe release was accepted")
            self.assertNotEqual(result.stdout.strip(), self.head)

    def new_commit(self):
        self.git("commit", "--allow-empty", "-m", "新增尚未发布的提交")

    def test_clean_main(self):
        self.gate(True)

    def test_detached_same_main(self):
        self.git("checkout", "--detach")
        self.gate(True)

    def test_old_production_branch_rejected(self):
        self.git("switch", "-c", "host-production")
        self.gate(False)

    def test_feature_branch_rejected(self):
        self.git("switch", "-c", "feature")
        self.gate(False)

    def test_untracked_rejected(self):
        (self.repo / "pending.txt").write_text("pending")
        self.gate(False)

    def test_dirty_rejected(self):
        (self.repo / "tracked.txt").write_text("pending")
        self.gate(False)

    def test_staged_rejected(self):
        (self.repo / "tracked.txt").write_text("pending")
        self.git("add", "tracked.txt")
        self.gate(False)

    def test_unpushed_main_rejected(self):
        self.new_commit()
        self.gate(False)

    def test_fresh_remote_advance_rejected(self):
        peer = self.root / "peer"
        self.run_cmd("git", "clone", str(self.remote), str(peer))
        self.run_cmd("git", "-c", "user.name=Peer", "-c", "user.email=peer@example.invalid",
                     "commit", "--allow-empty", "-m", "远端主分支新增提交", cwd=peer)
        self.run_cmd("git", "push", "origin", "main", cwd=peer)
        self.assertEqual(self.git("rev-parse", "origin/main"), self.head, "Start with a stale tracking ref")
        self.gate(False)
        self.assertNotEqual(self.git("rev-parse", "origin/main"), self.head, "The gate must fetch")

    def test_detached_with_different_local_main_rejected(self):
        self.new_commit()
        self.git("checkout", "--detach", self.head)
        self.gate(False)

    def test_missing_local_main_rejected(self):
        self.git("checkout", "--detach")
        self.git("branch", "-d", "main")
        self.gate(False)

    def test_unavailable_origin_rejected(self):
        self.git("remote", "set-url", "origin", str(self.root / "missing.git"))
        self.gate(False)


if __name__ == "__main__":
    unittest.main()
