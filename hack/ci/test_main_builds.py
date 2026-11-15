"""Selection of the components a push to main publishes as a main build."""

from pathlib import Path
import subprocess
import tempfile
import unittest

import yaml

import main_builds
from main_builds import changed_since, components, needs_build, select


ROOT = Path(__file__).resolve().parents[2]


def by_name():
    return {component["name"]: component for component in components()}


class ComponentTests(unittest.TestCase):
    def test_every_chart_in_the_repository_has_a_main_build(self):
        charts = {str(path.parent.relative_to(ROOT))
                  for pattern in ("deploy/charts/*/Chart.yaml", "providers/*/deploy/chart/Chart.yaml")
                  for path in ROOT.glob(pattern)}
        self.assertEqual({component["chart"] for component in components()}, charts)

    def test_images_match_the_chart_defaults(self):
        for component in components():
            with self.subTest(component=component["name"]):
                values = yaml.safe_load((ROOT / component["chart"] / "values.yaml").read_text())
                image = values["image"].get("hub", values["image"])
                self.assertEqual(image["repository"], f"ghcr.io/railgrid/{component['image']}")
                # An empty tag is what makes the chart's appVersion the image tag.
                self.assertFalse(image.get("tag"))
                self.assertTrue((ROOT / component["dockerfile"]).is_file())

    def test_companion_images_are_not_components(self):
        self.assertFalse({"access-proxy", "infrastructure/dev-agent", "infrastructure/universal-dev"} & set(by_name()))


class SelectionTests(unittest.TestCase):
    def selected(self, paths):
        return {name for name, component in by_name().items() if needs_build(component, paths)}

    def test_unknown_history_builds_everything(self):
        self.assertEqual(self.selected(None), set(by_name()))

    def test_nothing_changed_builds_nothing(self):
        self.assertEqual(self.selected([]), set())

    def test_documentation_builds_nothing(self):
        self.assertEqual(self.selected(["docs/providers.md", "README.md", "providers/code/README.md",
                                        "providers/edges/deploy/chart/README.md"]), set())

    def test_provider_change_builds_only_that_provider(self):
        self.assertEqual(self.selected(["providers/code/main.go"]), {"code"})
        self.assertEqual(self.selected(["providers/kuery/deploy/chart/templates/deployment.yaml"]), {"kuery"})
        self.assertEqual(self.selected(["providers/app-studio/portal/src/App.vue"]), {"app-studio"})

    def test_platform_change_builds_hub_and_agent(self):
        self.assertEqual(self.selected(["portal/src/App.vue"]), {"hub", "agent"})
        self.assertEqual(self.selected(["deploy/charts/railgrid-hub/values.yaml"]), {"hub", "agent"})
        # agents builds from the root module too (hack/ci/matrices.json).
        self.assertEqual(self.selected(["pkg/hub/server.go"]), {"hub", "agent", "agents"})

    def test_provider_sdk_builds_everything(self):
        self.assertEqual(self.selected(["provider-sdk/portalkit/tenant.ts"]), set(by_name()))

    def test_publishing_machinery_builds_everything(self):
        for path in main_builds.REBUILD_ALL:
            with self.subTest(path=path):
                self.assertEqual(self.selected([path]), set(by_name()))

    def test_each_component_is_compared_with_its_own_published_build(self):
        revisions = {"railgrid-code-provider": "aaaaaaaaaaaa", "railgrid-hub": "bbbbbbbbbbbb"}
        changes = {"aaaaaaaaaaaa": ["providers/code/main.go"], "bbbbbbbbbbbb": []}
        matrix, report = select("railgrid", revision=lambda chart, owner: revisions.get(chart),
                                changed=lambda revision: changes.get(revision))
        names = {entry["name"] for entry in matrix}
        self.assertIn("code", names)          # changed since its build
        self.assertNotIn("hub", names)        # unchanged since its build
        self.assertIn("kuery", names)         # never published
        self.assertEqual(len(report), len(by_name()))
        code = next(entry for entry in matrix if entry["name"] == "code")
        self.assertEqual(code, {"name": "code", "image": "railgrid/railgrid-code-provider",
                                "dockerfile": "./providers/code/Dockerfile", "chart": "providers/code/deploy/chart",
                                "chart-name": "railgrid-code-provider", "cache": "code-amd64"})

    def test_force_builds_everything_without_asking_the_registry(self):
        def unreachable(*args):
            raise AssertionError("force must not look anything up")
        matrix, _ = select("railgrid", force=True, revision=unreachable, changed=unreachable)
        self.assertEqual({entry["name"] for entry in matrix}, set(by_name()))


class GitTests(unittest.TestCase):
    def test_changed_since_reads_real_history_and_fails_closed(self):
        with tempfile.TemporaryDirectory() as tmp:
            def git(*args):
                return subprocess.run(["git", "-c", "user.name=t", "-c", "user.email=t@t", *args], cwd=tmp,
                                      check=True, capture_output=True, text=True).stdout.strip()
            git("init", "-q", "-b", "main")
            Path(tmp, "a").write_text("1")
            git("add", "."); git("commit", "-qm", "one")
            first = git("rev-parse", "HEAD")
            Path(tmp, "b c").write_text("2")
            git("add", "."); git("commit", "-qm", "two")
            self.assertEqual(changed_since(first[:12], cwd=tmp), ["b c"])
            self.assertEqual(changed_since(git("rev-parse", "HEAD"), cwd=tmp), [])
            # Unknown, malformed and missing revisions all mean "rebuild".
            for revision in ("0123456789ab", "HEAD~1", "", None):
                self.assertIsNone(changed_since(revision, cwd=tmp))
            # A published commit that is no longer in main's history.
            git("checkout", "-q", "-b", "side", first)
            Path(tmp, "d").write_text("3")
            git("add", "."); git("commit", "-qm", "side")
            side = git("rev-parse", "HEAD")
            git("checkout", "-q", "main")
            self.assertIsNone(changed_since(side, cwd=tmp))


class WorkflowTests(unittest.TestCase):
    def test_workflow_publishes_only_from_main_one_run_at_a_time(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/main-builds.yaml").read_text())
        events = workflow.get("on", workflow.get(True))
        self.assertEqual(events["push"], {"branches": ["main"]})
        self.assertEqual(workflow["concurrency"], {"group": "main-builds", "cancel-in-progress": False})
        self.assertEqual(workflow["env"]["CHART_VERSION"], main_builds.CHART_VERSION)
        jobs = workflow["jobs"]
        self.assertEqual(jobs["select"]["if"], "github.ref == 'refs/heads/main'")
        self.assertEqual(jobs["select"]["steps"][0]["with"]["fetch-depth"], 0)
        self.assertEqual(jobs["build"]["strategy"]["matrix"], "${{ fromJSON(needs.select.outputs.matrix) }}")
        steps = jobs["build"]["steps"]
        build = next(i for i, step in enumerate(steps) if step.get("uses", "").startswith("docker/build-push-action"))
        chart = next(i for i, step in enumerate(steps) if "helm push" in step.get("run", ""))
        self.assertLess(build, chart, "the chart must never name an image that is not pushed yet")
        self.assertEqual(steps[build]["with"]["platforms"], "linux/amd64")
        self.assertTrue(steps[build]["with"]["tags"].endswith(":${{ steps.tag.outputs.tag }}"))


if __name__ == "__main__":
    unittest.main()
