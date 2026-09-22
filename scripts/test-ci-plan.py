import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('ci_plan', Path(__file__).with_name('ci-plan.py'))
plan = importlib.util.module_from_spec(spec)
spec.loader.exec_module(plan)


class PlanTests(unittest.TestCase):
    def test_docs_do_not_build(self):
        self.assertFalse(any(plan.classify(['README.md', 'README.en.md', 'docs/preview-contract.md']).values()))

    def test_server_and_web_do_not_package(self):
        result = plan.classify(['internal/service/app.go', 'web/src/App.tsx'])
        self.assertTrue(result['core'] and result['web'])
        self.assertFalse(any(result[key] for key in ('windows', 'linux', 'macos')))

    def test_platform_isolation(self):
        for platform in ('windows', 'linux', 'macos'):
            with self.subTest(platform=platform):
                result = plan.classify([f'packaging/{platform}/launcher', f'scripts/release-{platform}.sh'])
                self.assertEqual([key for key in result if result[key]], [platform])
                self.assertEqual(len(plan.matrix(platform)['include']), 2)

    def test_common_packaging_validates_every_platform(self):
        result = plan.classify(['scripts/release-package.go'])
        self.assertTrue(all(result[key] for key in ('windows', 'linux', 'macos')))
        self.assertEqual(len(plan.matrix('all')['include']), 6)

    def test_invalid_platform_is_rejected(self):
        with self.assertRaises(ValueError):
            plan.matrix('unknown')

    def test_workflow_edit_does_not_force_full_packaging(self):
        result = plan.classify(['.github/workflows/preview-release.yml'])
        self.assertTrue(result['core'] and result['web'])
        self.assertFalse(any(result[key] for key in ('windows', 'linux', 'macos')))


if __name__ == '__main__':
    unittest.main()
