import importlib.util
import json
import os
from pathlib import Path
import tempfile
import subprocess
import unittest

spec = importlib.util.spec_from_file_location("media_cache", Path(__file__).with_name("media-cache.py"))
cache = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cache)


class MediaCacheTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.environment = {"RUNNER_OS": "Windows", "RUNNER_ARCH": "X64", "ImageOS": "win25", "ImageVersion": "20261020.1", "MEDIA_TARGET": "windows-amd64"}
        for name in cache.INPUTS:
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("fixture input")
        for name in ("ffmpeg/ffmpeg.exe", "ffmpeg/ffprobe.exe", "source/build-ffmpeg.sh", "licenses/license.txt"):
            path = self.root / "bundle" / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("fixture bytes")

    def test_every_build_input_and_native_toolchain_change_misses(self):
        key = cache.identity(self.root, "gcc version1", self.environment)
        self.assertEqual(key, cache.identity(self.root, "gcc version1", dict(reversed(list(self.environment.items())))))
        for name in cache.INPUTS:
            path = self.root / name
            path.write_text("changed")
            self.assertNotEqual(key, cache.identity(self.root, "gcc version1", self.environment), name)
            path.write_text("fixture input")
        for field in self.environment:
            changed = dict(self.environment, **{field: "different"})
            self.assertNotEqual(key, cache.identity(self.root, "gcc version1", changed), field)
        self.assertNotEqual(key, cache.identity(self.root, "gcc version2", self.environment))
        self.assertNotEqual(key, cache.identity(self.root, "gcc version1", dict(self.environment, CFLAGS="-O0")))
        with self.assertRaises((ValueError, KeyError)):
            cache.identity(self.root, "gcc version1", {})
        with self.assertRaises(ValueError):
            cache.identity(self.root, "", self.environment)

    def test_only_unversioned_pr_or_main_warming_can_use_test_key(self):
        workflow = Path(__file__).parent.parent.joinpath(".github/workflows/media-bundle.yml").read_text()
        guards = workflow.split('          if [ -z "$HHC_RELEASE_PUBLIC_KEY" ]; then')[1:]
        self.assertEqual(2, len(guards), "both macOS and Windows guards must be tested")
        for platform, guard in zip(("macOS", "Windows"), guards):
            script = 'if [ -z "$HHC_RELEASE_PUBLIC_KEY" ]; then' + guard.split('          go run ./cmd/hhc-release', 1)[0]
            for event, ref, version, accepted in (
                ("pull_request", "refs/pull/1/merge", "", True),
                ("push", "refs/heads/main", "", True),
                ("push", "refs/heads/main", "1.2.3", False),
                ("push", "refs/tags/v1.2.3", "1.2.3", False),
                ("workflow_dispatch", "refs/heads/main", "", False),
                ("workflow_call", "refs/heads/main", "1.2.3", False),
                ("pull_request", "refs/pull/1/merge", "1.2.3", False),
            ):
                with self.subTest(platform=platform, event=event, ref=ref, version=version):
                    env = dict(os.environ, HHC_RELEASE_PUBLIC_KEY="", HHC_RELEASE_VERSION=version, GITHUB_EVENT_NAME=event, GITHUB_REF=ref)
                    result = subprocess.run(["bash", "-eu", "-c", script], env=env, capture_output=True)
                    self.assertEqual(accepted, result.returncode == 0)
                    env["HHC_RELEASE_PUBLIC_KEY"] = "configured-public-key"
                    subprocess.run(["bash", "-eu", "-c", script], env=env, check=True, capture_output=True)

    def test_producer_outputs_reject_empty_or_multiple_artifacts_before_download(self):
        workflow = Path(__file__).parent.parent.joinpath(".github/workflows/media-bundle.yml").read_text()
        guard = workflow.split("      - name: Validate producer outputs", 1)[1].split("      - name: Download exact same-run", 1)[0]
        script = guard.split("        run: |\n", 1)[1]
        key = "media-v2-" + "a" * 64
        for artifact, candidate, accepted in (("123", key, True), ("", key, False),
                ("0", key, False), ("-1", key, False), ("1,2", key, False),
                ("123", "", False), ("123", "media-v1-" + "a" * 64, False),
                ("123", "media-v2-" + "A" * 64, False), ("123", key + "x", False)):
            with self.subTest(artifact=artifact, key=candidate):
                env = dict(os.environ, MEDIA_ARTIFACT_ID=artifact, MEDIA_INPUT_KEY=candidate)
                result = subprocess.run(["bash", "-eu", "-c", script], env=env, capture_output=True)
                self.assertEqual(accepted, result.returncode == 0)

    def test_native_compile_parallelism_is_bounded_by_cpus(self):
        recipe = Path(__file__).with_name("ffmpeg.sh").read_text()
        code = recipe.split("# Bound parallel compilation", 1)[1].split('\ncd "$media_build"', 1)[0]
        code = code.split("\n", 1)[1]
        for system in ("Darwin", "MINGW64_NT"):
            for cpus in (1, 3, 4, 16):
                script = f'uname() {{ echo {system}; }}; sysctl() {{ echo {cpus}; }}; nproc() {{ echo {cpus}; }};\n' + code + '\nprintf "%s" "$media_jobs"'
                result = subprocess.run(["bash", "-eu", "-c", script], check=True, capture_output=True, text=True)
                self.assertEqual(str(min(cpus, 4)), result.stdout)

    def test_handoff_rejects_source_target_identity_and_payload_mismatch(self):
        sha = "a" * 40
        cache.seal(self.root, "key")
        cache.handoff(self.root, "key", sha, "windows-amd64")
        cache.handoff(self.root, "key", sha, "windows-amd64", check=True)
        for key, source, target in (("other", sha, "windows-amd64"),
                                    ("key", "b" * 40, "windows-amd64"),
                                    ("key", sha, "darwin-arm64"),
                                    ("key", "", "windows-amd64")):
            with self.subTest(key=key, source=source, target=target):
                with self.assertRaises(ValueError):
                    cache.handoff(self.root, key, source, target, check=True)
        path = self.root / "handoff.json"
        saved = path.read_text()
        path.write_text(saved.replace("windows-amd64", "linux-amd64"))
        with self.assertRaises(ValueError):
            cache.handoff(self.root, "key", sha, "windows-amd64", check=True)
        path.write_text(saved)
        (self.root / "bundle/ffmpeg/ffmpeg.exe").write_text("changed")
        with self.assertRaises(ValueError):
            cache.handoff(self.root, "key", sha, "windows-amd64", check=True)

    def test_same_input_reuses_exact_bundle(self):
        cache.seal(self.root, "key")
        cache.verify(self.root, "key")
        with self.assertRaises(ValueError):
            cache.verify(self.root, "different-key")

    def test_modified_missing_and_extra_files_fail_before_execution(self):
        path = self.root / "bundle/ffmpeg/ffmpeg.exe"
        cache.seal(self.root, "key")
        for mutation in ("modified", "missing", "extra"):
            with self.subTest(mutation=mutation):
                if mutation == "modified": path.write_text("untrusted bytes")
                elif mutation == "missing": path.unlink()
                else: (self.root / "bundle/extra.exe").write_text("untrusted")
                with self.assertRaises(ValueError): cache.verify(self.root, "key")
                path.write_text("fixture bytes")

    def test_symlink_and_missing_sources_fail(self):
        path = self.root / "bundle/source/build-ffmpeg.sh"
        path.unlink()
        with self.assertRaises(ValueError): cache.seal(self.root, "key")
        path.symlink_to(self.root / "build/ffmpeg.sh")
        with self.assertRaises(ValueError): cache.seal(self.root, "key")

    def test_manifest_cannot_add_an_unchecked_file(self):
        cache.seal(self.root, "key")
        path = self.root / "cache-manifest.json"
        manifest = json.loads(path.read_text())
        manifest["files"]["../outside"] = "unchecked"
        path.write_text(json.dumps(manifest))
        with self.assertRaises(ValueError): cache.verify(self.root, "key")


if __name__ == "__main__":
    unittest.main()
