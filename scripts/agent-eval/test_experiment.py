import copy
import unittest
from pathlib import Path

import experiment as e


class ExperimentTest(unittest.TestCase):
    def test_live_baseline_requires_both_service_processes(self):
        with self.assertRaises(ValueError):
            e.verify_live_baseline({"processes": []})

    def test_retry_selection_only_includes_execution_failures(self):
        import tempfile
        with tempfile.TemporaryDirectory() as folder:
            out = Path(folder)
            for number, status in enumerate(["failed", "completed", "cancelled"], 1):
                e.save_new(out / "observations" / f"single-react-{number}.json",
                           {"case_id": "single-react", "trial": number, "status": status})
            self.assertEqual(e.failed_trials(out), [{"case_id": "single-react", "trial": 1}])

    def setUp(self):
        self.cases = [{"id": x, "category": "single" if x < "C" else "research"} for x in "ABCD"]
        self.rows = [{"case_id": case, "trial": i + 1, "pass": value}
                     for case, values in zip("ABCD", [[True, False, False], [False, True, True],
                                                     [False, False, False], [True, True, True]])
                     for i, value in enumerate(values)]

    def test_empirical_task_metrics(self):
        report = e.summarize(self.cases, self.rows, 3)
        self.assertEqual(report["overall"], {"tasks": 4, "trials": 12, "pass_at_1": .5,
                                           "pass_at_k": .75, "pass_pow_k": .25})
        self.assertEqual(report["categories"]["single"]["pass_at_k"], 1)
        self.assertEqual(report["categories"]["research"]["pass_pow_k"], .5)

    def test_incomplete_duplicate_unknown_or_ungraded_trials_are_not_scores(self):
        invalid = [self.rows[:-1], self.rows + [self.rows[0]],
                   self.rows + [{"case_id": "unknown", "trial": 1, "pass": True}]]
        for value in [None, "false", 1]:
            rows = copy.deepcopy(self.rows)
            rows[0]["pass"] = value
            invalid.append(rows)
        rows = copy.deepcopy(self.rows)
        rows[0]["trial"] = 0
        invalid.append(rows)
        for rows in invalid:
            with self.subTest(rows=rows), self.assertRaises(ValueError):
                e.summarize(self.cases, rows, 3)

    def test_all_rubric_items_and_evidence_must_pass(self):
        case = {"rubric": ["method correct", "limitations correct"]}
        observation = {"status": "completed", "answer": "report"}
        grade = {"checks": [True, True], "evidence_supported": True, "no_fabrication": True}
        self.assertTrue(e.verdict(case, observation, grade))
        self.assertFalse(e.verdict(case, observation, {**grade, "checks": [True, False]}))
        self.assertFalse(e.verdict(case, observation, {**grade, "evidence_supported": False}))
        self.assertFalse(e.verdict(case, {**observation, "status": "failed"}, grade))
        self.assertFalse(e.verdict(case, {**observation, "answer": ""}, grade))
        for checks in [[True], [True, "true"], [True, None]]:
            with self.assertRaises(ValueError):
                e.verdict(case, observation, {**grade, "checks": checks})

    def test_dataset_covers_approved_research_distribution(self):
        suite = e.load_suite(e.ROOT / "evals/agent/research-v1.json")
        counts = {kind: sum(c["category"] == kind for c in suite["cases"])
                  for kind in ["single", "comparison", "open"]}
        self.assertEqual(counts, {"single": 4, "comparison": 6, "open": 10})
        self.assertEqual(suite["repeats"], 5)
        for case in suite["cases"]:
            self.assertEqual(bool(case["sources"]), case["category"] != "open")

    def test_output_files_are_create_only(self):
        import tempfile
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "trial.json"
            e.save_new(path, {"pass": False})
            with self.assertRaises(FileExistsError):
                e.save_new(path, {"pass": True})
            self.assertIn("false", path.read_text())

    def test_stale_grade_cannot_score_a_different_observation(self):
        observation = b'{"answer":"changed"}'
        with self.assertRaises(ValueError):
            e.verify_grade_observation({"observation_sha256": e.digest(b"old")}, observation)
        e.verify_grade_observation({"observation_sha256": e.digest(observation)}, observation)

    def test_preparation_retry_never_replaces_an_admitted_trial(self):
        self.assertTrue(e.can_retry_preparation(False, ["failed", "ready"]))
        for admitted, states in [(True, ["failed"]), (False, ["processing"]),
                                  (False, ["failed", "processing"]), (False, []),
                                  (False, ["ready"])]:
            self.assertFalse(e.can_retry_preparation(admitted, states))


if __name__ == "__main__":
    unittest.main()
