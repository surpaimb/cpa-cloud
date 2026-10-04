import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('ci_plan', Path(__file__).with_name('ci-plan.py'))
plan = importlib.util.module_from_spec(spec)
spec.loader.exec_module(plan)

race_spec = importlib.util.spec_from_file_location('ci_plan_service_race', Path(__file__).with_name('ci-plan-service-race.py'))
race_plan = importlib.util.module_from_spec(race_spec)
race_spec.loader.exec_module(race_plan)


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

    def test_service_race_plan_change_runs_core_without_packaging(self):
        result = plan.classify(['scripts/ci-plan-service-race.py'])
        self.assertTrue(result['core'] and result['web'])
        self.assertFalse(any(result[key] for key in ('windows', 'linux', 'macos')))

    def test_employee_wallet_classification_smoke_runs_core_without_packaging(self):
        result = plan.classify(['scripts/smoke-employee-self-wallet-entry-classification.mjs'])
        self.assertTrue(result['core'])
        self.assertFalse(any(result[key] for key in ('windows', 'linux', 'macos')))

    def test_employee_redemption_history_smoke_runs_core_without_packaging(self):
        result = plan.classify(['scripts/smoke-employee-self-redemption-credit-history.mjs'])
        self.assertTrue(result['core'])
        self.assertFalse(any(result[key] for key in ('windows', 'linux', 'macos')))

    # Independently authored for docs/employee-self-redemption-route-boundary-contract.md.
    def test_employee_redemption_route_boundary_smoke_runs_core_without_packaging(self):
        result = plan.classify(['scripts/smoke-employee-self-redemption-route-boundary.mjs'])
        self.assertTrue(result['core'])
        self.assertFalse(result['web'])
        self.assertFalse(any(result[key] for key in ('windows', 'linux', 'macos')))

    def test_employee_admin_adjustment_history_smoke_runs_core_without_packaging(self):
        result = plan.classify(['scripts/smoke-employee-self-admin-adjustment-history.mjs'])
        self.assertTrue(result['core'])
        self.assertFalse(any(result[key] for key in ('windows', 'linux', 'macos')))

    # Independently authored for docs/employee-self-topup-credit-history-contract.md.
    def test_employee_topup_credit_history_smoke_runs_core_without_packaging(self):
        result = plan.classify(['scripts/smoke-employee-self-topup-credit-history.mjs'])
        self.assertTrue(result['core'])
        self.assertFalse(any(result[key] for key in ('windows', 'linux', 'macos')))

    # Independently authored for docs/employee-self-estimated-cost-route-boundary-contract.md.
    def test_estimated_cost_boundary_smoke_runs_only_core(self):
        result = plan.classify(['scripts/smoke-employee-self-estimated-cost-route-boundary.mjs'])
        self.assertTrue(result['core'])
        self.assertFalse(result['web'])
        self.assertFalse(any(result[key] for key in ('windows', 'linux', 'macos')))

    # Independently authored for docs/employee-self-plan-purchase-route-boundary-contract.md.
    def test_plan_purchase_boundary_smoke_runs_only_core(self):
        result = plan.classify(['scripts/smoke-employee-self-plan-purchase-route-boundary.mjs'])
        self.assertTrue(result['core'])
        self.assertFalse(result['web'])
        self.assertFalse(any(result[key] for key in ('windows', 'linux', 'macos')))


class ServiceRaceShardTests(unittest.TestCase):
    def test_default_case_parser_excludes_benchmarks_and_rejects_unknown_output(self):
        listing = 'TestAlpha\nFuzzSeeds\nExampleUsage\nBenchmarkSlow\nok  \tcpacloud.local/server/internal/service\t0.1s\n'
        self.assertEqual(race_plan.parse_cases(listing), ['ExampleUsage', 'FuzzSeeds', 'TestAlpha'])
        for malformed in ('', 'TestAlpha\nTestAlpha\n', 'TestAlpha\nUNKNOWN\n'):
            with self.subTest(malformed=malformed):
                with self.assertRaises(ValueError):
                    race_plan.parse_cases(malformed)

    def test_fixed_shards_are_complete_disjoint_and_exactly_selectable(self):
        import re
        names = [f'TestCase{i:03d}' for i in range(50)] + ['FuzzSeeds', 'ExampleUsage']
        shards = race_plan.partition(names, 4)
        self.assertEqual(set().union(*(set(shard) for shard in shards)), set(names))
        self.assertEqual(sum(map(len, shards)), len(names))
        self.assertEqual(shards, race_plan.partition(list(reversed(names)), 4))
        for shard in shards:
            pattern = re.compile(race_plan.exact_regex(shard))
            self.assertEqual(sorted(name for name in names if pattern.fullmatch(name)), sorted(shard))
        with self.assertRaises(ValueError):
            race_plan.partition(['TestDuplicate', 'TestDuplicate'], 4)
        with self.assertRaises(ValueError):
            race_plan.partition(['TestOnly'], 4)


if __name__ == '__main__':
    unittest.main()
